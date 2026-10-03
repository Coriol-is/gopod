# Durable Turns Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every agent turn is a SQLite row before it runs, so a gopod crash or restart loses no Telegram message and no scheduled run, and in-flight turns resume on boot.

**Architecture:** The queue (`internal/queue`) owns durability: `Enqueue` inserts a `turns` row first, workers mark it `running`/`done`/`failed`/`interrupted`, and `Recover` re-enqueues unfinished rows at boot with `Resumed=true`. The Telegram handler turns a resumed item into a `--continue` turn with an interruption notice and records placeholder/reply message ids on the row as a first-write-wins memo. The scheduler finalizes `task_run_logs` through a queue completion hook instead of fire-and-forget.

**Tech Stack:** Go 1.25, `ncruces/go-sqlite3` via `internal/store`, `go-telegram/bot`, stdlib `testing` only.

**Spec:** `docs/superpowers/specs/2026-10-03-durable-turns-design.md`

**Commit steps:** CLAUDE.md forbids committing without an explicit ask. Run each "Commit" step only if the operator approved per-task commits when choosing the execution method. Otherwise `git add` the files and leave the commit to the operator.

## Global Constraints

- Go only, no CGO, no new dependencies (stdlib `testing`, no testify).
- All DB access through `internal/store`; `store` never imports `queue`.
- `log/slog` structured logging; no `fmt.Println` outside `cmd/`.
- Errors wrapped `fmt.Errorf("doing X: %w", err)`; no silent swallowing.
- Max attempts before a turn is declared a crash loop: constant `3`, no env var.
- Turn status strings exactly: `pending`, `running`, `done`, `failed`, `interrupted`.
- Source strings exactly: `telegram`, `task`. Task `source_id` = `<task_id>:<next_run>`.
- Nil `TurnStore` keeps today's memory-only behaviour.
- Existing queue semantics unchanged: per-chat serialization, global cap, coalescing (handler runs the last item), exponential backoff.
- Run `go build ./... && go vet ./... && go test ./...` before every commit.

## Review Focus

Inputs the spec implies but no happy-path test covers. Each has a pinned test in the owning task.

1. **Enqueue when the DB write fails** (disk full, locked): message must not run twice and must not crash the handler; expected: log, drop, return `(0,false)`. → Task 4 `TestEnqueueStoreErrorDrops`.
2. **Recovered row whose chat no longer exists** (`registered_chats` row deleted while down): handler must fail cleanly, row ends `failed`, not a crash loop forever. → Task 7 `TestResumedUnknownChatMarksFailed` (handler returns nil, row done; documented).
3. **Two rows for the same chat recovered at once** (one `running`, one `pending`): must stay serialized and in id order. → Task 5 `TestRecoverPreservesOrderPerChat`.
4. **Placeholder deleted by the user before restart**: edit fails, must fall back to a fresh placeholder, not drop the reply. → Task 7 covered by `reusePlaceholder` returning 0 on error; test `TestReusePlaceholderFallsBack`.
5. **Scheduler slot enqueued twice after restart** (poll fires for a `next_run` that was already bumped in the crashed process): dup insert must mark the log row `skipped`, not run twice. → Task 8 `TestRunTaskDupMarksSkipped`.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/store/schema.go` (modify) | `turns` table + index; `task_run_logs.turn_id` migration |
| `internal/store/turns.go` (create) | `Turn` type, status consts, CRUD for turns |
| `internal/store/turns_test.go` (create) | turns CRUD, dup, unfinished listing, migration |
| `internal/store/tasks.go` (modify) | `SetTaskRunTurn`, `FinishTaskRun` |
| `internal/store/tasks_test.go` (modify) | task run finalize tests |
| `internal/queue/queue.go` (modify) | `TurnStore`, `Item` fields, insert/mark, `OnDone`, ctx-derived workers, `Close`, `Recover`, `MaxAttempts` |
| `internal/queue/queue_test.go` (modify) | fake `TurnStore`, new behaviour tests |
| `internal/telegram/resume.go` (create) | `resumePrompt`, `reusePlaceholder`, `recordReply`, `NewDoneHook` |
| `internal/telegram/resume_test.go` (create) | pure-function tests |
| `internal/telegram/handler.go` (modify) | `Enqueue` call fills `Source/SourceID`; `runAgentSync` memo + outbound; `NewAgentHandler` resumed short-circuit; `sendFormatted` returns message id |
| `internal/telegram/streaming.go` (modify) | placeholder reuse, memo writes |
| `internal/scheduler/scheduler.go` (modify) | `running` log row, `SourceID`, `turn_id`, completion hook |
| `internal/scheduler/scheduler_test.go` (modify) | runTask + hook tests with a real store and a nil-handler queue |
| `cmd/gopod/main.go` (modify) | `turnStoreAdapter`, `queue.New(ctx, ...)`, `Recover`, `Close`, done hook wiring |
| `docs/ARCHITECTURE.md`, `docs/DECISIONS.md`, `docs/HANDOFF.md`, `ROADMAP.md` (modify) | schema, ADR D020, state |

---

### Task 1: `turns` table and store CRUD

**Files:**
- Modify: `internal/store/schema.go` (append to `schemaStatements` after the `router_state` statement; append to `migrations`)
- Create: `internal/store/turns.go`
- Create: `internal/store/turns_test.go`

**Interfaces:**
- Consumes: `Store.db`, `nullableString`, `nullableInt64`, `boolToInt` from `messages.go`.
- Produces:
  ```go
  const (
      TurnPending     = "pending"
      TurnRunning     = "running"
      TurnDone        = "done"
      TurnFailed      = "failed"
      TurnInterrupted = "interrupted"
  )
  type Turn struct {
      ID               int64
      Source           string
      SourceID         string
      ChatFolder       string
      ChatID           int64
      TGMessageID      int64
      IsOwner          bool
      IsVoice          bool
      Text             string
      FilePath         string
      Status           string
      Attempts         int
      PlaceholderMsgID int
      ReplyMsgID       int
      Error            string
      CreatedAt        int64
      StartedAt        int64
      FinishedAt       int64
  }
  func (s *Store) InsertTurn(ctx context.Context, t Turn) (id int64, dup bool, err error)
  func (s *Store) MarkTurnRunning(ctx context.Context, id int64) error
  func (s *Store) MarkTurnFinished(ctx context.Context, id int64, status, errText string) error
  func (s *Store) SetTurnPlaceholder(ctx context.Context, id int64, msgID int) error
  func (s *Store) SetTurnReply(ctx context.Context, id int64, msgID int) error
  func (s *Store) GetTurn(ctx context.Context, id int64) (Turn, error)
  func (s *Store) ListUnfinishedTurns(ctx context.Context) ([]Turn, error)
  var ErrTurnNotFound = errors.New("store: turn not found")
  ```

- [ ] **Step 1: Write the failing tests**

`internal/store/turns_test.go`:

```go
package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "store.sqlite"), silentLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleTurn() Turn {
	return Turn{
		Source: "telegram", SourceID: "42", ChatFolder: "owner", ChatID: 100,
		TGMessageID: 42, IsOwner: true, Text: "hello", Status: TurnPending,
	}
}

func TestInsertTurnAssignsIDAndDefaults(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, dup, err := s.InsertTurn(ctx, sampleTurn())
	if err != nil || dup || id == 0 {
		t.Fatalf("InsertTurn = (%d, %v, %v), want (>0, false, nil)", id, dup, err)
	}
	got, err := s.GetTurn(ctx, id)
	if err != nil {
		t.Fatalf("GetTurn: %v", err)
	}
	if got.Status != TurnPending || got.Attempts != 0 || got.CreatedAt == 0 {
		t.Errorf("row = %+v, want pending/0 attempts/created_at set", got)
	}
	if got.Text != "hello" || got.ChatFolder != "owner" || !got.IsOwner || got.TGMessageID != 42 {
		t.Errorf("row fields not round-tripped: %+v", got)
	}
}

func TestInsertTurnDuplicateIsNoop(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id1, _, err := s.InsertTurn(ctx, sampleTurn())
	if err != nil {
		t.Fatal(err)
	}
	id2, dup, err := s.InsertTurn(ctx, sampleTurn())
	if err != nil {
		t.Fatal(err)
	}
	if !dup || id2 != 0 {
		t.Errorf("second insert = (%d, %v), want (0, true)", id2, dup)
	}
	rows, err := s.ListUnfinishedTurns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != id1 {
		t.Errorf("unfinished = %+v, want exactly the first row", rows)
	}
}

func TestInsertTurnRejectsEmptyKeys(t *testing.T) {
	s := openTestStore(t)
	tr := sampleTurn()
	tr.SourceID = ""
	if _, _, err := s.InsertTurn(context.Background(), tr); err == nil {
		t.Error("empty SourceID accepted")
	}
	tr = sampleTurn()
	tr.ChatFolder = ""
	if _, _, err := s.InsertTurn(context.Background(), tr); err == nil {
		t.Error("empty ChatFolder accepted")
	}
}

func TestMarkTurnRunningIncrementsAttempts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, _, _ := s.InsertTurn(ctx, sampleTurn())
	for i := 1; i <= 2; i++ {
		if err := s.MarkTurnRunning(ctx, id); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetTurn(ctx, id)
		if got.Status != TurnRunning || got.Attempts != i || got.StartedAt == 0 {
			t.Errorf("after MarkTurnRunning #%d: %+v", i, got)
		}
	}
}

func TestMarkTurnFinishedSetsStatusAndError(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, _, _ := s.InsertTurn(ctx, sampleTurn())
	_ = s.MarkTurnRunning(ctx, id)
	if err := s.MarkTurnFinished(ctx, id, TurnFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTurn(ctx, id)
	if got.Status != TurnFailed || got.Error != "boom" || got.FinishedAt == 0 {
		t.Errorf("row = %+v", got)
	}
	if err := s.MarkTurnFinished(ctx, 9999, TurnDone, ""); !errors.Is(err, ErrTurnNotFound) {
		t.Errorf("unknown id err = %v, want ErrTurnNotFound", err)
	}
}

func TestSetTurnPlaceholderAndReply(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, _, _ := s.InsertTurn(ctx, sampleTurn())
	if err := s.SetTurnPlaceholder(ctx, id, 7); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTurnReply(ctx, id, 8); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTurn(ctx, id)
	if got.PlaceholderMsgID != 7 || got.ReplyMsgID != 8 {
		t.Errorf("memo = (%d, %d), want (7, 8)", got.PlaceholderMsgID, got.ReplyMsgID)
	}
}

func TestListUnfinishedTurnsFiltersAndOrders(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mk := func(sid, status string) int64 {
		tr := sampleTurn()
		tr.SourceID = sid
		id, _, _ := s.InsertTurn(ctx, tr)
		if status != TurnPending {
			_ = s.MarkTurnRunning(ctx, id)
			if status != TurnRunning {
				_ = s.MarkTurnFinished(ctx, id, status, "")
			}
		}
		return id
	}
	a := mk("1", TurnRunning)
	mk("2", TurnDone)
	b := mk("3", TurnPending)
	mk("4", TurnFailed)
	c := mk("5", TurnInterrupted)
	rows, err := s.ListUnfinishedTurns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].ID != a || rows[1].ID != b || rows[2].ID != c {
		t.Errorf("unfinished ids = %v, want [%d %d %d]", ids(rows), a, b, c)
	}
}

func ids(rows []Turn) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/ -run 'Turn' -v`
Expected: compile error, `undefined: Turn` / `InsertTurn`.

- [ ] **Step 3: Add the schema statements**

In `internal/store/schema.go`, append to `schemaStatements` right after the `router_state` statement (before the `// --- Memory layer 3` comment):

```go
	// --- Durable agent turns (docs/superpowers/specs/2026-10-03-durable-turns-design.md).
	`CREATE TABLE IF NOT EXISTS turns (
	  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	  source             TEXT NOT NULL,
	  source_id          TEXT NOT NULL,
	  chat_folder        TEXT NOT NULL,
	  chat_id            INTEGER NOT NULL,
	  tg_message_id      INTEGER NOT NULL DEFAULT 0,
	  is_owner           INTEGER NOT NULL DEFAULT 0,
	  is_voice           INTEGER NOT NULL DEFAULT 0,
	  text               TEXT NOT NULL,
	  file_path          TEXT,
	  status             TEXT NOT NULL,
	  attempts           INTEGER NOT NULL DEFAULT 0,
	  placeholder_msg_id INTEGER NOT NULL DEFAULT 0,
	  reply_msg_id       INTEGER NOT NULL DEFAULT 0,
	  error              TEXT,
	  created_at         INTEGER NOT NULL,
	  started_at         INTEGER,
	  finished_at        INTEGER,
	  UNIQUE(source, source_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_turns_status ON turns(status)`,
```

Append to `migrations`:

```go
	// Durable turns: link task run logs to the turn that ran them.
	`ALTER TABLE task_run_logs ADD COLUMN turn_id INTEGER`,
```

- [ ] **Step 4: Write `internal/store/turns.go`**

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Turn statuses. See the state machine in
// docs/superpowers/specs/2026-10-03-durable-turns-design.md §2.
const (
	TurnPending     = "pending"
	TurnRunning     = "running"
	TurnDone        = "done"
	TurnFailed      = "failed"
	TurnInterrupted = "interrupted"
)

// ErrTurnNotFound is returned when a turn id does not exist.
var ErrTurnNotFound = errors.New("store: turn not found")

// Turn is one row of the turns table: one agent run requested by a
// Telegram message or a scheduled task. Times are unix milliseconds.
type Turn struct {
	ID               int64
	Source           string // "telegram" | "task"
	SourceID         string // tg message id | "<task_id>:<next_run>"
	ChatFolder       string
	ChatID           int64 // Telegram chat id, 0 for tasks
	TGMessageID      int64
	IsOwner          bool
	IsVoice          bool
	Text             string
	FilePath         string
	Status           string
	Attempts         int
	PlaceholderMsgID int
	ReplyMsgID       int
	Error            string
	CreatedAt        int64
	StartedAt        int64
	FinishedAt       int64
}

const turnColumns = `id, source, source_id, chat_folder, chat_id, tg_message_id,
	is_owner, is_voice, text, file_path, status, attempts,
	placeholder_msg_id, reply_msg_id, error, created_at, started_at, finished_at`

// InsertTurn inserts t as a pending turn. The UNIQUE(source, source_id)
// constraint makes this idempotent: a duplicate returns (0, true, nil)
// and writes nothing.
func (s *Store) InsertTurn(ctx context.Context, t Turn) (int64, bool, error) {
	if t.Source == "" || t.SourceID == "" {
		return 0, false, errors.New("store: InsertTurn: empty Source/SourceID")
	}
	if t.ChatFolder == "" {
		return 0, false, errors.New("store: InsertTurn: empty ChatFolder")
	}
	if t.Status == "" {
		t.Status = TurnPending
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = time.Now().UnixMilli()
	}
	const stmt = `
		INSERT INTO turns
		  (source, source_id, chat_folder, chat_id, tg_message_id, is_owner, is_voice,
		   text, file_path, status, attempts, placeholder_msg_id, reply_msg_id,
		   error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source, source_id) DO NOTHING`
	res, err := s.db.ExecContext(ctx, stmt,
		t.Source, t.SourceID, t.ChatFolder, t.ChatID, t.TGMessageID,
		boolToInt(t.IsOwner), boolToInt(t.IsVoice),
		t.Text, nullableString(t.FilePath), t.Status, t.Attempts,
		t.PlaceholderMsgID, t.ReplyMsgID, nullableString(t.Error), t.CreatedAt)
	if err != nil {
		return 0, false, fmt.Errorf("store: InsertTurn: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("store: InsertTurn rows affected: %w", err)
	}
	if n == 0 {
		return 0, true, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("store: InsertTurn last id: %w", err)
	}
	return id, false, nil
}

// MarkTurnRunning sets status=running, increments attempts and stamps
// started_at. Called once per attempt.
func (s *Store) MarkTurnRunning(ctx context.Context, id int64) error {
	const stmt = `
		UPDATE turns
		   SET status = ?, attempts = attempts + 1, started_at = ?
		 WHERE id = ?`
	return s.execTurn(ctx, "MarkTurnRunning", stmt, TurnRunning, time.Now().UnixMilli(), id)
}

// MarkTurnFinished sets a terminal (or interrupted) status, the error
// text, and finished_at.
func (s *Store) MarkTurnFinished(ctx context.Context, id int64, status, errText string) error {
	const stmt = `
		UPDATE turns
		   SET status = ?, error = ?, finished_at = ?
		 WHERE id = ?`
	return s.execTurn(ctx, "MarkTurnFinished", stmt, status, nullableString(errText), time.Now().UnixMilli(), id)
}

// SetTurnPlaceholder records the Telegram message id of the streaming
// placeholder so a resumed turn can edit it instead of sending a new one.
func (s *Store) SetTurnPlaceholder(ctx context.Context, id int64, msgID int) error {
	return s.execTurn(ctx, "SetTurnPlaceholder",
		`UPDATE turns SET placeholder_msg_id = ? WHERE id = ?`, msgID, id)
}

// SetTurnReply records the Telegram message id of the final reply.
// First write wins: a resumed turn with a reply id does not run again.
func (s *Store) SetTurnReply(ctx context.Context, id int64, msgID int) error {
	return s.execTurn(ctx, "SetTurnReply",
		`UPDATE turns SET reply_msg_id = ? WHERE id = ?`, msgID, id)
}

func (s *Store) execTurn(ctx context.Context, op, stmt string, args ...any) error {
	res, err := s.db.ExecContext(ctx, stmt, args...)
	if err != nil {
		return fmt.Errorf("store: %s: %w", op, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTurnNotFound
	}
	return nil
}

// GetTurn returns one turn by id.
func (s *Store) GetTurn(ctx context.Context, id int64) (Turn, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE id = ?`, id)
	if err != nil {
		return Turn{}, fmt.Errorf("store: GetTurn: %w", err)
	}
	defer rows.Close()
	out, err := scanTurns(rows)
	if err != nil {
		return Turn{}, err
	}
	if len(out) == 0 {
		return Turn{}, ErrTurnNotFound
	}
	return out[0], nil
}

// ListUnfinishedTurns returns pending, running and interrupted turns in
// id order. This is what boot recovery replays.
func (s *Store) ListUnfinishedTurns(ctx context.Context) ([]Turn, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+turnColumns+` FROM turns WHERE status IN (?, ?, ?) ORDER BY id`,
		TurnPending, TurnRunning, TurnInterrupted)
	if err != nil {
		return nil, fmt.Errorf("store: ListUnfinishedTurns: %w", err)
	}
	defer rows.Close()
	return scanTurns(rows)
}

func scanTurns(rows *sql.Rows) ([]Turn, error) {
	var out []Turn
	for rows.Next() {
		var t Turn
		var filePath, errText sql.NullString
		var startedAt, finishedAt sql.NullInt64
		var isOwner, isVoice int
		if err := rows.Scan(&t.ID, &t.Source, &t.SourceID, &t.ChatFolder, &t.ChatID,
			&t.TGMessageID, &isOwner, &isVoice, &t.Text, &filePath, &t.Status,
			&t.Attempts, &t.PlaceholderMsgID, &t.ReplyMsgID, &errText,
			&t.CreatedAt, &startedAt, &finishedAt); err != nil {
			return nil, fmt.Errorf("store: scan turn: %w", err)
		}
		t.IsOwner = isOwner != 0
		t.IsVoice = isVoice != 0
		t.FilePath = filePath.String
		t.Error = errText.String
		t.StartedAt = startedAt.Int64
		t.FinishedAt = finishedAt.Int64
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate turns: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: all `Turn*` tests PASS; existing tests still PASS (schema re-apply is idempotent, migration ignores "duplicate column").

- [ ] **Step 6: Commit**

```bash
git add internal/store/schema.go internal/store/turns.go internal/store/turns_test.go
git commit -m "store: add turns table for durable agent turns

One row per requested agent run, UNIQUE(source, source_id) so Telegram
redelivery and double-fired scheduler slots collapse on insert. Also
adds task_run_logs.turn_id via the existing migrations list."
```

---

### Task 2: Task run log finalization

**Files:**
- Modify: `internal/store/tasks.go` (append after `LogTaskRun`)
- Modify: `internal/store/tasks_test.go` (append)

**Interfaces:**
- Produces:
  ```go
  func (s *Store) SetTaskRunTurn(ctx context.Context, taskID string, runAt, turnID int64) error
  func (s *Store) FinishTaskRun(ctx context.Context, taskID string, runAt int64, status string, durationMs int64, errText string) error
  func (s *Store) GetTaskRun(ctx context.Context, taskID string, runAt int64) (TaskRunLog, error)
  var ErrTaskRunNotFound = errors.New("store: task run not found")
  ```
  `TaskRunLog` gains `TurnID int64`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/store/tasks_test.go` (the file already opens a store; reuse its helper if one exists, otherwise use `openTestStore` from Task 1):

```go
func TestTaskRunLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.LogTaskRun(ctx, TaskRunLog{TaskID: "t1", RunAt: 1000, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskRunTurn(ctx, "t1", 1000, 55); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishTaskRun(ctx, "t1", 1000, "success", 1234, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTaskRun(ctx, "t1", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got.TurnID != 55 || got.Status != "success" || got.DurationMs != 1234 {
		t.Errorf("run = %+v", got)
	}
	if err := s.FinishTaskRun(ctx, "nope", 1, "error", 0, "x"); !errors.Is(err, ErrTaskRunNotFound) {
		t.Errorf("unknown run err = %v, want ErrTaskRunNotFound", err)
	}
}
```

Add `"errors"` to the test file imports if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestTaskRunLifecycle -v`
Expected: compile error `undefined: (*Store).SetTaskRunTurn`.

- [ ] **Step 3: Implement**

In `internal/store/tasks.go`, add `TurnID int64` to `TaskRunLog` (after `Error`), add the sentinel next to `ErrTaskNotFound`:

```go
// ErrTaskRunNotFound is returned when no task_run_logs row matches
// (task_id, run_at).
var ErrTaskRunNotFound = errors.New("store: task run not found")
```

Append after `LogTaskRun`:

```go
// SetTaskRunTurn links a task_run_logs row to the turn that executes it.
// Rows are addressed by (task_id, run_at), unique per scheduled slot.
func (s *Store) SetTaskRunTurn(ctx context.Context, taskID string, runAt, turnID int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE task_run_logs SET turn_id = ? WHERE task_id = ? AND run_at = ?`,
		turnID, taskID, runAt)
	if err != nil {
		return fmt.Errorf("store: SetTaskRunTurn: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTaskRunNotFound
	}
	return nil
}

// FinishTaskRun records the outcome of a task run that was logged as
// "running" when it was enqueued.
func (s *Store) FinishTaskRun(ctx context.Context, taskID string, runAt int64, status string, durationMs int64, errText string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE task_run_logs SET status = ?, duration_ms = ?, error = ?
		  WHERE task_id = ? AND run_at = ?`,
		status, durationMs, nullableString(errText), taskID, runAt)
	if err != nil {
		return fmt.Errorf("store: FinishTaskRun: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTaskRunNotFound
	}
	return nil
}

// GetTaskRun returns one task_run_logs row by (task_id, run_at).
func (s *Store) GetTaskRun(ctx context.Context, taskID string, runAt int64) (TaskRunLog, error) {
	var l TaskRunLog
	var dur, turnID sql.NullInt64
	var result, errText sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT task_id, run_at, duration_ms, status, result, error, turn_id
		   FROM task_run_logs WHERE task_id = ? AND run_at = ?`, taskID, runAt).
		Scan(&l.TaskID, &l.RunAt, &dur, &l.Status, &result, &errText, &turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskRunLog{}, ErrTaskRunNotFound
	}
	if err != nil {
		return TaskRunLog{}, fmt.Errorf("store: GetTaskRun: %w", err)
	}
	l.DurationMs = dur.Int64
	l.Result = result.String
	l.Error = errText.String
	l.TurnID = turnID.Int64
	return l, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/tasks.go internal/store/tasks_test.go
git commit -m "store: finalize task runs by (task_id, run_at)

The scheduler will log a run as running at enqueue time and finish it
when the queue reports the turn done, so the log needs an update path
and a turn_id link."
```

---

### Task 3: Queue persists turns (insert, mark, OnDone, ctx-derived workers, Close)

**Files:**
- Modify: `internal/queue/queue.go`
- Modify: `internal/queue/queue_test.go`

**Interfaces:**
- Produces:
  ```go
  type TurnStore interface {
      InsertTurn(ctx context.Context, it Item) (id int64, dup bool, err error)
      MarkRunning(ctx context.Context, id int64) error
      MarkFinished(ctx context.Context, id int64, status, errText string) error
      LoadUnfinished(ctx context.Context) ([]Item, error)
  }
  const (
      StatusDone        = "done"
      StatusFailed      = "failed"
      StatusInterrupted = "interrupted"
  )
  const MaxAttempts = 3
  type Item struct {
      ID               int64
      Source           string
      SourceID         string
      Resumed          bool
      Attempts         int
      PlaceholderMsgID int
      ReplyMsgID       int
      ChatID    int64
      MessageID int
      Folder    string
      IsOwner   bool
      Text      string
      FilePath  string
      IsVoice   bool
  }
  func New(ctx context.Context, handler Handler, maxConcurrent int, store TurnStore, log *slog.Logger) *Queue
  func (q *Queue) Enqueue(ctx context.Context, item Item) (id int64, dup bool)
  func (q *Queue) OnDone(fn func(Item, error))
  func (q *Queue) Close()
  ```
- Note: `Recover` is Task 5. `LoadUnfinished` is declared here so the interface is final.

- [ ] **Step 1: Update existing tests for the new `New` signature**

In `internal/queue/queue_test.go`, replace every `New(handler, N, silentLog())` with `New(context.Background(), handler, N, nil, silentLog())`. Add a fake store at the bottom of the file:

```go
// fakeStore records TurnStore calls in order so tests can assert
// ordering relative to handler invocations.
type fakeStore struct {
	mu         sync.Mutex
	nextID     int64
	calls      []string // "insert:<sid>", "running:<id>", "finished:<id>:<status>"
	dupIDs     map[string]bool
	insertErr  error
	unfinished []Item
}

func newFakeStore() *fakeStore { return &fakeStore{dupIDs: map[string]bool{}} }

func (f *fakeStore) InsertTurn(_ context.Context, it Item) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return 0, false, f.insertErr
	}
	key := it.Source + ":" + it.SourceID
	if f.dupIDs[key] {
		return 0, true, nil
	}
	f.dupIDs[key] = true
	f.nextID++
	f.calls = append(f.calls, "insert:"+it.SourceID)
	return f.nextID, false, nil
}

func (f *fakeStore) MarkRunning(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("running:%d", id))
	return nil
}

func (f *fakeStore) MarkFinished(_ context.Context, id int64, status, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("finished:%d:%s", id, status))
	return nil
}

func (f *fakeStore) LoadUnfinished(_ context.Context) ([]Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Item(nil), f.unfinished...), nil
}

func (f *fakeStore) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
```

Add `"sync"` to the imports.

- [ ] **Step 2: Write the failing tests**

Append to `internal/queue/queue_test.go`:

```go
func TestEnqueuePersistsBeforeRunAndFinishesAfter(t *testing.T) {
	fs := newFakeStore()
	done := make(chan struct{})
	handler := func(ctx context.Context, items []Item) error {
		fs.mu.Lock()
		fs.calls = append(fs.calls, "handler")
		fs.mu.Unlock()
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	q.OnDone(func(it Item, err error) {
		if it.ID == 1 {
			close(done)
		}
	})
	id, dup := q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "1", Text: "x"})
	if id != 1 || dup {
		t.Fatalf("Enqueue = (%d, %v), want (1, false)", id, dup)
	}
	<-done
	want := []string{"insert:1", "running:1", "handler", "finished:1:done"}
	if got := fs.snapshot(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

func TestEnqueueDuplicateNeverReachesHandler(t *testing.T) {
	fs := newFakeStore()
	var calls int32
	handler := func(ctx context.Context, items []Item) error {
		atomic.AddInt32(&calls, int32(len(items)))
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	it := Item{Folder: "a", Source: "telegram", SourceID: "7", Text: "x"}
	q.Enqueue(context.Background(), it)
	_, dup := q.Enqueue(context.Background(), it)
	if !dup {
		t.Fatal("second Enqueue not reported as dup")
	}
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("handler saw %d items, want 1", n)
	}
}

func TestEnqueueStoreErrorDrops(t *testing.T) {
	fs := newFakeStore()
	fs.insertErr = errors.New("disk full")
	var calls int32
	handler := func(ctx context.Context, items []Item) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	id, dup := q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "1"})
	if id != 0 || dup {
		t.Errorf("Enqueue = (%d, %v), want (0, false)", id, dup)
	}
	time.Sleep(30 * time.Millisecond)
	if atomic.LoadInt32(&calls) != 0 {
		t.Error("handler ran despite insert failure")
	}
}

func TestCoalescedBatchMarksEveryItem(t *testing.T) {
	fs := newFakeStore()
	release := make(chan struct{})
	var batches int32
	handler := func(ctx context.Context, items []Item) error {
		if atomic.AddInt32(&batches, 1) == 1 {
			<-release // hold the first turn so the next two coalesce
		}
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	finished := make(chan int64, 3)
	q.OnDone(func(it Item, err error) { finished <- it.ID })
	for i := 1; i <= 3; i++ {
		q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: fmt.Sprint(i)})
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	got := map[int64]bool{}
	for i := 0; i < 3; i++ {
		select {
		case id := <-finished:
			got[id] = true
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for OnDone")
		}
	}
	if len(got) != 3 {
		t.Errorf("OnDone ids = %v, want 3 distinct", got)
	}
	calls := fs.snapshot()
	for _, id := range []string{"finished:2:done", "finished:3:done"} {
		if !contains(calls, id) {
			t.Errorf("missing %s in %v", id, calls)
		}
	}
}

func TestHandlerErrorMarksFailedAfterRetries(t *testing.T) {
	fs := newFakeStore()
	handler := func(ctx context.Context, items []Item) error { return errors.New("infra") }
	q := New(context.Background(), handler, 1, fs, silentLog())
	q.BackoffFunc = func(int) time.Duration { return time.Millisecond }
	done := make(chan error, 1)
	q.OnDone(func(it Item, err error) { done <- err })
	q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "1"})
	select {
	case err := <-done:
		if err == nil {
			t.Error("OnDone err = nil, want handler error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	calls := fs.snapshot()
	if !contains(calls, "finished:1:failed") {
		t.Errorf("calls = %v, want finished:1:failed", calls)
	}
	if n := count(calls, "running:1"); n != DefaultMaxRetries+1 {
		t.Errorf("running marks = %d, want %d (attempts persisted per try)", n, DefaultMaxRetries+1)
	}
}

func TestCloseInterruptsInFlightTurn(t *testing.T) {
	fs := newFakeStore()
	started := make(chan struct{})
	handler := func(ctx context.Context, items []Item) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := New(ctx, handler, 1, fs, silentLog())
	q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "1"})
	<-started
	cancel()
	q.Close()
	if calls := fs.snapshot(); !contains(calls, "finished:1:interrupted") {
		t.Errorf("calls = %v, want finished:1:interrupted", calls)
	}
}

func TestNilStoreKeepsMemoryOnlyBehaviour(t *testing.T) {
	done := make(chan struct{})
	handler := func(ctx context.Context, items []Item) error { close(done); return nil }
	q := New(context.Background(), handler, 1, nil, silentLog())
	id, dup := q.Enqueue(context.Background(), Item{Folder: "a", Text: "x"})
	if id != 0 || dup {
		t.Errorf("Enqueue with nil store = (%d, %v), want (0, false)", id, dup)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler not called")
	}
}

func contains(ss []string, s string) bool { return count(ss, s) > 0 }

func count(ss []string, s string) int {
	n := 0
	for _, x := range ss {
		if x == s {
			n++
		}
	}
	return n
}
```

Add `"errors"` to the imports.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/queue/ 2>&1 | head`
Expected: compile errors (`New` arity, `Enqueue` returns, `OnDone`, `Close` undefined).

- [ ] **Step 4: Implement in `internal/queue/queue.go`**

Replace the `Item` struct, `Queue` struct, `New`, `Enqueue`, and `worker` with the following; keep `Pending`, `ActiveChats`, `drainPending`, `deactivate`, `dropPendingAndDeactivate`, `incrementRetries`, `resetRetries`, `backoffDuration` as they are. Update the package doc comment's bullet list with one more bullet: "**Durability.** With a TurnStore, every item is a `turns` row before it is dispatched and is marked running/done/failed/interrupted around the handler call. Nil store = memory only."

```go
// Item is one enqueued message waiting for an agent turn.
type Item struct {
	// Durability fields. Zero when the queue has no TurnStore.
	ID               int64  // turns.id
	Source           string // "telegram" | "task"
	SourceID         string // tg message id | "<task_id>:<next_run>"
	Resumed          bool   // set by Recover: this turn was running/interrupted when gopod died
	Attempts         int    // from the row, for the crash-loop cap
	PlaceholderMsgID int    // streaming placeholder recorded by a previous attempt
	ReplyMsgID       int    // final reply recorded by a previous attempt

	ChatID    int64  // Telegram chat ID (for the reply callback), 0 for tasks
	MessageID int    // Telegram message ID (for reply threading + reactions)
	Folder    string // registered chat folder
	IsOwner   bool
	Text      string // the user's message text
	FilePath  string // container-side path to an uploaded file (photo/doc), empty if text-only
	IsVoice   bool   // true if the original message was a voice message
}

// Turn statuses written through TurnStore. Mirror store.Turn* so the
// queue does not import store.
const (
	StatusDone        = "done"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
)

// MaxAttempts is how many times a turn may be started (across process
// restarts) before Recover declares it a crash loop.
const MaxAttempts = 3

// TurnStore persists turns. Declared here, at the consumer, so the
// store package stays free of queue imports; cmd/gopod adapts
// *store.Store to it.
type TurnStore interface {
	InsertTurn(ctx context.Context, it Item) (id int64, dup bool, err error)
	MarkRunning(ctx context.Context, id int64) error
	MarkFinished(ctx context.Context, id int64, status, errText string) error
	LoadUnfinished(ctx context.Context) ([]Item, error)
}

// Queue is gopod's agent-run scheduler.
type Queue struct {
	handler    Handler
	maxConc    int
	sem        chan struct{} // buffered channel, size = maxConc
	log        *slog.Logger
	maxRetries int
	store      TurnStore // nil = memory only

	// BackoffFunc computes the wait duration for a given retry count.
	// Defaults to the exponential backoffDuration. Tests override this
	// to avoid real sleeps.
	BackoffFunc func(retry int) time.Duration

	ctx    context.Context // workers derive from this; cancelled by Close
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	chats  map[string]*chatState // keyed by folder
	onDone []func(Item, error)
}

// New creates a Queue. Workers derive their context from ctx (the app
// context), so cancelling it interrupts in-flight turns; see Close.
// maxConcurrent ≤ 0 defaults to DefaultMaxConcurrent. handler must not
// be nil. store may be nil (memory-only, dev mode).
func New(ctx context.Context, handler Handler, maxConcurrent int, store TurnStore, log *slog.Logger) *Queue {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	if log == nil {
		log = slog.Default()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	wctx, cancel := context.WithCancel(ctx)
	return &Queue{
		handler:     handler,
		maxConc:     maxConcurrent,
		sem:         make(chan struct{}, maxConcurrent),
		log:         log,
		maxRetries:  DefaultMaxRetries,
		store:       store,
		BackoffFunc: backoffDuration,
		ctx:         wctx,
		cancel:      cancel,
		chats:       make(map[string]*chatState),
	}
}

// OnDone registers a hook called after a turn reaches a terminal
// status (done, failed, interrupted) or is rejected by Recover as a
// crash loop. Hooks run synchronously in the worker goroutine, in
// registration order. err is the last handler error, nil on success.
func (q *Queue) OnDone(fn func(Item, error)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onDone = append(q.onDone, fn)
}

// Close cancels every worker's context and waits for them to exit.
// In-flight turns are marked interrupted. Safe to call once.
func (q *Queue) Close() {
	q.cancel()
	q.wg.Wait()
}

// Enqueue persists the item (when a TurnStore is configured) and
// schedules it for the chat. Returns the turn id and whether the item
// was a duplicate of an existing turn (same Source+SourceID), in which
// case nothing was scheduled. Without a store both are zero.
//
// If the chat has no active worker, one is started (subject to the
// global concurrency cap). If the chat already has an active worker,
// the item is appended to the pending queue.
//
// Enqueue never blocks on the global cap. The worker runs on the
// queue's own context (derived from the ctx passed to New), NOT the
// caller's ctx, which for Telegram is request-scoped and cancelled
// when the handler returns.
func (q *Queue) Enqueue(ctx context.Context, item Item) (int64, bool) {
	if q.store != nil {
		id, dup, err := q.store.InsertTurn(ctx, item)
		if err != nil {
			q.log.Error("queue: persist turn failed, dropping",
				slog.String("folder", item.Folder),
				slog.String("source", item.Source),
				slog.String("source_id", item.SourceID),
				slog.Any("err", err))
			return 0, false
		}
		if dup {
			q.log.Debug("queue: duplicate turn ignored",
				slog.String("source", item.Source),
				slog.String("source_id", item.SourceID))
			return 0, true
		}
		item.ID = id
	}
	q.schedule(item)
	return item.ID, false
}

// schedule appends item to its chat's pending list and starts a worker
// if none is active. Shared by Enqueue and Recover.
func (q *Queue) schedule(item Item) {
	q.mu.Lock()
	cs, ok := q.chats[item.Folder]
	if !ok {
		cs = &chatState{}
		q.chats[item.Folder] = cs
	}
	cs.pending = append(cs.pending, item)
	if cs.active {
		q.mu.Unlock()
		return // worker already running, it will drain pending
	}
	cs.active = true
	q.mu.Unlock()

	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		q.worker(q.ctx, item.Folder)
	}()
}

func (q *Queue) worker(ctx context.Context, folder string) {
	// Acquire semaphore slot (blocks if cap reached).
	select {
	case q.sem <- struct{}{}:
	case <-ctx.Done():
		q.deactivate(folder)
		return
	}
	defer func() { <-q.sem }()

	for {
		items := q.drainPending(folder)
		if len(items) == 0 {
			q.deactivate(folder)
			return
		}

		q.log.Debug("queue: running agent turn",
			slog.String("folder", folder),
			slog.Int("items", len(items)))

		// Inner retry loop: retries the SAME items on failure.
		// New messages arriving during backoff append to pending
		// and get picked up in the NEXT outer-loop iteration.
		for {
			q.markRunning(ctx, items)
			err := q.handler(ctx, items)
			if err == nil {
				q.resetRetries(folder)
				q.finish(items, StatusDone, nil)
				break // success → drain more pending in outer loop
			}
			if ctx.Err() != nil {
				// Shutdown, not a failure: leave the rows for Recover.
				q.log.Info("queue: turn interrupted by shutdown",
					slog.String("folder", folder))
				q.finish(items, StatusInterrupted, err)
				q.deactivate(folder)
				return
			}

			retries := q.incrementRetries(folder)
			if retries > q.maxRetries {
				q.log.Error("queue: max retries exceeded, dropping items",
					slog.String("folder", folder),
					slog.Int("retries", retries),
					slog.Any("err", err))
				q.finish(items, StatusFailed, err)
				q.dropPendingAndDeactivate(folder)
				return
			}
			backoff := q.BackoffFunc(retries)
			q.log.Warn("queue: agent run failed, backing off",
				slog.String("folder", folder),
				slog.Int("retry", retries),
				slog.Duration("backoff", backoff),
				slog.Any("err", err))
			select {
			case <-time.After(backoff):
				// retry same items
			case <-ctx.Done():
				q.finish(items, StatusInterrupted, ctx.Err())
				q.deactivate(folder)
				return
			}
		}
	}
}

// markRunning flips every item in the batch to running. Store errors
// are logged, not fatal: the turn still runs, and Recover may replay it
// once more than necessary, which the reply memo tolerates.
func (q *Queue) markRunning(ctx context.Context, items []Item) {
	if q.store == nil {
		return
	}
	for _, it := range items {
		if it.ID == 0 {
			continue
		}
		if err := q.store.MarkRunning(context.WithoutCancel(ctx), it.ID); err != nil {
			q.log.Error("queue: mark running", slog.Int64("turn", it.ID), slog.Any("err", err))
		}
	}
}

// finish marks every item in the batch with status and fires the
// OnDone hooks. Uses a context detached from cancellation so shutdown
// bookkeeping still reaches the store.
func (q *Queue) finish(items []Item, status string, err error) {
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	if q.store != nil {
		for _, it := range items {
			if it.ID == 0 {
				continue
			}
			if merr := q.store.MarkFinished(context.WithoutCancel(q.ctx), it.ID, status, errText); merr != nil {
				q.log.Error("queue: mark finished", slog.Int64("turn", it.ID), slog.Any("err", merr))
			}
		}
	}
	q.mu.Lock()
	hooks := append([]func(Item, error)(nil), q.onDone...)
	q.mu.Unlock()
	for _, it := range items {
		for _, h := range hooks {
			h(it, err)
		}
	}
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/queue/ -race -v`
Expected: all PASS, including the pre-existing serialization/cap/coalescing/backoff tests.

- [ ] **Step 6: Fix the other callers so the tree builds**

`cmd/gopod/main.go:439` → `queue.New(ctx, tgBot.NewAgentHandler(), queue.DefaultMaxConcurrent, nil, queueLog)` (the real adapter arrives in Task 6). Any other `queue.New(` call found by `grep -rn 'queue.New(' --include=*.go .` gets the same treatment (scheduler tests construct none today).

Run: `go build ./... && go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 7: Commit**

```bash
git add internal/queue/queue.go internal/queue/queue_test.go cmd/gopod/main.go
git commit -m "queue: persist turns through a TurnStore and report completion

Enqueue inserts the turn row before dispatch and returns (id, dup) so
producers can key on it; workers mark running/done/failed around the
handler and interrupted on shutdown. Workers now derive from the app
context instead of context.Background(), and Close waits for them.
OnDone hooks let the scheduler finalize its run log on the real end of
a turn."
```

---

### Task 4: Queue recovery at boot

**Files:**
- Modify: `internal/queue/queue.go` (append)
- Modify: `internal/queue/queue_test.go` (append)

**Interfaces:**
- Produces: `func (q *Queue) Recover(ctx context.Context) (int, error)` — returns the number of turns re-enqueued.

- [ ] **Step 1: Write the failing tests**

```go
func TestRecoverReenqueuesUnfinishedAsResumed(t *testing.T) {
	fs := newFakeStore()
	fs.unfinished = []Item{
		{ID: 1, Folder: "a", Source: "telegram", SourceID: "1", Attempts: 1, Text: "running one"},
		{ID: 2, Folder: "b", Source: "telegram", SourceID: "2", Attempts: 0, Text: "pending one"},
	}
	got := make(chan Item, 2)
	handler := func(ctx context.Context, items []Item) error {
		for _, it := range items {
			got <- it
		}
		return nil
	}
	q := New(context.Background(), handler, 2, fs, silentLog())
	n, err := q.Recover(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("Recover = (%d, %v), want (2, nil)", n, err)
	}
	seen := map[int64]Item{}
	for i := 0; i < 2; i++ {
		select {
		case it := <-got:
			seen[it.ID] = it
		case <-time.After(2 * time.Second):
			t.Fatal("timeout")
		}
	}
	if !seen[1].Resumed {
		t.Error("turn 1 (was running) not marked Resumed")
	}
	if seen[2].Resumed {
		t.Error("turn 2 (was pending) wrongly marked Resumed")
	}
	if contains(fs.snapshot(), "insert:1") || contains(fs.snapshot(), "insert:2") {
		t.Error("Recover must not re-insert rows")
	}
}

func TestRecoverPreservesOrderPerChat(t *testing.T) {
	fs := newFakeStore()
	fs.unfinished = []Item{
		{ID: 1, Folder: "a", Source: "telegram", SourceID: "1", Attempts: 1},
		{ID: 2, Folder: "a", Source: "telegram", SourceID: "2"},
	}
	var order []int64
	var mu sync.Mutex
	done := make(chan struct{}, 2)
	handler := func(ctx context.Context, items []Item) error {
		mu.Lock()
		for _, it := range items {
			order = append(order, it.ID)
		}
		mu.Unlock()
		done <- struct{}{}
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	q.Recover(context.Background())
	<-done
	// Second item may have coalesced into the first batch or run alone.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 || order[0] != 1 || order[1] != 2 {
		t.Errorf("order = %v, want [1 2 ...]", order)
	}
}

func TestRecoverCrashLoopIsFailedWithoutRunning(t *testing.T) {
	fs := newFakeStore()
	fs.unfinished = []Item{
		{ID: 9, Folder: "a", Source: "telegram", SourceID: "9", Attempts: MaxAttempts},
	}
	handler := func(ctx context.Context, items []Item) error {
		t.Error("handler must not run for a crash-looping turn")
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	var hookErr error
	q.OnDone(func(it Item, err error) { hookErr = err })
	n, _ := q.Recover(context.Background())
	if n != 0 {
		t.Errorf("Recover re-enqueued %d, want 0", n)
	}
	if !contains(fs.snapshot(), "finished:9:failed") {
		t.Errorf("calls = %v, want finished:9:failed", fs.snapshot())
	}
	if hookErr == nil || !errors.Is(hookErr, ErrCrashLoop) {
		t.Errorf("hook err = %v, want ErrCrashLoop", hookErr)
	}
}

func TestRecoverWithoutStoreIsNoop(t *testing.T) {
	q := New(context.Background(), func(context.Context, []Item) error { return nil }, 1, nil, silentLog())
	if n, err := q.Recover(context.Background()); n != 0 || err != nil {
		t.Errorf("Recover = (%d, %v), want (0, nil)", n, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/queue/ -run Recover -v`
Expected: compile error `undefined: (*Queue).Recover`, `ErrCrashLoop`.

- [ ] **Step 3: Implement**

Append to `internal/queue/queue.go`:

```go
// ErrCrashLoop is passed to OnDone hooks for a turn that Recover
// refused to replay because it already reached MaxAttempts.
var ErrCrashLoop = errors.New("queue: turn exceeded max attempts across restarts")

// Recover loads every unfinished turn from the store and schedules it.
// Rows that were running or interrupted when the previous process died
// are re-enqueued with Resumed=true so the handler can tell the agent
// its last turn was cut off. Rows that already reached MaxAttempts are
// marked failed and reported through OnDone instead of being replayed.
//
// Call once at boot, after producers and hooks are wired and before
// Telegram starts polling. Returns the number of turns scheduled.
func (q *Queue) Recover(ctx context.Context) (int, error) {
	if q.store == nil {
		return 0, nil
	}
	items, err := q.store.LoadUnfinished(ctx)
	if err != nil {
		return 0, fmt.Errorf("queue: load unfinished turns: %w", err)
	}
	n := 0
	for _, it := range items {
		if it.Attempts >= MaxAttempts {
			q.log.Error("queue: turn exceeded max attempts, giving up",
				slog.Int64("turn", it.ID),
				slog.String("folder", it.Folder),
				slog.Int("attempts", it.Attempts))
			q.finish([]Item{it}, StatusFailed, ErrCrashLoop)
			continue
		}
		it.Resumed = it.Attempts > 0 // running/interrupted rows were started at least once
		q.log.Info("queue: recovering turn",
			slog.Int64("turn", it.ID),
			slog.String("folder", it.Folder),
			slog.Bool("resumed", it.Resumed))
		q.schedule(it)
		n++
	}
	return n, nil
}
```

Add `"errors"` and `"fmt"` to the imports.

Note on `Resumed`: a `pending` row has `attempts = 0`; `running` and `interrupted` rows have `attempts ≥ 1` because `MarkRunning` increments before the handler. The fake store in the tests sets `Attempts` accordingly; the real adapter (Task 6) copies `Attempts` from the row.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/queue/ -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/queue/queue.go internal/queue/queue_test.go
git commit -m "queue: recover unfinished turns at boot

Rows left pending, running or interrupted by the previous process are
scheduled again; those that were already started come back with
Resumed=true. A row that reached MaxAttempts is failed and reported
through OnDone instead of being replayed forever."
```

---

### Task 5: Telegram resume helpers (pure)

**Files:**
- Create: `internal/telegram/resume.go`
- Create: `internal/telegram/resume_test.go`

**Interfaces:**
- Produces:
  ```go
  func resumePrompt(userText string) string
  const resumingBanner = "⟳ resuming after restart"
  const crashLoopNotice = "Gave up on this message after 3 restarts. Please resend."
  func (b *Bot) NewDoneHook() func(queue.Item, error)
  func (b *Bot) recordPlaceholder(ctx context.Context, item queue.Item, msgID int)
  func (b *Bot) recordReply(ctx context.Context, item queue.Item, msgID int, text string)
  func (b *Bot) reusePlaceholder(ctx context.Context, item queue.Item) int
  ```

- [ ] **Step 1: Write the failing tests**

`internal/telegram/resume_test.go`:

```go
package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/queue"
)

func TestResumePromptWrapsUserText(t *testing.T) {
	got := resumePrompt("deploy the thing")
	for _, want := range []string{"[gopod]", "interrupted by a restart", "deploy the thing", "Continue where you left off"} {
		if !strings.Contains(got, want) {
			t.Errorf("resumePrompt missing %q in:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "[gopod]") {
		t.Error("resumePrompt must start with the [gopod] marker so the agent can tell it from user text")
	}
}

func TestAgentHandlerSkipsResumedWithReply(t *testing.T) {
	// A Bot with nil runner panics if the handler tries to run the
	// agent; a resumed item that already has a reply must return early.
	b := &Bot{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := b.NewAgentHandler()
	err := h(context.Background(), []queue.Item{{ID: 1, Resumed: true, ReplyMsgID: 77, Folder: "owner"}})
	if err != nil {
		t.Errorf("handler err = %v, want nil", err)
	}
}

func TestDoneHookIgnoresNonCrashLoop(t *testing.T) {
	// Without a Telegram API the hook must not touch the network for
	// ordinary completions; only ErrCrashLoop triggers a notice, and
	// that path needs b.api, so assert the early return by using a nil
	// api and a non-crash-loop error.
	b := &Bot{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hook := b.NewDoneHook()
	hook(queue.Item{ID: 1, ChatID: 5, MessageID: 6}, nil)
	hook(queue.Item{ID: 2, ChatID: 5, MessageID: 6}, errors.New("infra"))
	// Reaching here without a nil-pointer panic is the assertion.
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/telegram/ -run 'Resume|DoneHook|SkipsResumed' -v`
Expected: compile error `undefined: resumePrompt`, `NewDoneHook`.

- [ ] **Step 3: Implement `internal/telegram/resume.go`**

```go
package telegram

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
)

// resumingBanner replaces a stale streaming placeholder while a
// resumed turn runs.
const resumingBanner = "⟳ resuming after restart"

// crashLoopNotice is sent when Recover gives up on a turn.
const crashLoopNotice = "Gave up on this message after 3 restarts. Please resend."

// resumePrompt wraps the user's original text in an interruption notice.
// The session continues (`claude --continue` / `codex resume --last`),
// so the agent sees its own partial transcript and decides what is
// safe to redo. gopod does not classify tool side effects.
func resumePrompt(userText string) string {
	return "[gopod] The previous turn was interrupted by a restart before a reply was sent. " +
		"The user's message was:\n\n" + userText +
		"\n\nContinue where you left off, or reply."
}

// NewDoneHook returns a queue.OnDone hook that reacts ❌ and explains
// when a turn was abandoned as a crash loop. Every other completion is
// already handled by the agent handler itself.
func (b *Bot) NewDoneHook() func(queue.Item, error) {
	return func(it queue.Item, err error) {
		if !errors.Is(err, queue.ErrCrashLoop) || it.ChatID == 0 || b.api == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if it.MessageID > 0 {
			b.react(ctx, it.ChatID, it.MessageID, emojiError)
		}
		b.replyTo(ctx, it.ChatID, it.MessageID, crashLoopNotice)
	}
}

// recordPlaceholder memoizes the streaming placeholder id on the turn
// row. Best effort: failure is logged, the turn goes on.
func (b *Bot) recordPlaceholder(ctx context.Context, item queue.Item, msgID int) {
	if item.ID == 0 || msgID == 0 || b.store == nil {
		return
	}
	if err := b.store.SetTurnPlaceholder(ctx, item.ID, msgID); err != nil {
		b.log.Warn("record placeholder", slog.Int64("turn", item.ID), slog.Any("err", err))
	}
}

// recordReply memoizes the final reply id on the turn row and persists
// the outbound text to messages so the SQLite transcript has both
// sides. Best effort: the user already has the reply.
func (b *Bot) recordReply(ctx context.Context, item queue.Item, msgID int, text string) {
	if msgID == 0 || b.store == nil {
		return
	}
	if item.ID != 0 {
		if err := b.store.SetTurnReply(ctx, item.ID, msgID); err != nil {
			b.log.Warn("record reply", slog.Int64("turn", item.ID), slog.Any("err", err))
		}
	}
	if item.ChatID == 0 {
		return
	}
	_, err := b.store.SaveMessage(ctx, store.Message{
		ChatJID:            buildChatJID(item.ChatID),
		TGMessageID:        int64(msgID),
		Sender:             "bot",
		SenderName:         "gopod",
		Content:            text,
		Timestamp:          time.Now().UnixMilli(),
		IsFromMe:           true,
		IsBotMessage:       true,
		ReplyToTGMessageID: int64(item.MessageID),
	})
	if err != nil {
		b.log.Warn("persist outbound", slog.Int64("chat_id", item.ChatID), slog.Any("err", err))
	}
}

// reusePlaceholder edits the placeholder left by the interrupted
// attempt to the resuming banner and returns its id, or 0 when there
// is none or the edit fails (deleted message), in which case the
// caller sends a fresh placeholder.
func (b *Bot) reusePlaceholder(ctx context.Context, item queue.Item) int {
	if !item.Resumed || item.PlaceholderMsgID == 0 || b.api == nil {
		return 0
	}
	_, err := b.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    item.ChatID,
		MessageID: item.PlaceholderMsgID,
		Text:      resumingBanner,
	})
	if err != nil {
		b.log.Debug("reuse placeholder failed, sending a new one",
			slog.Int64("chat_id", item.ChatID),
			slog.Int("msg_id", item.PlaceholderMsgID),
			slog.Any("err", err))
		return 0
	}
	return item.PlaceholderMsgID
}

// ensure models is used even if a future refactor drops the edit call.
var _ = models.ParseModeHTML
```

Remove the trailing `var _ = models.ParseModeHTML` line if `models` is otherwise unused; `go vet` will say.

- [ ] **Step 4: Make `NewAgentHandler` short-circuit resumed items with a reply**

In `internal/telegram/handler.go`, `NewAgentHandler`, right after `item := items[len(items)-1]`:

```go
		if item.Resumed && item.ReplyMsgID != 0 {
			// The crash hit between sending the reply and marking the
			// turn done. The user already has the answer.
			b.log.Info("resumed turn already replied, skipping",
				slog.Int64("turn", item.ID),
				slog.Int("reply_msg_id", item.ReplyMsgID))
			return nil
		}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/telegram/ -v -run 'Resume|DoneHook|SkipsResumed'`
Expected: PASS. Then `go vet ./internal/telegram/`.

- [ ] **Step 6: Commit**

```bash
git add internal/telegram/resume.go internal/telegram/resume_test.go internal/telegram/handler.go
git commit -m "telegram: resume helpers for durable turns

resumePrompt tells the agent its last turn was cut off; recordReply and
recordPlaceholder write the first-write-wins memo and persist outbound
text; reusePlaceholder edits the stale ▍ instead of sending a new one;
NewDoneHook explains a crash loop to the user."
```

---

### Task 6: Wire resume, memo and outbound into the Telegram paths

**Files:**
- Modify: `internal/telegram/handler.go` (`defaultHandler` enqueue, `runAgentSync`, `replyTo`, `sendFormatted`)
- Modify: `internal/telegram/streaming.go` (`runAgentStreaming`, `drainAndSend`)

**Interfaces:**
- Consumes: Task 5 helpers; `queue.Item` fields from Task 3.
- Produces: `func (b *Bot) replyTo(ctx, chatID int64, replyToMsgID int, text string) int` and `func (b *Bot) sendFormatted(...) int` — both now return the id of the **last** message sent (0 on failure). `replyText` keeps returning nothing.

- [ ] **Step 1: Make sends return the message id**

In `handler.go`, change `replyTo` to `return b.sendFormatted(ctx, chatID, replyToMsgID, text)` with return type `int`, and `replyText` to `_ = b.sendFormatted(ctx, chatID, 0, text)`. Change `sendFormatted` signature to return `int`; track `last := 0`; after each successful `b.api.SendMessage` in both the HTML loop and the plain-text fallback loop set `last = sent.ID` (capture the first return value instead of discarding it); every early `return` becomes `return last`; the function ends with `return last`.

Build: `go build ./internal/telegram/` — fix every call site that `go vet` flags for unused results (none should; Go allows ignoring return values).

- [ ] **Step 2: Enqueue with Source/SourceID**

In `defaultHandler`, where `item := queue.Item{...}` is built, add:

```go
		Source:   "telegram",
		SourceID: strconv.Itoa(m.ID),
```

`strconv` is already imported.

- [ ] **Step 3: Sync path**

In `runAgentSync`:

```go
	prompt := item.Text
	if item.Resumed {
		prompt = resumePrompt(item.Text)
	}
	reply, err := b.runner.Run(ctx, item.Folder, tier, b.allowlist, prompt)
```

At the end, replace the final block:

```go
	mode := b.getReplyMode(item.ChatID, item.IsVoice)
	if mode != "voice" {
		sent := b.replyTo(ctx, item.ChatID, item.MessageID, reply)
		b.recordReply(ctx, item, sent, reply)
	}
```

- [ ] **Step 4: Streaming path**

In `runAgentStreaming`:

```go
	prompt := item.Text
	if item.Resumed {
		prompt = resumePrompt(item.Text)
	}
	stream, err := b.runner.RunStream(ctx, item.Folder, tier, b.allowlist, prompt)
```

Replace the placeholder block:

```go
	// Reuse the placeholder left by an interrupted attempt, else send one.
	placeholder := b.reusePlaceholder(ctx, item)
	if placeholder == 0 {
		placeholder = b.sendPlaceholder(ctx, item.ChatID, item.MessageID)
	}
	if placeholder == 0 {
		// Fallback: drain stream, send as buffered.
		b.drainAndSend(ctx, item, stream)
		return
	}
	b.recordPlaceholder(ctx, item, placeholder)
```

In the overflow branch inside the loop, after `placeholder = b.sendPlaceholder(ctx, item.ChatID, 0)` and its zero check, add `b.recordPlaceholder(ctx, item, placeholder)`.

In the `done:` section:

```go
	if !overflow {
		html := markdownToTelegramHTML(finalText)
		b.editMessageHTML(ctx, item.ChatID, placeholder, html)
		b.recordReply(ctx, item, placeholder, finalText)
	} else {
		if accumulated.Len() > 0 {
			b.editMessage(ctx, item.ChatID, placeholder, accumulated.String())
		}
		sent := b.replyTo(ctx, item.ChatID, item.MessageID, finalText)
		b.recordReply(ctx, item, sent, finalText)
	}
```

In `drainAndSend`, replace `b.replyTo(ctx, item.ChatID, item.MessageID, text)` with:

```go
	sent := b.replyTo(ctx, item.ChatID, item.MessageID, text)
	b.recordReply(ctx, item, sent, text)
```

- [ ] **Step 5: Build, vet, test**

Run: `go build ./... && go vet ./... && go test ./internal/telegram/ ./internal/queue/`
Expected: green. Existing `handler_test.go`/`format_test.go` unaffected.

- [ ] **Step 6: Commit**

```bash
git add internal/telegram/handler.go internal/telegram/streaming.go
git commit -m "telegram: run resumed turns with an interruption notice and memoize replies

Sync and streaming paths wrap a Resumed item's text in resumePrompt,
reuse the previous placeholder when it still exists, record placeholder
and reply ids on the turn row, and persist the outbound text to
messages. Enqueue now carries Source/SourceID so the queue can
deduplicate Telegram redeliveries."
```

---

### Task 7: Scheduler checkpoint through the queue

**Files:**
- Modify: `internal/scheduler/scheduler.go` (`New`, `runTask`)
- Modify: `internal/scheduler/scheduler_test.go` (append)

**Interfaces:**
- Consumes: `queue.Enqueue` returning `(id, dup)`, `queue.OnDone`, `store.LogTaskRun`, `store.SetTaskRunTurn`, `store.FinishTaskRun`, `store.GetTurn`.
- Produces: `func taskSourceID(taskID string, runAt int64) string` and `func parseTaskSourceID(s string) (taskID string, runAt int64, ok bool)`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/scheduler/scheduler_test.go`:

```go
func TestTaskSourceIDRoundTrip(t *testing.T) {
	sid := taskSourceID("abc-123", 1700000000000)
	id, runAt, ok := parseTaskSourceID(sid)
	if !ok || id != "abc-123" || runAt != 1700000000000 {
		t.Errorf("parse(%q) = (%q, %d, %v)", sid, id, runAt, ok)
	}
	if _, _, ok := parseTaskSourceID("garbage"); ok {
		t.Error("garbage parsed as ok")
	}
}

func newSchedulerTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// storeAdapter is a minimal queue.TurnStore over *store.Store for
// tests. cmd/gopod has the production copy.
type storeAdapter struct{ s *store.Store }

func (a storeAdapter) InsertTurn(ctx context.Context, it queue.Item) (int64, bool, error) {
	return a.s.InsertTurn(ctx, store.Turn{
		Source: it.Source, SourceID: it.SourceID, ChatFolder: it.Folder, ChatID: it.ChatID,
		TGMessageID: int64(it.MessageID), IsOwner: it.IsOwner, IsVoice: it.IsVoice,
		Text: it.Text, FilePath: it.FilePath,
	})
}
func (a storeAdapter) MarkRunning(ctx context.Context, id int64) error { return a.s.MarkTurnRunning(ctx, id) }
func (a storeAdapter) MarkFinished(ctx context.Context, id int64, status, e string) error {
	return a.s.MarkTurnFinished(ctx, id, status, e)
}
func (a storeAdapter) LoadUnfinished(ctx context.Context) ([]queue.Item, error) { return nil, nil }

func TestRunTaskLogsRunningThenFinishes(t *testing.T) {
	st := newSchedulerTestStore(t)
	ctx := context.Background()
	taskID, err := st.CreateTask(ctx, store.TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "do it", ScheduleType: "once", ScheduleValue: "2020-01-01T00:00:00Z", NextRun: 1})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	handler := func(ctx context.Context, items []queue.Item) error { return nil }
	q := queue.New(ctx, handler, 1, storeAdapter{st}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := New(st, q, nil)
	q.OnDone(func(it queue.Item, err error) { close(finished) })

	task, _ := st.GetTask(ctx, taskID)
	now := int64(5000)
	s.runTask(ctx, task, now)

	<-finished
	// Give the scheduler's own OnDone hook (registered first in New)
	// time to write; it runs before ours, so the row is final here.
	run, err := st.GetTaskRun(ctx, taskID, now)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "success" || run.TurnID == 0 {
		t.Errorf("run = %+v, want success with turn_id", run)
	}
	turn, _ := st.GetTurn(ctx, run.TurnID)
	if turn.Source != "task" || turn.Status != store.TurnDone || turn.ChatID != 0 {
		t.Errorf("turn = %+v", turn)
	}
}

func TestRunTaskDupMarksSkipped(t *testing.T) {
	st := newSchedulerTestStore(t)
	ctx := context.Background()
	taskID, _ := st.CreateTask(ctx, store.TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "p", ScheduleType: "once", ScheduleValue: "2020-01-01T00:00:00Z", NextRun: 1})
	task, _ := st.GetTask(ctx, taskID)
	block := make(chan struct{})
	handler := func(ctx context.Context, items []queue.Item) error { <-block; return nil }
	q := queue.New(ctx, handler, 1, storeAdapter{st}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := New(st, q, nil)
	s.runTask(ctx, task, 5000)
	s.runTask(ctx, task, 5000) // same slot again, as after a crash mid-poll
	close(block)
	// The second log row would collide on (task_id, run_at); runTask
	// must not insert a second one, and must leave the first intact.
	run, err := st.GetTaskRun(ctx, taskID, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status == "skipped" {
		t.Error("first run row overwritten by the duplicate")
	}
}
```

Add imports: `"io"`, `"log/slog"`, `"path/filepath"`, `"github.com/Coriol-is/gopod/internal/queue"`, `"github.com/Coriol-is/gopod/internal/store"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/scheduler/ -run 'TaskSourceID|RunTask' -v`
Expected: compile error `undefined: taskSourceID`.

- [ ] **Step 3: Implement**

In `internal/scheduler/scheduler.go`:

```go
// taskSourceID is the queue dedup key for one scheduled slot.
func taskSourceID(taskID string, runAt int64) string {
	return taskID + ":" + strconv.FormatInt(runAt, 10)
}

// parseTaskSourceID inverts taskSourceID.
func parseTaskSourceID(s string) (string, int64, bool) {
	i := strings.LastIndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", 0, false
	}
	runAt, err := strconv.ParseInt(s[i+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return s[:i], runAt, true
}
```

Replace `New`:

```go
// New creates a Scheduler and registers its completion hook on q, so
// task_run_logs rows are finalized when the queue reports the turn
// done rather than when it was enqueued.
func New(st *store.Store, q *queue.Queue, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	s := &Scheduler{store: st, queue: q, log: log}
	q.OnDone(s.onTurnDone)
	return s
}

// onTurnDone finalizes the task_run_logs row for a task turn.
func (s *Scheduler) onTurnDone(it queue.Item, err error) {
	if it.Source != "task" {
		return
	}
	taskID, runAt, ok := parseTaskSourceID(it.SourceID)
	if !ok {
		s.log.Error("scheduler: bad task source id", slog.String("source_id", it.SourceID))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, errText := "success", ""
	if err != nil {
		status, errText = "error", err.Error()
	}
	var durationMs int64
	if it.ID != 0 {
		if turn, terr := s.store.GetTurn(ctx, it.ID); terr == nil && turn.StartedAt > 0 && turn.FinishedAt >= turn.StartedAt {
			durationMs = turn.FinishedAt - turn.StartedAt
		}
	}
	if ferr := s.store.FinishTaskRun(ctx, taskID, runAt, status, durationMs, errText); ferr != nil {
		s.log.Error("scheduler: FinishTaskRun", slog.String("task", taskID), slog.Any("err", ferr))
	}
}
```

Replace `runTask`:

```go
func (s *Scheduler) runTask(ctx context.Context, t store.TaskRecord, now int64) {
	// 1. Log the run as running before anything can go wrong.
	if err := s.store.LogTaskRun(ctx, store.TaskRunLog{
		TaskID: t.ID, RunAt: now, Status: "running",
	}); err != nil {
		s.log.Error("scheduler: LogTaskRun", slog.String("task", t.ID), slog.Any("err", err))
	}

	// 2. Push the prompt through the queue as if it were a Telegram
	// message. Source/SourceID make a re-fired slot a no-op.
	turnID, dup := s.queue.Enqueue(ctx, queue.Item{
		Source:   "task",
		SourceID: taskSourceID(t.ID, now),
		ChatID:   0, // no Telegram reply; tasks run silently for now
		Folder:   t.ChatFolder,
		IsOwner:  false, // tasks run as non-owner tier (conservative)
		Text:     t.Prompt,
	})
	if dup {
		s.log.Warn("scheduler: slot already enqueued, skipping",
			slog.String("task", t.ID), slog.Int64("run_at", now))
		if err := s.store.FinishTaskRun(ctx, t.ID, now, "skipped", 0, "duplicate slot"); err != nil && !errors.Is(err, store.ErrTaskRunNotFound) {
			s.log.Error("scheduler: FinishTaskRun skipped", slog.String("task", t.ID), slog.Any("err", err))
		}
		return
	}
	if turnID != 0 {
		if err := s.store.SetTaskRunTurn(ctx, t.ID, now, turnID); err != nil {
			s.log.Error("scheduler: SetTaskRunTurn", slog.String("task", t.ID), slog.Any("err", err))
		}
	}

	// 3. Compute next_run so the poll loop does not re-fire this slot.
	nextRun, newStatus := computeNextRun(t, now)
	if err := s.store.UpdateTaskStatus(ctx, t.ID, newStatus, nextRun, now); err != nil {
		s.log.Error("scheduler: UpdateTaskStatus", slog.String("task", t.ID), slog.Any("err", err))
	}

	s.log.Info("scheduler: task enqueued",
		slog.String("task", t.ID),
		slog.String("type", t.ScheduleType),
		slog.String("folder", t.ChatFolder),
		slog.Int64("turn", turnID),
		slog.String("new_status", newStatus),
		slog.Int64("next_run", nextRun))
}
```

Add imports `"errors"`, `"strconv"`, `"strings"`. Remove the now-unused `start := time.Now()` / `duration` lines.

Dup handling detail for `TestRunTaskDupMarksSkipped`: the second `LogTaskRun` inserts a second row with the same `(task_id, run_at)` (the table has no unique constraint). `FinishTaskRun("skipped")` would then update both rows. To keep the first row intact, guard step 1: call `GetTaskRun` first and skip the insert when a row already exists:

```go
	if _, err := s.store.GetTaskRun(ctx, t.ID, now); errors.Is(err, store.ErrTaskRunNotFound) {
		if err := s.store.LogTaskRun(ctx, store.TaskRunLog{TaskID: t.ID, RunAt: now, Status: "running"}); err != nil {
			s.log.Error("scheduler: LogTaskRun", slog.String("task", t.ID), slog.Any("err", err))
		}
	}
```

and in the `dup` branch do **not** call `FinishTaskRun` at all (the existing row belongs to the live turn); just log and return. Update the test expectation accordingly: `run.Status != "skipped"` already holds.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/scheduler/ -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scheduler/scheduler.go internal/scheduler/scheduler_test.go
git commit -m "scheduler: checkpoint task runs through the queue

runTask logs the run as running, enqueues with a per-slot SourceID so a
re-fired slot is a no-op, links the log row to the turn, and bumps
next_run. The completion hook registered in New finalizes the row with
the real outcome and duration instead of logging success at enqueue."
```

---

### Task 8: Wire it all in `cmd/gopod/main.go`

**Files:**
- Modify: `cmd/gopod/main.go` (queue construction block, shutdown)

**Interfaces:**
- Consumes: everything above.
- Produces: `turnStoreAdapter` (unexported) in `cmd/gopod`.

- [ ] **Step 1: Add the adapter at the bottom of `main.go`**

```go
// turnStoreAdapter maps queue.Item ↔ store.Turn so the queue package
// can persist turns without importing store (and vice versa).
type turnStoreAdapter struct{ st *store.Store }

func (a turnStoreAdapter) InsertTurn(ctx context.Context, it queue.Item) (int64, bool, error) {
	return a.st.InsertTurn(ctx, store.Turn{
		Source:      it.Source,
		SourceID:    it.SourceID,
		ChatFolder:  it.Folder,
		ChatID:      it.ChatID,
		TGMessageID: int64(it.MessageID),
		IsOwner:     it.IsOwner,
		IsVoice:     it.IsVoice,
		Text:        it.Text,
		FilePath:    it.FilePath,
	})
}

func (a turnStoreAdapter) MarkRunning(ctx context.Context, id int64) error {
	return a.st.MarkTurnRunning(ctx, id)
}

func (a turnStoreAdapter) MarkFinished(ctx context.Context, id int64, status, errText string) error {
	return a.st.MarkTurnFinished(ctx, id, status, errText)
}

func (a turnStoreAdapter) LoadUnfinished(ctx context.Context) ([]queue.Item, error) {
	turns, err := a.st.ListUnfinishedTurns(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]queue.Item, 0, len(turns))
	for _, t := range turns {
		out = append(out, queue.Item{
			ID:               t.ID,
			Source:           t.Source,
			SourceID:         t.SourceID,
			Attempts:         t.Attempts,
			PlaceholderMsgID: t.PlaceholderMsgID,
			ReplyMsgID:       t.ReplyMsgID,
			ChatID:           t.ChatID,
			MessageID:        int(t.TGMessageID),
			Folder:           t.ChatFolder,
			IsOwner:          t.IsOwner,
			Text:             t.Text,
			FilePath:         t.FilePath,
			IsVoice:          t.IsVoice,
		})
	}
	return out, nil
}
```

- [ ] **Step 2: Replace the queue construction block**

```go
		// Wire the GroupQueue if the runner is available. Turns are
		// persisted in the store so a restart can recover them.
		var agentQueue *queue.Queue
		if agentRunner != nil {
			queueLog := logger.With(slog.String("subsys", "queue"))
			agentQueue = queue.New(ctx, tgBot.NewAgentHandler(), queue.DefaultMaxConcurrent,
				turnStoreAdapter{st: st}, queueLog)
			tgBot.SetQueue(agentQueue)
			agentQueue.OnDone(tgBot.NewDoneHook())
			subsystems.Add(1)
			go func() {
				defer subsystems.Done()
				<-ctx.Done()
				agentQueue.Close()
			}()
			queueLog.Info("queue ready",
				slog.Int("max_concurrent", queue.DefaultMaxConcurrent))
		}
```

- [ ] **Step 3: Recover after the scheduler is constructed, before the bot runs**

Right after the scheduler block (which calls `scheduler.New` and therefore registers its hook), add:

```go
		if agentQueue != nil {
			n, err := agentQueue.Recover(ctx)
			if err != nil {
				logger.Error("queue: recover unfinished turns", slog.Any("err", err))
			} else if n > 0 {
				logger.Info("queue: recovered unfinished turns", slog.Int("count", n))
			}
		}
```

This must stay above the `tgBot.Run(ctx)` goroutine so recovered turns are scheduled before new updates arrive.

- [ ] **Step 4: Build, vet, full test**

Run: `go build ./... && go vet ./... && go test ./... -race`
Expected: green.

- [ ] **Step 5: Manual crash test (dev machine, Docker up)**

1. `go run ./cmd/gopod`, send "count slowly from 1 to 30, one number per line" from the owner chat.
2. While the placeholder is updating, `kill -9` the gopod pid.
3. `sqlite3 data/store.sqlite "select id,status,attempts,placeholder_msg_id from turns order by id desc limit 1"` → status `running`, attempts 1, placeholder set.
4. `go run ./cmd/gopod` again. Expected log: `queue: recovering turn … resumed=true`; in Telegram the `▍` message becomes `⟳ resuming after restart`, then the reply.
5. Same query → `done`, attempts 2, `reply_msg_id` set; `select content from messages where sender='bot' order by id desc limit 1` shows the reply.
6. Ctrl-C during a turn → log `queue: turn interrupted by shutdown`, row `interrupted`; restart resumes it.

Record the outcome in HANDOFF (Task 9).

- [ ] **Step 6: Commit**

```bash
git add cmd/gopod/main.go
git commit -m "gopod: persist and recover agent turns at boot

Adapt *store.Store to queue.TurnStore, build the queue on the app
context, close it on shutdown, register the Telegram crash-loop hook,
and call Recover after the scheduler hook is wired and before Telegram
starts polling."
```

---

### Task 9: Docs and ADR

**Files:**
- Modify: `docs/ARCHITECTURE.md` §4.3 (schema listing, after `task_run_logs`)
- Modify: `docs/DECISIONS.md` (append D020)
- Modify: `docs/HANDOFF.md`, `ROADMAP.md`

- [ ] **Step 1: ARCHITECTURE §4.3**

After the `task_run_logs` CREATE block, add the `turns` CREATE statement from Task 1 verbatim and one paragraph: "`turns` is the durable queue: one row per requested agent run, written before dispatch and finalized after. `UNIQUE(source, source_id)` deduplicates Telegram redeliveries and re-fired scheduler slots. Boot recovery replays `pending`, `running` and `interrupted` rows; see `docs/superpowers/specs/2026-10-03-durable-turns-design.md`." Add `turn_id INTEGER` to the `task_run_logs` block.

- [ ] **Step 2: ADR**

Append to `docs/DECISIONS.md`:

```markdown
## D020 — Durable turns: the queue persists every agent run

**Status:** accepted 2026-10-03
**Refines:** [D005](#d005--groupqueue-per-chat-serialization) (memory-only queue)

**Context.** The GroupQueue mirrored NanoClaw: in memory, workers on
`context.Background()`. A crash mid-turn lost the user's message, the
pending backlog, and left the streaming placeholder dangling. The
scheduler logged `success` at enqueue time, so a crash could mark a
task done that never ran. Pi Durable's checkpoint model (every
submission is a row before it runs; idempotent request ids; the model
is told when its turn was interrupted) fits gopod's single-SQLite rule.

**Decision.** `internal/queue` owns durability through a consumer-side
`TurnStore` interface backed by a `turns` table. `Enqueue` inserts
first; workers mark `running`/`done`/`failed`/`interrupted`; `Recover`
replays unfinished rows at boot with `Resumed=true`. The Telegram
message id is the request id. Resumed turns are not re-run blindly and
not dropped: the session continues with an interruption notice and the
agent decides what to redo, because gopod does not own the tool set and
cannot classify side effects. Placeholder and reply message ids are
memoized on the row (first write wins) so recovery never double-replies.
The scheduler finalizes `task_run_logs` through a queue completion hook.
Max attempts across restarts is a constant, 3.

**Rejected.** DB-as-queue polling (rewrites M3, adds latency, needs row
locking); per-producer outboxes (recovery logic in two places, scheduler
still blind to completion); conversation forks, multiplayer and tool
replay flags from Pi (no second user, no owned tools).

**Consequences.** `queue.New` takes the app context and a store; the
`GOPOD_NO_CONTAINER` dev path passes nil and keeps memory-only
behaviour. Outbound bot replies are now persisted to `messages`. First
schema migration via the existing `migrations` list
(`task_run_logs.turn_id`).
```

Check the `D005` anchor name with `grep -n '^## D005' docs/DECISIONS.md` and adjust the link text to match.

- [ ] **Step 3: HANDOFF + ROADMAP**

Run `/handoff`. In HANDOFF: new "Current state" paragraph naming the spec, plan, and the manual crash test result from Task 8 step 5; move the durable-turns item into "What's done"; keep the memory-API and FreeFeed notes. In ROADMAP: add a row `DT — Durable turns` ✅ under post-milestone work.

- [ ] **Step 4: Commit**

```bash
git add docs/ARCHITECTURE.md docs/DECISIONS.md docs/HANDOFF.md ROADMAP.md
git commit -m "docs: record durable turns (D020) and update handoff"
```

---

## Self-review

**Spec coverage.** §3 schema → Task 1 (+ `turn_id` migration) and Task 2. §4 queue (`TurnStore`, `Item` fields, insert/mark, `OnDone`, ctx workers, `Close`) → Task 3; `Recover` + attempts cap → Task 4. §5 resume prompt, placeholder reuse, silent tasks → Tasks 5–6. §6 memo write order + outbound `SaveMessage` → Tasks 5–6. §7 scheduler → Task 7 (log row addressed by `(task_id, run_at)`, hook in `New`, `SourceID` parse). §8 shutdown → Task 3 (`Close`, interrupted) + Task 8 (WaitGroup). §9 error table → Task 3 tests (`TestEnqueueStoreErrorDrops`, `TestHandlerErrorMarksFailedAfterRetries`, `TestCloseInterruptsInFlightTurn`), Task 4 (`TestRecoverCrashLoopIsFailedWithoutRunning`), Task 5 (`reusePlaceholder` fallback). §10 tests → each task. §11 rollout → task order matches DT1–DT6. §12/§13 → ADR in Task 9.

**Placeholders.** None; every step has code. Task 7 step 3 was revised inline (dup guard) rather than left vague.

**Type consistency.** `queue.New(ctx, handler, n, store, log)` used identically in Tasks 3, 7, 8. `Enqueue` returns `(int64, bool)` in Tasks 3, 6, 7. Store method names `InsertTurn/MarkTurnRunning/MarkTurnFinished/SetTurnPlaceholder/SetTurnReply/GetTurn/ListUnfinishedTurns` match between Task 1, the test adapter in Task 7 and the production adapter in Task 8. `store.TurnDone` used in Task 7 test matches Task 1 const. `queue.ErrCrashLoop` defined Task 4, used Task 5. `b.recordReply/recordPlaceholder/reusePlaceholder/resumePrompt` defined Task 5, used Task 6. `replyTo` returns `int` after Task 6; Task 5's `NewDoneHook` ignores the result, fine.

**Review Focus.** 1 → `TestEnqueueStoreErrorDrops` (Task 3). 2 → covered by existing behaviour: `runAgentSync` logs and replies on runner error and `NewAgentHandler` returns nil, so the row ends `done`; no new test, documented here. 3 → `TestRecoverPreservesOrderPerChat` (Task 4). 4 → `reusePlaceholder` returns 0 on edit error (Task 5); needs a Telegram API fake to unit-test, so it is covered by the manual crash test step 6 variant (delete the placeholder before restarting) added to Task 8 step 5 as an optional check. 5 → `TestRunTaskDupMarksSkipped` (Task 7).
