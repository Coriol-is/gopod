# M7 — IPC design

Container → host backchannel for the gopod agent, scoped per chat,
filesystem-mediated. Lets the agent post Telegram messages and schedule
tasks without a network channel into the host.

Status: design approved 2026-05-07. Implementation not started.
Reference impl: NanoClaw `src/ipc.ts` (cherry-picked, not ported).

## 1. Architecture

Single goroutine on the host watches per-chat IPC dirs and dispatches
JSON files to typed sinks.

```
host: gopod process

  ipc.Watcher (single goroutine, 1s tick)
    ├─ walk data/ipc/<chat>/messages
    └─ walk data/ipc/<chat>/tasks
                │
        ┌───────┴───────┐
        ▼               ▼
   MessageSink       TaskSink
   (interface)       (interface)
        │               │
        ▼               ▼
   telegram.Bot.Send   scheduler.Schedule / .Cancel
```

```
data/ipc/<chat_folder>/
  messages/    *.json   {chatJid, text}     agent → host
  tasks/       *.json   {op, ...}            agent → host
  input/                                     reserved; no v1 producer
  .processed/  *.json   successful files; cap 200/dir
  .failed/     *.json   exhausted retries / permanent errs; cap 200/dir
```

Mounted into the container at `/workspace/ipc`. Mount + per-chat host
dir scaffolding already exist in `internal/runner/mounts.go`; this
milestone adds the watcher loop + dispatchers + retention.

### Authorization is path-based

The chat folder owning the IPC file (the parent directory under
`data/ipc/`) is the only trusted identity. JSON content is **never**
trusted to claim source identity. Owner chat (`GOPOD_OWNER_CHAT_ID`'s
folder, default `owner`) is the only chat allowed to target other chats.

| Source folder | Target chatJid / chatFolder      | Allowed |
|---------------|----------------------------------|---------|
| any           | matches own folder's registered JID | yes     |
| `owner`       | any registered chat              | yes     |
| non-owner     | different folder / different JID | no → `.failed/<f>.unauthorized` |
| any           | unregistered folder              | no → `.failed/` |

Folder-to-JID resolution comes from `internal/store.RegisteredChats`
(one JID per folder). The "owner" folder name is derived from the chat
that the operator registered against `GOPOD_OWNER_CHAT_ID`; spec uses
"owner" as the conventional name, but the actual string comes from
`Config.OwnerFolder`.

### Crash recovery

Files in `messages/` and `tasks/` are the queue. The next tick after
restart picks them up; rename to `.processed/` is the commit point. No
separate replay path. Re-delivery after a crash mid-tick is possible
(at-least-once); accepted.

## 2. Components

```
internal/ipc/
├── ipc.go         package doc + public types
├── watcher.go     Watcher{} + Run loop + tickOnce
├── messages.go    messages/*.json parse + dispatch
├── tasks.go       tasks/*.json parse + dispatch (schedule/cancel)
├── retention.go   .processed/.failed cap (200/dir)
├── authz.go       sourceFolder + cross-chat checks
├── errors.go      ErrPermanent sentinel
└── *_test.go      table-driven tests with fakes (t.TempDir)
```

### Public API

```go
package ipc

type MessageSink interface {
    Send(ctx context.Context, chatJID, text string) error
}

type TaskSink interface {
    Schedule(ctx context.Context, t store.TaskRecord) error
    Cancel(ctx context.Context, taskID string) error
}

type Config struct {
    DataDir      string          // gopod data dir; ipc lives at <dir>/ipc
    OwnerFolder  string          // e.g. "owner"
    Tick         time.Duration   // default 1s
    MaxRetries   int             // default 3
    Retention    int             // default 200 per .processed/.failed dir
    RetryBackoff []time.Duration // default [5s, 15s, 45s]
    NowFn        func() time.Time // testing seam; default time.Now
}

type Watcher struct { /* unexported */ }

func New(cfg Config,
         msgs MessageSink,
         tasks TaskSink,
         log *slog.Logger,
         resolveFolderJID func(folder string) (jid string, registered bool),
        ) *Watcher

func (w *Watcher) Run(ctx context.Context) error // blocks until ctx done
```

### Wiring (in `cmd/gopod/main.go`)

```go
ipcWatcher := ipc.New(
    ipc.Config{ DataDir: cfg.DataDir, OwnerFolder: cfg.OwnerFolder },
    telegramSink{bot},
    schedulerSink{sched},
    log,
    store.LookupChatJID,
)
go ipcWatcher.Run(ctx)
```

Two thin adapter files:

- `internal/telegram/ipc_sink.go` — `func (b *Bot) Send(ctx, jid, text)
  error` (or wraps an existing helper).
- `internal/scheduler/ipc_sink.go` — wraps `Scheduler.Add` / adds
  `Scheduler.Cancel(taskID)` if missing.

## 3. Payload schemas

### messages/

```json
{
  "chatJid": "<destination chat jid>",
  "text":    "<message body>"
}
```

### tasks/ — schedule

```json
{
  "op":               "schedule",
  "taskId":           "task-abc123",
  "prompt":           "remind me to ...",
  "scheduleType":     "cron|interval|once",
  "scheduleValue":    "0 9 * * *",
  "targetChatFolder": "owner"
}
```

- `taskId` optional; if absent, host generates `task-<unix_ms>-<rand6>`.
- `targetChatFolder` optional; defaults to source folder. If different
  from source, source must be `owner`.

### tasks/ — cancel

```json
{ "op": "cancel", "taskId": "task-abc123" }
```

Only the source folder owner of the target task (or `owner`) may cancel.
Unknown task id → `.failed/`.

### Naming

Agent writes `<unix_ms>-<rand6>.json`. Leading timestamp gives FIFO
order under `os.ReadDir` and lets retention drop oldest by suffix sort.

## 4. Data flow

### messages/

1. agent writes `/workspace/ipc/messages/<ts>-<rand>.json`
2. watcher tick reads `data/ipc/<source>/messages`, sorted ascending
3. parse JSON → on parse err: `.failed/`, log, continue
4. authz check (table above); fail → `.failed/<f>.unauthorized`
5. `MessageSink.Send(ctx, chatJid, text)`
   - ok → `os.Rename → .processed/`
   - transient err → bump retry counter; backoff `[5s, 15s, 45s]`
   - permanent err (`errors.Is(err, ipc.ErrPermanent)`) → `.failed/` now
   - 3rd transient fail → `.failed/`
6. retention sweep on `.processed/` and `.failed/` per dir → keep 200
   newest by name suffix.

### tasks/

Same pipeline; dispatcher picks `Schedule` or `Cancel` by `op`. Schema
validation errors and `scheduler.ValidateCron` errors are wrapped with
`ipc.ErrPermanent`.

### Retry state

Watcher holds in-memory `map[string]retryEntry` keyed by absolute file
path: `{attempts int, nextRetryAt time.Time}`. Files whose
`nextRetryAt` is in the future are skipped this tick. Retry state is
not persisted — after a process restart, all leftover files retry from
scratch (no `attempts` carried over). This is acceptable: at-least-once
delivery + idempotent failure means a duplicate Telegram message after
a crash is the worst case.

## 5. Error handling

| Class | Example | Action |
|---|---|---|
| Permanent payload | malformed JSON, unknown `op`, missing field | `.failed/` immediately, `level=warn` |
| Authz violation | non-owner targets other chat, unregistered folder | `.failed/<f>.unauthorized` immediately, `level=warn` |
| Transient sink | telegram 5xx/timeout, scheduler DB lock | retry `[5s,15s,45s]`; 3rd fail → `.failed/`, `level=error` |
| Permanent sink | bad cron, target chat not registered | `.failed/` after first attempt, `level=warn` |

```go
// internal/ipc/errors.go
var ErrPermanent = errors.New("ipc: permanent dispatch error")
// Sinks wrap permanent dispatch errors:
//   fmt.Errorf("validate cron %q: %w: %w", v, err, ipc.ErrPermanent)
// Watcher tests with errors.Is.
```

### Watcher must not crash

Every per-file step runs inside `func(){ defer recover() ... }()`. A
recovered panic logs at `level=error`; the file is moved to
`.failed/<f>.panic`. The loop continues. `os.MkdirAll` on `.processed/`
and `.failed/` happens on every tick (cheap; idempotent) so renames
never fail for "no such directory."

### Logging

Every line carries `chat_folder`, `channel`, `file`. Retry lines also
carry `attempt` and `next_retry_at`. The existing slog redactor strips
token-ish fields from any logged JSON.

### Out of scope for v1

- No persistent retry queue (files are the queue).
- No deduplication (caller responsibility).
- No global lock; per-tick processing is sequential per file.
- No Prom/OTel metrics — `M3.6` covers that.

## 6. Reserved channels (no v1 producer)

### input/

`data/ipc/<chat>/input/` is a mounted writable directory the container
can read from but the host does not write to in v1. Telegram polling
already feeds user follow-ups via the message store on the next agent
turn, so no host-side writer is needed yet. The directory and JSON
shape are documented so a future caller (e.g. an `/inject` slash
command) can land without an ADR.

### `_close` removed

The original NanoClaw `_close` sentinel signaled a long-lived in-container
agent loop to exit. Gopod uses one-shot `docker exec` per turn and the
30-minute idle watcher for teardown, so nothing inside the container
polls for `_close`. Dropped from the spec.

## 7. Testing

All unit tests use `t.TempDir()` + fake sinks; no docker, no real
network, no real scheduler. The watcher exposes an unexported
`tickOnce(ctx)` so tests advance the loop without `time.Sleep`. Backoff
uses `cfg.NowFn` so a fake clock drives retry timing.

### Test list

- `TestWatcher_MessageHappyPath`
- `TestWatcher_TaskScheduleHappyPath` (incl. generated taskId)
- `TestWatcher_TaskCancelHappyPath`
- `TestWatcher_MalformedJSON`
- `TestWatcher_UnknownOp`
- `TestWatcher_AuthzNonOwnerCrossChat`
- `TestWatcher_AuthzOwnerCrossChat`
- `TestWatcher_AuthzUnregisteredTargetFolder`
- `TestWatcher_TransientRetry` (2 fails then ok across simulated ticks)
- `TestWatcher_TransientExhausted` (3 fails → `.failed/`)
- `TestWatcher_PermanentSinkError` (one attempt → `.failed/`)
- `TestWatcher_RetentionCap` (250 → 200)
- `TestWatcher_PanicRecovered` (sink panics, watcher survives, next file ok)
- `TestWatcher_RestartPicksUpLeftover` (no special replay path)
- `TestWatcher_FIFOOrder`
- `TestWatcher_MkdirsOnBoot`

### One integration test

`TestIntegration_IPCRoundtrip` (build tag `integration`): full gopod
w/ in-memory store + fake bot — drops a real file under
`data/ipc/owner/messages/`, asserts the bot's `Send` is called. No
docker, no network. Mirrors the existing `docker_integration_test.go`
pattern.

## 8. Sub-step rollout

| Step | Scope |
|---|---|
| **I1** | `internal/ipc/` package + `Watcher.Run` loop + `tickOnce` + retention. No real dispatch yet — fakes only. |
| **I2** | `messages.go` + `MessageSink` adapter on `telegram.Bot`. Watcher delivers. |
| **I3** | `tasks.go` + `TaskSink` adapter on `scheduler.Scheduler`. Watcher schedules + cancels. Add `Scheduler.Cancel` if missing. |
| **I4** | Reserved-dir scaffolding (`input/`) — no host writer, just docs + dir creation in chat bootstrap. |
| **I5** | Integration test, wire watcher into `cmd/gopod/main.go`, update `docs/HANDOFF.md`. |

Each step ships a green test suite. I1–I3 are independent enough that
one engineer (or one Claude session) can land them in order.

## 9. Decisions log (during brainstorm 2026-05-07)

| # | Question | Pick |
|---|---|---|
| Q1 | Scope cut | Full M7 (modulo Q4/Q5 trims) |
| Q2 | Tasks ops | `schedule` + `cancel` (no `pause/resume`) |
| Q3 | Failure model | 3 retries `[5s,15s,45s]` → `.failed/`; retention 200/dir |
| Q4 | `input/` host writer in v1 | No — reserve dir + schema only |
| Q5 | `_close` semantics | Drop from spec entirely |
| Q6 | Watcher cadence | 1s ticker (single goroutine) |
| Q7 | Sink wiring | Consumer-side interfaces on `internal/ipc` |

## 10. Out of scope / future ADRs

- Persistent retry state across restarts.
- Per-chat rate limiting on `messages/` (defer until abuse observed).
- fsnotify-based watcher (defer until 1s tick is shown to be too slow).
- `input/` host-side writer (`/inject` slash command, etc.).
- A `_close` analogue if/when long-lived in-container agent loops land.
- Metrics / tracing — covered by M3.6.
