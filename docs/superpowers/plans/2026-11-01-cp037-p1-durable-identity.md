# CP-037 P1 — Durable session identity: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every conversation a durable execution-session identity (binding → session → context epoch → native context → attempt binding → execution claim) in SQLite, selectable by an atomic adoption marker, without changing serial-mode behavior by default.

**Architecture:** Additive tables in `internal/store` keyed by the registration `ScopeRef` (namespace id + epoch), never by folder alone. The runner keeps its consumer-side `stateStore` interface; a new adapter in `cmd/gopod` resolves `(folder, thread, provider)` through the binding when the `router_state` marker `session_mapping` is `durable`, and the old `sessions` table stays authoritative while it is `legacy`. Adoption happens once at controller startup, inside one transaction, when the owner has requested it with `/sessions adopt`; queue batches are bound to their session/context epoch by a handler wrapper so recovery can refuse to replay a turn into a changed context.

**Tech Stack:** Go 1.27, `ncruces/go-sqlite3` (no CGO), `internal/control` Router, `internal/queue`, stdlib `testing`.

**Spec:** [contract A](../specs/2026-11-01-session-state-implementation.md) §A1, §A2 (ordering/dedup only; `/clear`, `/new`, `/compact` transitions are P2), §A4; [architecture §3](../specs/2026-11-01-parallel-sessions.md); [slice plan](2026-11-01-parallel-sessions.md) row P1.

## Global Constraints

- Go only, no CGO; DB access only through `internal/store`; `context.Context` first; errors wrapped with `%w`; no silent swallowing (CLAUDE.md).
- Additive schema only: `CREATE TABLE IF NOT EXISTS`, no destructive migration, no cascade deletes of evidence (A1, A4).
- "Old `sessions` mappings remain authoritative in legacy mode; a migration marker atomically selects the new mapping adapter, never two writers" (A1). The marker is read once per process at startup.
- "Every foreign relationship validates owner scope" (A1): every write takes a `ScopeRef` and runs `ValidateScopeTx` inside the same transaction.
- "Expose stale/conflict/blocked/retired/not-qualified outcomes separately, never silently choose another session" (A1): distinct sentinel errors, listed in Task 1.
- "Maintain a unique active execution claim per session ... `execution_unknown` is a held claim, not terminal availability" (A1).
- "Only compatible interactive items of the same session/epoch may coalesce; task attempts are singleton batches. Durable attempt bindings outlive retries." (A2) In P1 a thread has exactly one active session and epoch, so the existing `sameBatch` (same ThreadID, same source kind, never managed) already satisfies this; `turns.id` is the monotonic per-conversation ingress sequence and `UNIQUE(source, source_id)` the dedup key. D055 records both so P2 does not add parallel sequencing.
- "Retain serial default; source registration and group behavior do not broaden." (A4) Default mode is `legacy`; nothing changes for a deployment that never runs `/sessions adopt`.
- All admin commands go through `internal/control`; `Perm` decides authorization; destructive actions use the D028 confirmation button.
- Commit messages `<area>: <imperative summary>`; no attribution lines.

## Review Focus

1. A re-registered chat (same folder, new namespace epoch) must get a fresh binding and its old sessions must read as retired, never resumed — Task 1 test `TestResolveConversationRetiresOldEpoch`.
2. A turn recovered after a restart whose binding points at an older context epoch must be finished as interrupted, not run into the current context — Task 3 test `TestBindingHandlerRefusesStaleEpoch`.
3. Adoption that fails halfway (a `sessions` row for a folder with no registration) must leave the marker `legacy` and write no session rows — Task 1 test `TestAdoptLegacySessionsIsAtomic`.
4. `/sessions adopt` while turns are unfinished must be refused, so adoption never races a running agent's `PutSession` — Task 4 test `TestSessionsAdoptRefusedWhileBusy`.
5. In `durable` mode a legacy `/clear` (DeleteSession) must remove only the current epoch's native context for that provider, leaving the binding, the session and other providers' contexts intact — Task 2 test `TestDurableAdapterDeleteRemovesOnlyProviderContext`.

---

### Task 1: Store schema and session identity API

**Files:**
- Create: `internal/store/schema_sessions.go` (DDL + `applySessionSchema`)
- Create: `internal/store/sessions_durable.go` (types, errors, API)
- Create: `internal/store/sessions_adopt.go` (marker + adoption)
- Modify: `internal/store/schema.go` — call `applySessionSchema` from `applySchema` after `applyControlMessageSchema`
- Test: `internal/store/sessions_durable_test.go`, `internal/store/sessions_adopt_test.go`

**Interfaces:**
- Consumes: `ScopeRef`, `ValidateScopeTx`, `withTx`, `taskSQL`, `newUUID`, `ListRegistered`, `ListUnfinishedTurns`, `GetState`/`SetState` (all existing).
- Produces (exact names, used by Tasks 2–4):

```go
const (
	SessionMappingLegacy  = "legacy"
	SessionMappingDurable = "durable"
	sessionMappingKey     = "session_mapping"           // router_state
	sessionMappingReqKey  = "session_mapping_requested" // router_state, "durable" or ""
)

var (
	ErrConversationRetired = errors.New("store: conversation binding is retired")
	ErrStaleContextEpoch   = errors.New("store: attempt context epoch is not current")
	ErrAttemptBound        = errors.New("store: turn already bound to a different session")
	ErrSessionBusy         = errors.New("store: session already holds an execution claim")
	ErrAdoptionBusy        = errors.New("store: unfinished turns exist; adoption needs a quiesced controller")
	ErrAdoptionUnregistered = errors.New("store: legacy session belongs to an unregistered folder")
)

type ConversationBinding struct {
	NamespaceID     string
	NamespaceEpoch  int64
	ThreadID        int
	ActiveSessionID string
	ContextEpoch    int64 // of the active session
	WorkspaceID     string
	Revision        int64
	Lifecycle       string // "active" | "retired"
}

type ExecutionSession struct {
	SessionID            string
	NamespaceID          string
	NamespaceEpoch       int64
	RegistrationRevision int64
	Folder               string
	ThreadID             int
	Kind                 string // "conversation" | "task_attempt"
	ContextEpoch         int64
	WorkspaceID          string
	Lifecycle            string // "active" | "archived" | "retired"
	CreatedAt, ArchivedAt int64
}

type AttemptBinding struct {
	TurnID             int64
	SessionID          string
	ContextEpoch       int64
	Provider, Model    string
	ProfileFingerprint string
	RuntimeGeneration  string
	CreatedAt          int64
}

type AdoptionReport struct {
	Mode       string // mode after the call
	Sessions   int    // execution_sessions created
	Contexts   int    // native_contexts created
	AlreadyDone bool
}

func (s *Store) ResolveConversation(ctx context.Context, scope ScopeRef, thread int) (ConversationBinding, error)
func (s *Store) GetExecutionSession(ctx context.Context, sessionID string) (ExecutionSession, error)
func (s *Store) GetNativeContext(ctx context.Context, sessionID string, epoch int64, provider string) (nativeID string, ok bool, err error)
func (s *Store) PutNativeContext(ctx context.Context, scope ScopeRef, sessionID string, epoch int64, provider, nativeID string) error
func (s *Store) DeleteNativeContext(ctx context.Context, scope ScopeRef, sessionID string, epoch int64, provider string) error
func (s *Store) BindAttempt(ctx context.Context, scope ScopeRef, b AttemptBinding) error
func (s *Store) GetAttemptBinding(ctx context.Context, turnID int64) (AttemptBinding, bool, error)
func (s *Store) AcquireExecutionClaim(ctx context.Context, scope ScopeRef, sessionID string, turnID int64) error
func (s *Store) SettleExecutionClaim(ctx context.Context, sessionID string, turnID int64, unknown bool) error
func (s *Store) SessionMappingMode(ctx context.Context) (mode string, requested bool, err error)
func (s *Store) RequestSessionAdoption(ctx context.Context) error
func (s *Store) AdoptLegacySessions(ctx context.Context) (AdoptionReport, error)
```

Semantics the signatures do not determine:
- `ResolveConversation`: inside one `withTx`: `ValidateScopeTx(scope)`; select binding by `(scope.NamespaceID, scope.NamespaceEpoch, thread)`; if absent, create `execution_sessions` row (`session_id = newUUID()`, `kind = conversation`, `context_epoch = 1`, `workspace_id = "legacy:" + scope.Folder`, `lifecycle = active`) and the binding (`revision = 1`, `lifecycle = active`); if a binding exists for the same namespace id with a **lower** epoch and lifecycle `active`, set it and its session `retired` first (A11). Returns `ErrConversationRetired` when the matching binding is `retired`.
- `PutNativeContext`/`DeleteNativeContext`: validate scope and that the session is `active` and belongs to `scope.NamespaceID/NamespaceEpoch`; upsert / delete the `(session_id, epoch, provider)` row. Put refuses `epoch != session.context_epoch` with `ErrStaleContextEpoch`.
- `BindAttempt`: validate scope; session must be active and owned by the scope; `b.ContextEpoch` must equal the session's current epoch (`ErrStaleContextEpoch`); insert; a second call with the same `TurnID` returns nil when `SessionID` and `ContextEpoch` match and `ErrAttemptBound` otherwise.
- `AcquireExecutionClaim`: insert into `execution_claims(session_id PRIMARY KEY, turn_id UNIQUE, state 'held', claimed_at)`; an existing row for the session with a different turn returns `ErrSessionBusy`; the same turn is idempotent. `SettleExecutionClaim(unknown=false)` deletes the row; `unknown=true` sets `state='execution_unknown'` and keeps it (still blocks a new claim).
- `SessionMappingMode`: `GetState(sessionMappingKey)`, empty means `legacy`; `requested` is `GetState(sessionMappingReqKey) == "durable"`.
- `AdoptLegacySessions`: if mode already `durable` return `{Mode: durable, AlreadyDone: true}`; if `ListUnfinishedTurns` non-empty return `ErrAdoptionBusy`; else one `withTx`: for every row of `sessions`, find the registered chat by folder (`ListRegistered`); none → `ErrAdoptionUnregistered` (whole tx rolls back); resolve/create the binding for `(scope, thread_id)`, insert `native_contexts(session, 1, provider, session_id)`; finally `router_state[sessionMappingKey] = durable` and clear `sessionMappingReqKey`, all in the same tx (use `tx.ExecContext` on `router_state`, not `SetState`).

DDL (`schema_sessions.go`), all `CREATE TABLE IF NOT EXISTS`:
- `execution_sessions(session_id TEXT PRIMARY KEY CHECK(session_id<>''), namespace_id TEXT NOT NULL, namespace_epoch INTEGER NOT NULL, registration_revision INTEGER NOT NULL, folder TEXT NOT NULL, thread_id INTEGER NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('conversation','task_attempt')), context_epoch INTEGER NOT NULL CHECK(context_epoch>=1), workspace_id TEXT NOT NULL, lifecycle TEXT NOT NULL CHECK(lifecycle IN ('active','archived','retired')), created_at INTEGER NOT NULL, archived_at INTEGER)`
- `conversation_bindings(namespace_id TEXT NOT NULL, namespace_epoch INTEGER NOT NULL, thread_id INTEGER NOT NULL, active_session_id TEXT NOT NULL REFERENCES execution_sessions(session_id) ON DELETE RESTRICT, revision INTEGER NOT NULL DEFAULT 1, lifecycle TEXT NOT NULL CHECK(lifecycle IN ('active','retired')), updated_at INTEGER NOT NULL, PRIMARY KEY(namespace_id, namespace_epoch, thread_id))`
- `native_contexts(session_id TEXT NOT NULL REFERENCES execution_sessions(session_id) ON DELETE RESTRICT, context_epoch INTEGER NOT NULL, provider TEXT NOT NULL, native_id TEXT NOT NULL CHECK(native_id<>''), profile_revision TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL, PRIMARY KEY(session_id, context_epoch, provider))`
- `attempt_bindings(turn_id INTEGER PRIMARY KEY REFERENCES turns(id) ON DELETE RESTRICT, session_id TEXT NOT NULL REFERENCES execution_sessions(session_id) ON DELETE RESTRICT, context_epoch INTEGER NOT NULL, provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', profile_fingerprint TEXT NOT NULL DEFAULT '', runtime_generation TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL)`
- `execution_claims(session_id TEXT PRIMARY KEY REFERENCES execution_sessions(session_id) ON DELETE RESTRICT, turn_id INTEGER NOT NULL UNIQUE REFERENCES turns(id) ON DELETE RESTRICT, state TEXT NOT NULL CHECK(state IN ('held','execution_unknown')), claimed_at INTEGER NOT NULL)`
- index `idx_execution_sessions_scope ON execution_sessions(namespace_id, namespace_epoch, thread_id)`

- [ ] **Step 1: Write the failing schema/constraint tests (A01)** in `sessions_durable_test.go`, using `openTestStore` and a registered owner chat created the way `namespaces_test.go` does it:

```go
func TestResolveConversationCreatesOnceAndIsIdempotent(t *testing.T)  // same scope+thread twice -> same SessionID, ContextEpoch 1, Revision 1
func TestResolveConversationIsPerThread(t *testing.T)                 // thread 0 and 7 -> different sessions
func TestResolveConversationRejectsStaleScope(t *testing.T)           // scope with RegistrationRevision+1 -> errors.Is(err, ErrStaleScope)
func TestResolveConversationRetiresOldEpoch(t *testing.T)             // resolve at epoch 1; rotate the registration epoch (RotateGroupEpoch + CompleteGroupEpoch, as namespace_epochs_test.go does) and resolve with the new ScopeRef -> new SessionID; GetExecutionSession(old).Lifecycle == "retired"; resolving with the epoch-1 scope again -> ErrStaleScope
func TestNativeContextRoundTrip(t *testing.T)                         // Put/Get/Delete per provider; Put with epoch 2 -> ErrStaleContextEpoch
func TestBindAttemptIsIdempotentAndEpochChecked(t *testing.T)         // bind twice same -> nil; bind to other session -> ErrAttemptBound; epoch 2 -> ErrStaleContextEpoch
func TestExecutionClaimIsUniquePerSession(t *testing.T)               // acquire turn A ok; turn B -> ErrSessionBusy; settle unknown keeps blocking; settle false frees
func TestSessionSchemaConstraints(t *testing.T)                       // raw INSERT into execution_sessions with context_epoch 0 fails; duplicate binding PK fails
```

- [ ] **Step 2: Run `go test ./internal/store -run 'ResolveConversation|NativeContext|BindAttempt|ExecutionClaim|SessionSchema' -v`** — expected: compile failure (undefined symbols).

- [ ] **Step 3: Implement `schema_sessions.go` and `sessions_durable.go`** with the signatures above; wire `applySessionSchema` into `applySchema`.

- [ ] **Step 4: Run the same tests** — expected: PASS.

- [ ] **Step 5: Write the failing adoption tests** in `sessions_adopt_test.go`:

```go
func TestSessionMappingDefaultsToLegacy(t *testing.T)                 // mode "legacy", requested false
func TestAdoptLegacySessionsMapsEveryRow(t *testing.T)                // 2 registered chats, sessions rows (folderA,0,claude),(folderA,3,codex),(folderB,0,claude) -> report Sessions 3, Contexts 3, Mode durable; GetNativeContext returns the legacy ids; requested cleared
func TestAdoptLegacySessionsIsAtomic(t *testing.T)                    // extra sessions row for unregistered folder -> ErrAdoptionUnregistered; mode still legacy; SELECT count(*) FROM execution_sessions == 0
func TestAdoptLegacySessionsRefusedWhileBusy(t *testing.T)            // one pending turn -> ErrAdoptionBusy
func TestAdoptLegacySessionsIsIdempotent(t *testing.T)                // second call -> AlreadyDone true, counts unchanged
```

- [ ] **Step 6: Run `go test ./internal/store -run 'SessionMapping|AdoptLegacy' -v`** — expected: FAIL (undefined).

- [ ] **Step 7: Implement `sessions_adopt.go`.**

- [ ] **Step 8: Run `go test ./internal/store` and `go test -race ./internal/store`** — expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/store/schema.go internal/store/schema_sessions.go internal/store/sessions_durable.go internal/store/sessions_adopt.go internal/store/sessions_durable_test.go internal/store/sessions_adopt_test.go
git commit -m "store: add durable session identity tables and adoption marker"
```

---

### Task 2: Durable session adapter for the runner

**Files:**
- Create: `cmd/gopod/session_adapter.go`
- Test: `cmd/gopod/session_adapter_test.go`
- Modify: `cmd/gopod/main.go` — where `agentRunner.SetStore(st)` is called, select the adapter by mode (see Step 3).

**Interfaces:**
- Consumes: Task 1 API; `runner.stateStore` (unexported interface: `GetState`, `SetState`, `GetSession`, `PutSession`, `DeleteSession`); `store.Store.SnapshotScope(ctx, folder)`.
- Produces:

```go
// runnerStateStore mirrors runner's unexported stateStore so main.go can pass
// either implementation to agentRunner.SetStore.
type runnerStateStore interface {
	GetState(ctx context.Context, key string) (string, error)
	SetState(ctx context.Context, key, value string) error
	GetSession(ctx context.Context, folder string, thread int, provider string) (string, bool, error)
	PutSession(ctx context.Context, folder string, thread int, provider, id string) error
	DeleteSession(ctx context.Context, folder string, thread int, provider string) error
}

// durableSessionStore satisfies runnerStateStore in durable mode.
type durableSessionStore struct{ st *store.Store }

func newSessionStateStore(ctx context.Context, st *store.Store, log *slog.Logger) (runnerStateStore, string /* mode */, error)
```

Semantics:
- `GetState`/`SetState` delegate to `st` unchanged (the legacy adoption marker in `runner/session.go` keeps working).
- `GetSession(folder, thread, provider)`: `scope := st.SnapshotScope(folder)` → `b := st.ResolveConversation(scope, thread)` → `st.GetNativeContext(b.ActiveSessionID, b.ContextEpoch, provider)`. `ErrConversationRetired` and `ErrStaleScope` are returned wrapped, never converted to "no session".
- `PutSession` → `PutNativeContext(scope, b.ActiveSessionID, b.ContextEpoch, provider, id)`.
- `DeleteSession` → `DeleteNativeContext(...)` for that provider and current epoch only.
- `newSessionStateStore` reads `st.SessionMappingMode`; returns `st` itself for `legacy`, `durableSessionStore{st}` for `durable`, and logs the chosen mode once at Info.

- [ ] **Step 1: Write failing tests** (open a real `store.Store` in a temp dir, register an owner chat the way `cmd/gopod/startup_test.go` does):

```go
func TestDurableAdapterRoundTripsThroughBinding(t *testing.T)          // Put then Get same id; GetNativeContext via ResolveConversation sees it
func TestDurableAdapterDeleteRemovesOnlyProviderContext(t *testing.T)  // put claude+codex; Delete claude; Get codex still ok; ResolveConversation same SessionID
func TestDurableAdapterRejectsUnregisteredFolder(t *testing.T)         // Get for unknown folder -> errors.Is(err, store.ErrStaleScope)
func TestNewSessionStateStoreSelectsByMarker(t *testing.T)             // legacy -> *store.Store; after AdoptLegacySessions -> durableSessionStore
```

- [ ] **Step 2: Run `go test ./cmd/gopod -run 'DurableAdapter|NewSessionStateStore' -v`** — expected: FAIL (undefined).

- [ ] **Step 3: Implement `session_adapter.go`; in `main.go` replace `agentRunner.SetStore(st)` with the selected adapter.** `newSessionStateStore` runs after store open and after Task 4's startup adoption hook (so a freshly adopted DB starts durable in the same boot).

- [ ] **Step 4: Run `go test ./cmd/gopod` and `go build ./...`** — expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/gopod/session_adapter.go cmd/gopod/session_adapter_test.go cmd/gopod/main.go
git commit -m "gopod: resolve agent sessions through durable bindings when adopted"
```

---

### Task 3: Attempt binding and execution claims around queue batches

**Files:**
- Create: `cmd/gopod/session_binding.go`
- Test: `cmd/gopod/session_binding_test.go`
- Modify: `cmd/gopod/main.go` — wrap `tgBot.NewAgentHandler()` passed to `queue.New` (line ~658) with `bindingHandler` in durable mode.
- Modify: `internal/queue/queue_test.go` (or `drain_test.go`) — add `TestBatchesNeverSpanThreads` if no equivalent exists (A02).

**Interfaces:**
- Consumes: Task 1 API; `queue.Handler`, `queue.Item`, `queue.ErrStopped`; `runner.Runner.ProviderForChat(folder).Name()`, `runner.Runner.ModelForChat(folder)`; `runner.ErrExecutionUnproven`.
- Produces:

```go
type sessionBinder interface {
	SnapshotScope(ctx context.Context, folder string) (store.ScopeRef, error)
	ResolveConversation(ctx context.Context, scope store.ScopeRef, thread int) (store.ConversationBinding, error)
	BindAttempt(ctx context.Context, scope store.ScopeRef, b store.AttemptBinding) error
	GetAttemptBinding(ctx context.Context, turnID int64) (store.AttemptBinding, bool, error)
	AcquireExecutionClaim(ctx context.Context, scope store.ScopeRef, sessionID string, turnID int64) error
	SettleExecutionClaim(ctx context.Context, sessionID string, turnID int64, unknown bool) error
}

var ErrContextEpochChanged = errors.New("gopod: turn bound to a superseded context epoch")

func bindingHandler(st sessionBinder, provider func(folder string) string, model func(folder string) string, next queue.Handler, log *slog.Logger) queue.Handler
```

Semantics of the returned handler, per batch (`items` share Folder and ThreadID by `sameBatch`):
1. Managed items (`items[0].Managed()`) pass straight to `next` (CP-004 ledger owns them).
2. `scope := SnapshotScope(folder)`; `b := ResolveConversation(scope, thread)`; any error → return it wrapped (the queue retries/backs off as today; `ErrStaleScope` is not retried forever because the queue's max retries apply).
3. For each item: `GetAttemptBinding(item.ID)`; if bound and `ContextEpoch != b.ContextEpoch` or `SessionID != b.ActiveSessionID` → return `ErrContextEpochChanged` wrapped so the queue finishes the batch as failed with that text (no run). Otherwise `BindAttempt` with `Provider`, `Model`, `ContextEpoch = b.ContextEpoch`.
4. `AcquireExecutionClaim(scope, b.ActiveSessionID, items[len(items)-1].ID)`; `ErrSessionBusy` → return wrapped (retry later).
5. `err := next(ctx, items)`; then `SettleExecutionClaim(sessionID, turnID, unknown = errors.Is(err, runner.ErrExecutionUnproven))` using `context.WithoutCancel(ctx)`; return `err`.

- [ ] **Step 1: Write failing tests** with a fake `sessionBinder` recording calls:

```go
func TestBindingHandlerBindsEveryItemAndClaimsLast(t *testing.T)     // 3 items -> 3 BindAttempt with epoch from binding, 1 Acquire for last ID, next called, Settle(unknown=false)
func TestBindingHandlerRefusesStaleEpoch(t *testing.T)               // GetAttemptBinding returns epoch 1, binding epoch 2 -> next not called, errors.Is(err, ErrContextEpochChanged)
func TestBindingHandlerSettlesUnknownOnUnprovenExit(t *testing.T)    // next returns runner.ErrExecutionUnproven -> Settle(unknown=true)
func TestBindingHandlerPassesManagedThrough(t *testing.T)            // AttemptID set -> no binder calls
func TestBindingHandlerRetriesOnBusyClaim(t *testing.T)              // Acquire -> ErrSessionBusy -> next not called, error returned
```

- [ ] **Step 2: Run `go test ./cmd/gopod -run BindingHandler -v`** — expected: FAIL.

- [ ] **Step 3: Implement `session_binding.go`; wire in `main.go` only when the mode from Task 2 is `durable`.**

- [ ] **Step 4: Add `TestBatchesNeverSpanThreads` in `internal/queue`** if `sameBatch` has no test covering two threads: enqueue thread 0, thread 5, thread 0 for one folder with a handler that records `len(items)` and `ThreadID`; assert three batches of one. Skip this step only if an equivalent assertion already exists (grep `sameBatch` in `internal/queue/*_test.go`).

- [ ] **Step 5: Run `go test ./cmd/gopod ./internal/queue` and `go test -race ./cmd/gopod ./internal/queue`** — expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/gopod/session_binding.go cmd/gopod/session_binding_test.go cmd/gopod/main.go internal/queue
git commit -m "gopod: bind queue batches to session context epochs and execution claims"
```

---

### Task 4: `/sessions` owner command and startup adoption

**Files:**
- Create: `internal/control/sessions.go`, test `internal/control/sessions_test.go`
- Create: `cmd/gopod/session_adoption.go`, test `cmd/gopod/session_adoption_test.go`
- Modify: `cmd/gopod/main.go` — call `adoptSessionsIfRequested` right after `store.Open` succeeds (before any runner/queue wiring); register the command next to `control.RegisterTriggerCommand`.

**Interfaces:**
- Consumes: Task 1 `SessionMappingMode`, `RequestSessionAdoption`, `AdoptLegacySessions`, `ErrAdoptionBusy`; `control.Router.Register`, `control.Confirm`, `PermOwnerOnly`.
- Produces:

```go
type SessionsDeps struct {
	Mode    func(ctx context.Context) (mode string, requested bool, err error)
	Request func(ctx context.Context) error
	Count   func(ctx context.Context) (sessions, contexts int, err error) // Task 1 adds (s *Store) CountSessionRows
}
func RegisterSessionsCommand(r *Router, d SessionsDeps)                 // "sessions", PermOwnerOnly

func adoptSessionsIfRequested(ctx context.Context, st *store.Store, log *slog.Logger) error
```

Semantics:
- `/sessions` and `/sessions status`: text `Session mapping: <mode>. Adoption requested: yes|no. Durable sessions: N, native contexts: M.`
- `/sessions adopt`: without `Confirmed` → `Confirm(cmd, "Adopt legacy sessions into durable identity on the next restart? The current mappings are copied once; after that the old table is no longer read. Requires a restart to take effect.")`; with it → `Request` then text `Adoption requested. Run /restart to apply.`; if mode already durable → `Already adopted.` with Code 1.
- Any other argument → `Use /sessions [status|adopt].`, Code 1. Picker convention: bare `/sessions` shows status plus one button row `Adopt` (re-dispatches `sessions adopt`) when mode is legacy and not yet requested.
- `adoptSessionsIfRequested`: `mode, requested := st.SessionMappingMode`; if `requested && mode == legacy` → `st.AdoptLegacySessions`; `ErrAdoptionBusy` is logged at Warn and **not** fatal (startup continues in legacy; the request stays); other errors are fatal (`fmt.Errorf("session adoption: %w", err)`); success logs the report at Info.
- Add to Task 1's store: `func (s *Store) CountSessionRows(ctx context.Context) (sessions, contexts int, err error)`.

- [ ] **Step 1: Write failing control tests** (pattern: `internal/control/restart_test.go`):

```go
func TestSessionsStatusShowsModeAndCounts(t *testing.T)
func TestSessionsAdoptAsksForConfirmation(t *testing.T)      // Code == CodeConfirm, Yes button re-dispatches "sessions adopt"
func TestSessionsAdoptRequestsOnConfirm(t *testing.T)
func TestSessionsAdoptRefusedWhenDurable(t *testing.T)
func TestSessionsIsOwnerOnly(t *testing.T)                   // Router denies a non-owner caller before the handler runs
```

- [ ] **Step 2: Run `go test ./internal/control -run Sessions -v`** — FAIL.
- [ ] **Step 3: Implement `internal/control/sessions.go`.**
- [ ] **Step 4: Run the tests** — PASS.
- [ ] **Step 5: Write failing startup tests** in `cmd/gopod/session_adoption_test.go`:

```go
func TestAdoptSessionsIfRequestedNoopWithoutRequest(t *testing.T)
func TestAdoptSessionsIfRequestedFlipsMode(t *testing.T)         // request + legacy rows -> mode durable, contexts copied
func TestSessionsAdoptRefusedWhileBusy(t *testing.T)             // pending turn -> returns nil, mode legacy, request retained
```

- [ ] **Step 6: Run `go test ./cmd/gopod -run AdoptSessions -v`** — FAIL.
- [ ] **Step 7: Implement `session_adoption.go`; wire both into `main.go`.**
- [ ] **Step 8: Run `go build ./... && go vet ./... && go test ./cmd/gopod ./internal/control`** — PASS.
- [ ] **Step 9: Commit**

```bash
git add internal/control/sessions.go internal/control/sessions_test.go cmd/gopod/session_adoption.go cmd/gopod/session_adoption_test.go cmd/gopod/main.go internal/store
git commit -m "control: add owner /sessions status|adopt with startup adoption"
```

---

### Task 5: Documentation and acceptance mapping

**Files:**
- Modify: `docs/CONTROL.md` §5.3 — add the `/sessions [status|adopt]` row after `/restart`.
- Modify: `docs/DECISIONS.md` — append `D055 — Durable session identity behind an atomic adoption marker` (context: A1/A4; decision: tables, scope validation on every write, marker read once at startup, adoption only when quiesced, legacy default, rollback needs explicit reconciliation; consequences: P2 can add `/clear`/`/new` epochs, P4 can use claims; no behavior change until adopted).
- Modify: `docs/ACCEPTANCE.md` — add rows A01, A02, A06, A11 pointing at the tests from Tasks 1–3 and marking "real native resume after adoption on the Pi" as open.
- Modify: `docs/proposals/README.md` CP-037 row — `in-progress (P0 merged, P1 in review)`.
- Modify: `docs/HANDOFF.md`, `ROADMAP.md` — P1 status (run `/handoff` procedure at the end of the branch).

- [ ] **Step 1: Write the doc changes.**
- [ ] **Step 2: Run `git diff --check`** — clean.
- [ ] **Step 3: Commit**

```bash
git add docs ROADMAP.md
git commit -m "docs: record D055 durable session identity and P1 acceptance mapping"
```

---

## Whole-branch gates (before PR)

- `go build ./... && go vet ./... && go test ./...`
- `go test -race ./internal/store ./internal/queue ./internal/control ./cmd/gopod ./internal/runner`
- `GOPOD_TEST_SH_IMAGE=redis:7-alpine go test -tags docker_integration -count=1 ./internal/runner -run '^TestIntegration(Stream|Lifecycle)'` (unchanged code path; proves no regression)
- Independent reviewer agent on the diff; fixes; PR; green CI; admin squash merge; retained branch. No Pi deployment.
