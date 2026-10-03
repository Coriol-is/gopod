# Durable turns — design

Checkpointed agent turns so a gopod crash or restart loses no user
message and no scheduled run. Every turn is a row in `store.sqlite`
before it runs; boot recovery resumes whatever was in flight.

Status: design approved 2026-10-03. Implementation not started.
Prior art: Pi Durable (checkpoint-based recovery, idempotent
submissions, first-write-wins memos, model notified of interruption).
Cherry-picked, not ported: no forks, no multiplayer, no tool replay
declarations.

## 1. Problem

Today the queue (`internal/queue`) is memory only. Workers run on
`context.Background()`. Consequences:

- gopod dies mid-turn: user sees 👀 reaction and a `▍` placeholder
  forever. Nothing resumes on restart.
- Messages pending behind a busy chat die with the process.
- SIGTERM does not reach workers; the turn is cut off without a trace.
- `scheduler.runTask` enqueues and immediately logs `success` and
  bumps `next_run`. A crash between enqueue and run marks the task done
  although it never ran.
- Bot replies are never written to `messages`, so the SQLite transcript
  is inbound only.

## 2. Architecture

The queue owns durability. Producers (Telegram, scheduler) keep calling
`Enqueue`; the queue writes the turn row first, dispatches in memory as
today, and finalizes the row when the handler returns. A completion hook
lets producers react to the real end of a turn.

```
Telegram update ─┐                      ┌─ telegram handler (run + reply)
                 ├─ queue.Enqueue ──────┤
scheduler poll ──┘   │                  └─ queue.OnDone hooks
                     │                        ├─ scheduler: finalize task_run_logs
                     ▼                        └─ telegram: ❌ + notice on crash loop
              store.turns (SQLite)
                     ▲
 boot ── queue.Recover ── LoadUnfinished ── re-enqueue (Resumed=true)
```

State machine per row:

```
pending ──► running ──► done
   ▲           │  └───► failed
   │           └──────► interrupted   (SIGTERM, graceful)
   └── Recover: running|interrupted → re-enqueued as Resumed
```

## 3. Schema

New table, applied by `applySchema` like the others:

```sql
CREATE TABLE IF NOT EXISTS turns (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  source             TEXT NOT NULL,                 -- 'telegram' | 'task'
  source_id          TEXT NOT NULL,                 -- "<tg_chat_id>:<tg_message_id>" | "<task_id>:<next_run>"
  chat_folder        TEXT NOT NULL,
  chat_id            INTEGER NOT NULL,              -- Telegram chat id, 0 for task
  tg_message_id      INTEGER NOT NULL DEFAULT 0,
  is_owner           INTEGER NOT NULL DEFAULT 0,
  is_voice           INTEGER NOT NULL DEFAULT 0,
  text               TEXT NOT NULL,
  file_path          TEXT,
  status             TEXT NOT NULL,                 -- pending|running|done|failed|interrupted
  attempts           INTEGER NOT NULL DEFAULT 0,
  placeholder_msg_id INTEGER NOT NULL DEFAULT 0,
  reply_msg_id       INTEGER NOT NULL DEFAULT 0,
  error              TEXT,
  created_at         INTEGER NOT NULL,
  started_at         INTEGER,
  finished_at        INTEGER,
  UNIQUE(source, source_id)
);
CREATE INDEX IF NOT EXISTS idx_turns_status ON turns(status);
```

`UNIQUE(source, source_id)` makes enqueue idempotent. Telegram
re-delivers updates whose offset was never acknowledged; the scheduler
can fire the same slot twice after a restart. Both collapse on insert.
The Telegram chat id + message id is the request id (message ids are
unique only within a chat), no new id scheme.

`task_run_logs` gains `turn_id INTEGER`. First schema change that is
not `CREATE ... IF NOT EXISTS`; done as a guarded
`ALTER TABLE ... ADD COLUMN` (check `PRAGMA table_info` first). No
`schema_version` yet; still additive. The table keeps no primary key;
rows are addressed by `(task_id, run_at)`, which is unique per
scheduled slot.

Store API (`internal/store/turns.go`):

```go
type Turn struct { /* mirrors the row; Status is a string */ }

func (s *Store) InsertTurn(ctx, t Turn) (id int64, dup bool, err error)
func (s *Store) MarkTurnRunning(ctx, id int64) error            // attempts++, started_at=now
func (s *Store) MarkTurnFinished(ctx, id int64, status, errText string) error
func (s *Store) SetTurnPlaceholder(ctx, id int64, msgID int) error
func (s *Store) SetTurnReply(ctx, id int64, msgID int) error
func (s *Store) ListUnfinishedTurns(ctx) ([]Turn, error)        // pending|running|interrupted, by id
```

Rows are never deleted by this feature. A retention sweep (keep last N
per chat) is out of scope; the table is small (one row per turn).

## 4. Queue changes

`internal/queue` declares the interface it needs in terms of `Item`.
`store` must not import `queue`, so a ten-line adapter in
`cmd/gopod/main.go` maps `queue.Item` ↔ `store.Turn` and forwards to
the `*store.Store` methods in §3. Nil store keeps today's memory-only
behaviour for `GOPOD_NO_CONTAINER` dev runs.

```go
type TurnStore interface {
    InsertTurn(ctx context.Context, it Item) (id int64, dup bool, err error)
    MarkRunning(ctx context.Context, id int64) error
    MarkFinished(ctx context.Context, id int64, status, errText string) error
    LoadUnfinished(ctx context.Context) ([]Item, error)
}
```

`Item` gains:

```go
ID       int64  // turns.id, 0 when store is nil
Source   string // "telegram" | "task"
SourceID string
Resumed  bool   // set by Recover for rows that were running/interrupted
Attempts int    // from the row, for the crash-loop cap
```

Behaviour:

- `New(ctx, handler, maxConcurrent, store, log)`. Workers derive from
  this ctx instead of `context.Background()`. The ctx is the app ctx
  from `signal.NotifyContext`; the Telegram request ctx is still not
  used.
- `Enqueue(ctx, item) (id int64, dup bool)`: `InsertTurn` first.
  `dup == true` → log at debug, return `(0, true)`. Insert error → log
  at error, return `(0, false)` (message is still in `messages`, nothing
  else to do). Then today's in-memory path. Existing callers ignore the
  return values.
- Worker, per batch: `MarkRunning` for every item in the batch, call
  handler, then `MarkFinished(done|failed)` for every item. Coalescing
  stays: handler still runs only the last item, earlier items are
  marked with the same outcome.
- Retry loop unchanged; `attempts` grows per `MarkRunning`.
- `OnDone(fn func(Item, error))`: appends a hook. Hooks run
  synchronously after `MarkFinished`, in the worker goroutine, in
  registration order. Panics are not recovered (same as handler).
- `Recover(ctx)`: `LoadUnfinished`. For each row: `status running` or
  `interrupted` → `Resumed = true`. If `Attempts >= MaxAttempts` (3,
  constant) → `MarkFinished(failed, "crash loop")`, fire hooks, skip.
  Otherwise append to the chat's pending list (no new insert) and start
  a worker. Called once from `main.go` after the queue and both
  producers are wired, before Telegram starts polling.
- `Close()`: cancels the worker ctx, waits for workers. Main adds it to
  the subsystems `WaitGroup`. A worker whose ctx is cancelled mid-handler
  marks the batch `interrupted` instead of `failed`.

## 5. Recovery turn (Telegram)

A `Resumed` item with `reply_msg_id != 0` already got its answer: the
crash hit between send and `MarkFinished`. Handler returns nil without
running anything.

Otherwise the handler builds the prompt through a pure function:

```go
func resumePrompt(userText string) string
// "[gopod] The previous turn was interrupted by a restart before a reply
//  was sent. The user's message was:\n\n<text>\n\nContinue where you
//  left off, or reply."
```

Session continuity does the rest: Claude runs `--continue`, Codex
`resume --last`, so the agent sees its own partial transcript and
tool calls. gopod does not decide what is safe to redo; the model does.
This is the Pi "model notified of interruption" branch, and the only
one gopod can implement because it does not own the tool set.

Placeholder: if `placeholder_msg_id != 0`, edit it to
`⟳ resuming after restart` and stream into it; do not send a new one.
If the edit fails (message deleted), fall back to a new placeholder.

Task turns (`chat_id == 0`) resume silently.

## 6. Reply memo and outbound persistence

Write order in the Telegram handler, both sync and streaming paths:

1. `sendPlaceholder` → `SetTurnPlaceholder(id, msgID)`.
2. Final send or final edit → `SetTurnReply(id, msgID)`.
3. `SaveMessage` for the outbound text: `Sender "bot"`, `IsFromMe`,
   `IsBotMessage`, `TGMessageID` = sent id, `ReplyToTGMessageID` = the
   user's message. Overflow replies (text split across messages) save
   the last one only.
4. Return to the queue, which marks `done`.

A crash between 2 and 4 is the window the memo closes. A crash between
the Telegram API call and 2 duplicates the reply once; accepted,
documented, milliseconds wide.

`SetTurnReply` and `SaveMessage` failures are logged, not returned: the
user already has the reply.

## 7. Scheduler

`runTask` becomes:

1. `InsertTaskRun(task_id, run_at, status "running")` → log row id.
2. `Enqueue(Item{Source: "task", SourceID: taskID + ":" + nextRun, ...})`.
   Dup → mark the log row `skipped`.
3. Bump `next_run` / `last_run` / status as today, so the poll loop does
   not re-fire the slot.
4. Store `turn_id` on the log row, addressed by `(task_id, run_at)`,
   using the id returned by `Enqueue`.

Completion via `queue.OnDone`, registered in `scheduler.New`:
`Source == "task"` → parse `task_id` and `run_at` back out of
`SourceID` → `FinishTaskRun(task_id, run_at, status, duration, error)`
with duration from the turn's `started_at`/`finished_at`. Resumed task
turns flow through the same hook. Existing rows keep `turn_id NULL`.

## 8. Shutdown

- `main.go` passes the app ctx into `queue.New`.
- SIGTERM → ctx cancelled → `docker exec` stream cancelled → handler
  returns an error wrapping `context.Canceled` → worker marks the batch
  `interrupted`, skips retry/backoff, exits.
- `Queue.Close()` joins the subsystems `WaitGroup`. No grace timer: the
  exec cancellation is immediate.
- Pending items left in memory are already `pending` rows; nothing to
  flush.
- Next boot: `Recover` picks up `interrupted` and `pending` rows.

## 9. Error handling

| Situation | Behaviour |
|---|---|
| `InsertTurn` fails | log error, drop (message still in `messages`) |
| Duplicate insert | debug log, drop |
| `MarkRunning`/`MarkFinished` fails | log error, continue; row may be recovered twice, memo protects the reply |
| Handler error, infra | existing retry/backoff, `attempts` persisted |
| Handler error, user-visible (auth, agent crash) | handler returns nil as today; row `done` |
| `Attempts >= 3` at recovery | `failed("crash loop")`, hook: ❌ reaction + "Gave up on this message after 3 restarts. Please resend." |
| Placeholder edit fails on resume | new placeholder |
| ctx cancelled mid-turn | `interrupted`, no backoff |

## 10. Testing

- `store/turns_test.go`: insert, dup returns `(0, true, nil)`, running
  increments attempts and sets `started_at`, finished sets
  `finished_at`, `ListUnfinishedTurns` order and filter. Migration test:
  open a store whose `task_run_logs` predates `turn_id`, reopen, column
  present, rows intact.
- `queue/queue_test.go` with a fake `TurnStore` recording calls:
  insert precedes handler; running precedes handler; finished follows
  handler for every item in a coalesced batch; dup never reaches the
  handler; `OnDone` fires once per item with the handler error;
  `Recover` re-enqueues with `Resumed` and no insert; attempts cap →
  failed + hook, no handler call; ctx cancel → `interrupted`.
- `telegram`: `resumePrompt` golden; handler with `Resumed` and
  `reply_msg_id != 0` makes no runner call; placeholder reuse path.
- `scheduler/scheduler_test.go`: log row `running` at enqueue, finalized
  by the hook, `skipped` on dup.
- Manual: send a message, `kill -9` gopod during the turn, restart,
  observe the placeholder turn into the reply. Documented in HANDOFF.

## 11. Rollout

1. **DT1** store: `turns` table, `turns.go`, migration helper for
   `task_run_logs`.
2. **DT2** queue: `TurnStore`, item fields, insert/mark, `OnDone`,
   ctx-derived workers, `Close`.
3. **DT3** queue: `Recover`, attempts cap; `main.go` wiring.
4. **DT4** telegram: resume prompt, placeholder reuse, memo writes,
   outbound `SaveMessage`, crash-loop notice hook.
5. **DT5** scheduler: `running` log row, `turn_id`, completion hook.
6. **DT6** docs: ARCHITECTURE §4.3 schema, ADR for persistent queue,
   HANDOFF.

DT1–DT3 ship without user-visible change; DT4 is where recovery becomes
observable.

## 12. Decisions log (brainstorm 2026-10-03)

- Queue owns persistence; DB-as-queue (poll loop) and per-producer
  outbox rejected. Keeps instant dispatch and one place for recovery.
- Resume with interruption notice, not re-run and not notify-only.
  gopod cannot classify tool safety; the model with its transcript can.
- Reply memo on the turn row, not a separate table.
- Max attempts is a constant (3), not an env var.
- Telegram chat id + message id is the request id. No UUIDs.
- All four features (turn log, memo + outbound, scheduler checkpoint,
  graceful shutdown) in one spec; split into DT1–DT6 for execution.

## 13. Out of scope

Conversation forks, multiplayer subscriptions, typed document state,
tool replay declarations, turn retention sweep, changing coalescing
(last-message-wins), a `schema_version` migration runner.
