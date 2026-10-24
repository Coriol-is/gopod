# Telegram Threads and Native Streaming Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Native Telegram streaming with a Stop button (Phase A), then separate agent contexts per private-chat thread with the no-thread chat staying one shared context (Phase B).

**Architecture:** Phase A swaps the placeholder-plus-edit loop for `sendMessageDraft` and cancels a turn on `MessageGenerationStopped`. Phase B keys a conversation by `(chat_folder, thread_id)` (`thread_id` 0 = main): explicit agent session ids stored in `sessions`, `thread_id` carried through queue, turns, memory summaries, commands and every outbound send. Execution stays sequential per chat; the container, workspace and long-term memory stay per chat.

**Tech Stack:** Go 1.26, `github.com/go-telegram/bot` (v1.20.0 → v1.27.0, Bot API 10.3), ncruces SQLite, Docker SDK, `claude` and `codex` CLIs.

**Spec:** `docs/superpowers/specs/2026-10-24-telegram-threads-and-drafts-design.md` (approved by the owner 2026-10-24).

## Global Constraints

- Go only, no CGO; one SQLite file; DB access only through `internal/store`; admin commands only through `internal/control` (CLAUDE.md, D011).
- Secrets only from env vars (D012). A thread id is routing inside an authorized chat, never authorization.
- Commit messages: `<area>: <imperative summary>` with a body that says why. **Never commit unless the owner has allowed commits for this run**; where a step says "Commit", skip it otherwise and leave changes staged.
- Both harnesses (`claude`, `codex`) must keep working. Provider is per chat, never per thread.
- Messages with no thread id behave exactly as today. Facts, pinned and preferences memory stay chat-wide; only the `conversation_summary` slot is per conversation.
- Execution is sequential per chat. Parallel threads are NOT built here (spec: optional research track, Task 12 only gathers evidence).
- Stopped turn: terminal status `stopped`, partial text kept as the reply, never replayed by `Recover`.
- Phase B does not start until Task 4 (the `kill -9` gate) passes.
- Final gate for every task: `go build ./... && go vet ./... && go test ./...` green.

## Review Focus

1. A message in thread 7 arrives while a main-chat turn runs: it waits (per-chat order) and its reply, placeholder and typing action land in thread 7, not main.
2. A message with a thread id in a group chat: handled as main (threads apply to private chats only).
3. Bot with topics disabled in @BotFather: no message has a thread id; behaviour is byte-identical to today.
4. Stop pressed after the turn already finished, or twice: no-op, no error, no state change.
5. `/clear` in a thread that has no session row yet, or `/clear` in main while threads exist: no error, no other conversation's session touched.

## File Structure

- Modify `go.mod`, `go.sum`: library bump.
- Create `internal/telegram/draft.go`: `sendDraft`, unsupported-detection, per-chat sticky fallback.
- Modify `internal/telegram/streaming.go`: use drafts, fall back to edits.
- Create `internal/telegram/stop.go`: `MessageGenerationStopped` handling and in-flight cancel registry.
- Modify `internal/queue/queue.go`, `internal/store/turns.go`, `internal/store/schema.go`: `stopped` status, `ThreadID`.
- Create `internal/store/sessions.go`: per-conversation session ids.
- Modify `internal/runner/provider*.go`, `runner.go`, `compact.go`: explicit session ids, per-conversation clear.
- Modify `internal/memory/compiler.go`, `memory.go`: per-conversation summary.
- Modify `internal/telegram/{handler,streaming,voice,media,ipc_sink,login,commands}.go`, `internal/control/*`, `cmd/gopod/main.go`: thread plumbing.
- Create `docs/DECISIONS.md` entry D021; update `docs/HANDOFF.md`, `ROADMAP.md`, `README.md`.

---

## Phase A

### Task 1: Bump go-telegram/bot to v1.27.0

**Files:** Modify `go.mod`, `go.sum`.

**Interfaces:** Produces: access to `bot.SendMessageDraftParams`, `models.MessageGenerationStopped` (or the library's equivalent names; the implementer confirms them in the v1.27.0 source and records the real names in the Task 2/3 notes).

- [ ] **Step 1:** Run `go get github.com/go-telegram/bot@v1.27.0 && go mod tidy`.
- [ ] **Step 2:** Run `go build ./... && go vet ./... && go test ./...`. Expected: PASS with no code changes; if a signature moved, adapt only the call sites that fail to compile.
- [ ] **Step 3:** Commit `telegram: bump go-telegram/bot to v1.27.0 (Bot API 10.3)`.

### Task 2: Stream through sendMessageDraft

**Files:** Create `internal/telegram/draft.go`, `internal/telegram/draft_test.go`; modify `internal/telegram/streaming.go`.

**Interfaces:**
- Produces: `func (b *Bot) sendDraft(ctx context.Context, chatID int64, threadID int, draftID int, text string) error`; `func (b *Bot) draftsSupported(chatID int64) bool`; `func (b *Bot) markDraftsUnsupported(chatID int64)` (sticky until restart). `threadID` is 0 until Phase B.
- Consumes: existing `reusePlaceholder`, `recordPlaceholder`, `recordReply`, `markdownToTelegramHTML`, `GOPOD_STREAM_DISABLED`.

- [ ] **Step 1: Read the `sendMessageDraft` method page** at core.telegram.org/bots/api (and the library's params struct). Write the answers as a comment block at the top of `draft.go`: how a draft turns into a real message, what id the final message has, draft lifetime, text length limit, what happens to a draft if gopod dies, whether HTML parse mode is allowed. These decide steps 4-6.
- [ ] **Step 2: Failing test** `TestSendDraft_UnsupportedErrorMarksChatSticky` in `draft_test.go`: with the API stub (same fake used in `handler_test.go`) returning the Bot API "method not supported / not available" error, after one call `draftsSupported(chatID)` is false and a second chat is unaffected; a transient error (HTTP 429 or network) does NOT flip the flag.
- [ ] **Step 3:** Run it; expect FAIL (undefined).
- [ ] **Step 4:** Implement `sendDraft`, `draftsSupported`, `markDraftsUnsupported` in `draft.go` (map + mutex per `Bot`).
- [ ] **Step 5: Failing test** `TestRunAgentStreaming_UsesDraftsThenFinalMessage`: a streamed reply of three chunks produces draft calls with growing text and exactly one final `sendMessage` carrying HTML; `recordReply` receives the final message id. Second test `TestRunAgentStreaming_DraftUnsupportedFallsBackToEdits`: unsupported error on first draft, the rest of the turn uses `sendPlaceholder` + `editMessage` as today and the reply is delivered once.
- [ ] **Step 6:** Rework `runAgentStreaming` in `streaming.go` accordingly: drafts replace the 1s edit ticker (keep the ticker cadence), the final text goes out with `sendFormatted`, durable-turn memoization (`reusePlaceholder`/`recordPlaceholder`/`recordReply`) keeps its D020 meaning (a resumed turn after a crash sends a fresh final message; no second reply when the reply id is already recorded). Overflow beyond the draft/message limit keeps today's behaviour.
- [ ] **Step 7:** Run `go test ./internal/telegram/...`; expect PASS. Commit `telegram: stream replies via sendMessageDraft with edit fallback`.

### Task 3: Stop button

**Files:** Create `internal/telegram/stop.go`, `internal/telegram/stop_test.go`; modify `internal/queue/queue.go`, `internal/store/turns.go`, `internal/telegram/streaming.go`, `internal/telegram/bot.go` (update routing).

**Interfaces:**
- Produces: `const queue.StatusStopped = "stopped"` and `store.TurnStopped = "stopped"`; `func (b *Bot) registerInflight(turnID int64, cancel context.CancelFunc) (unregister func())`; `func (b *Bot) handleGenerationStopped(ctx context.Context, upd *models.Update)`.
- Consumes: `Item.ID` (turn id), the draft call from Task 2 (`can_stop` and `keep_on_stop` set on the draft).

- [ ] **Step 1: Failing tests** in `stop_test.go`: `TestStopCancelsOnlyMatchingTurn` (two registered turns, stop for one cancels one); `TestStopAfterFinishIsNoop` (unregistered turn id, no panic, no state change); `TestStopTwiceIsNoop`.
- [ ] **Step 2:** Run; expect FAIL.
- [ ] **Step 3:** Implement the registry in `stop.go` keyed by (chat id, draft id/turn id as the update provides; the implementer confirms what `MessageGenerationStopped` carries). Pass `can_stop=true`, `keep_on_stop=true` in `sendDraft`.
- [ ] **Step 4: Failing test** in `internal/store/turns_test.go`: `TestLoadUnfinishedSkipsStopped` (a `stopped` row is not returned by `ListUnfinishedTurns`) and `TestQueueRecoverIgnoresStopped` in `queue_test.go`.
- [ ] **Step 5:** Add the `stopped` status through `MarkFinished`; `runAgentStreaming` on a stop cancels the agent context, delivers the partial text as the final reply, marks the turn `stopped`. Whether the agent session stays resumable after a killed turn is checked manually in Task 4 and recorded in D021.
- [ ] **Step 6:** Run `go test ./...`; PASS. Commit `telegram: stop button cancels the running turn`.

### Task 4: Gate — crash recovery with drafts (manual)

**Files:** Modify `docs/HANDOFF.md`.

- [ ] **Step 1:** Deploy the branch to the production host as in HANDOFF; send a slow-turn prompt; `docker kill -s KILL gopod-gopod-1` during streaming; `docker compose up -d`.
- [ ] **Step 2:** Verify: exactly one final reply, the agent mentions the interruption, the `turns` row ends `done` with `reply_msg_id` set, no leftover visible draft. Also verify Stop: press Stop, partial text stays, row is `stopped`, next message continues the session (record whether the interrupted turn is present in history).
- [ ] **Step 3:** Record results in HANDOFF. **If duplicate replies appear, stop and fix Task 2 before Phase B.**

---

## Phase B

Tasks 5, 6, 7 and 8 touch separate packages and can run in parallel after the gate. Tasks 9-11 depend on them.

### Task 5: Per-conversation sessions in the store

**Files:** Create `internal/store/sessions.go`, `internal/store/sessions_test.go`; modify `internal/store/schema.go` (migration).

**Interfaces:**
- Produces: `func (s *Store) GetSession(ctx context.Context, folder string, thread int, provider string) (id string, ok bool, err error)`; `func (s *Store) PutSession(ctx context.Context, folder string, thread int, provider, id string) error`; `func (s *Store) DeleteSession(ctx context.Context, folder string, thread int, provider string) error`.
- Migration: rebuild `sessions` to `PRIMARY KEY (chat_folder, thread_id, provider)` with `session_id`, `updated_at`; add `thread_id INTEGER NOT NULL DEFAULT 0` to `turns`. The implementer first greps for current use of `sessions`; existing rows (if any) become `thread_id=0`, provider `claude`.

- [ ] **Step 1: Failing tests:** `TestSessionsAreKeyedByThreadAndProvider` (main and thread 7 and a second provider hold distinct ids), `TestDeleteSessionOnlyRemovesOne`, `TestMigrationKeepsExistingRowsAsMain` (open a DB created with the old schema, apply schema, old row readable as `thread=0`).
- [ ] **Step 2:** Run; FAIL. **Step 3:** Implement and migrate (idempotent, re-apply is a no-op, like the existing `migrations` runner). **Step 4:** `go test ./internal/store/...`; PASS. Commit `store: key sessions by chat, thread and provider`.

### Task 6: Explicit session ids in providers

**Files:** Modify `internal/runner/provider.go`, `provider_claude.go`, `provider_codex.go`, `provider_*_test.go`.

**Interfaces:**
- Produces: `type SessionRef struct { ID string; Resume bool }` (zero value = legacy implicit resume); `RunCmd(prompt, systemPrompt string, s SessionRef) []string`; `ClearSessionCmd(s SessionRef) []string` (deletes only that session; zero value keeps today's wipe-all for `RunFresh` callers); `LatestSessionIDCmd() []string` (prints the newest session id in the container, used once for migration); `ExtractSessionID(line string) string` (Codex: the thread id from the exec JSON events; Claude returns "").
- Claude: `Resume` → `--resume ID`; ID set and not `Resume` → `--session-id ID`; zero → `--continue`. Codex: ID set → `exec resume ID`; zero → `exec resume --last`; new conversation → plain `exec` and capture the id from events.

- [ ] **Step 1: Failing tests** table-driven per provider: the three `RunCmd` shapes above (exact argv), `ClearSessionCmd` removes only the named session's file, `LatestSessionIDCmd` argv, `ExtractSessionID` on a recorded Codex `thread.started` line (capture a real one with codex 0.160 inside the agent image and store it under `testdata/`).
- [ ] **Step 2:** Run; FAIL. **Step 3:** Implement; fix compile fallout at call sites by passing `SessionRef{}`. **Step 4:** `go test ./internal/runner/...`; PASS. Commit `runner: resume agent sessions by explicit id`.

### Task 7: Per-conversation memory summary

**Files:** Modify `internal/store/schema.go` (migration `ALTER TABLE memories ADD COLUMN thread_id INTEGER NOT NULL DEFAULT 0`), `internal/memory/compiler.go`, `internal/memory/memory.go`, `internal/runner/runner.go` (`MemoryCompiler` interface).

**Interfaces:** Produces: `CompileContext(ctx, chatFolder string, thread int, query string) string`; summary write/read/supersede functions gain a `thread int` argument (non-summary kinds ignore it).

- [ ] **Step 1: Failing tests:** `TestSummaryIsPerConversation` (summary stored for thread 5 never appears in main's compiled context nor the reverse), `TestSupersedeOnlyWithinConversation`, `TestFactsAndPinnedAreSharedAcrossThreads`.
- [ ] **Step 2:** Run; FAIL. **Step 3:** Implement `fetchRecentByKind` and `supersedePreviousSummary` thread filters. **Step 4:** `go test ./internal/memory/...`; PASS. Commit `memory: scope conversation summary per thread`.

### Task 8: Thread id through queue, turns and inbound

**Files:** Modify `internal/queue/queue.go`, `internal/store/turns.go`, `internal/telegram/handler.go`, `resume.go`; tests alongside.

**Interfaces:**
- Produces: `queue.Item.ThreadID int`; `store.Turn.ThreadID int`; `func inboundThreadID(m *models.Message) int` in `handler.go` returning the thread id only for private chats (`m.Chat.Type == private` and the message carries one), else 0.
- The queue still serializes by `Folder`; per-chat order is preserved.

- [ ] **Step 1: Failing tests:** `TestInboundThreadID` (private + thread id → id; group + thread id → 0; no thread → 0), `TestTurnRoundTripKeepsThreadID` (insert, `ListUnfinishedTurns`, `Recover` item has `ThreadID`), `TestQueueOrdersMainAndThreadMessagesPerChat`.
- [ ] **Step 2:** Run; FAIL. **Step 3:** Implement. **Step 4:** `go test ./internal/queue/... ./internal/store/... ./internal/telegram/...`; PASS. Commit `queue: carry thread id through turns and recovery`.

### Task 9: Runner uses the conversation (depends on 5, 6, 7, 8)

**Files:** Modify `internal/runner/runner.go`, `compact.go`, `cmd/gopod/main.go`; tests in `runner_test.go`.

**Interfaces:**
- Produces: `Run(ctx, folder string, thread int, tier, allowlist, prompt)`, `RunStream(...)` likewise, `ClearSession(ctx, folder string, thread int, tier, allowlist) error`, `CompactSession(...)` likewise; turn counters keyed by `(folder, thread)`.
- Behaviour: resolve the session via `GetSession` for the chat's provider. Thread with no row: generate a UUID (Claude, `SessionRef{ID, Resume:false}`) or run plain `exec` and capture the id with `ExtractSessionID` (Codex), then `PutSession`; later turns `Resume`. Main with no row: run `LatestSessionIDCmd` once, store it (migration), then resume by id; if the container has no session yet, behave as a new conversation. `ClearSession` deletes only that conversation's session and row, never `rm -rf` of projects. `/provider` still switches the chat's provider; sessions are looked up per provider.

- [ ] **Step 1: Failing tests** with the existing fake Docker used in `runner` tests: `TestMainAdoptsLatestSessionOnce`, `TestThreadGetsOwnSessionAndMainIsUntouched`, `TestClearThreadDoesNotTouchMain`, `TestClearMainDoesNotTouchThreads`, `TestClearWithoutRowIsNoop`, `TestProviderSwitchDoesNotResumeForeignID`.
- [ ] **Step 2:** Run; FAIL. **Step 3:** Implement; adapt `main.go` closures (`SetCompactFn`, clear/compact wiring) to the new arguments. **Step 4:** `go test ./...`; PASS. Commit `runner: one agent session per conversation`.

### Task 10: Send paths and commands in the thread (depends on 8, 9)

**Files:** Modify `internal/telegram/{handler,streaming,draft,voice,media,ipc_sink,login,commands}.go`, `internal/control/{router,handlers}.go` (+ tests), `cmd/gopod/main.go`.

**Interfaces:**
- Produces: `thread int` on `replyTo`, `replyText`, `sendFormatted`, `sendPlaceholder`, `sendDraft`, `startTyping`, `sendVoiceReply`, photo/document sends; `control.Request` (or the existing request context) gains `ThreadID int`; `CompactFunc` becomes `func(ctx, folder string, thread int, clear bool) (string, error)`.
- Every outbound send sets `MessageThreadID` when `thread != 0`; `/clear` and `/compact` receive the thread from the update; `/model`, `/provider`, `/login`, `/whoami`, `/help` reply in the thread they were sent in but keep per-chat effect.

- [ ] **Step 1: Failing tests:** `TestRepliesCarryThreadID` (stub API records `MessageThreadID` on reply, placeholder, draft, voice, typing action, error and login messages), `TestMainRepliesCarryNoThreadID`, `TestClearInThreadCallsClearWithThread`, `TestCommandReplyGoesToSameThread`.
- [ ] **Step 2:** Run; FAIL. **Step 3:** Implement. IPC and scheduler-originated messages stay in main (spec); the IPC sink passes `thread=0`. **Step 4:** `go test ./...`; PASS. Commit `telegram: answer in the thread the message came from`.

### Task 11: Live verification and docs

**Files:** Modify `docs/DECISIONS.md` (D021), `docs/HANDOFF.md`, `ROADMAP.md`, `README.md`.

- [ ] **Step 1:** In @BotFather enable topics for the bot (record the exact setting name in README). On the production host: main chat plus two threads hold three distinct contexts across an agent-container recycle (`docker rm -f gopod-owner`) and a gopod restart; `/clear` in a thread leaves main intact; `kill -9` mid-turn in a thread resumes in that thread without a duplicate; run once with each of `claude` and `codex`.
- [ ] **Step 2:** Write D021 (conversation key, explicit session ids, per-chat provider, per-conversation summary, sequential execution, Stop semantics including the resumability result from Task 4); update HANDOFF, ROADMAP, README; mark parallel threads "needs research" in ROADMAP.
- [ ] **Step 3:** Commit `docs: D021 conversation threads and native streaming`.

### Task 12: Concurrency spike (research, non-blocking)

**Files:** Create `internal/runner/concurrency_integration_test.go` (build tag `docker_integration`), `docs/proposals/` note only if the owner wants a CP.

- [ ] **Step 1:** Integration test: in one container, two simultaneous `claude -p` runs with different session ids, then two `codex exec` runs; repeat 20 times; assert both replies arrive and `~/.claude.json` / `~/.codex` stay valid JSON.
- [ ] **Step 2:** Record pass/fail in HANDOFF under "Nearby work". This only informs the optional parallel-threads item; do not change the queue.

---

## Self-review

- **Spec coverage:** Phase A → Tasks 1-4; session ids/migration → 5, 6, 9; per-conversation summary → 7; queue/turns/thread inbound → 8; send paths/commands → 10; provider per chat → 9-10; docs/ADR/BotFather → 11; research track → 12; anti-goals covered by Review Focus and tests in 7, 9.
- **Types:** `SessionRef`, `thread int`, `ThreadID`, `StatusStopped` used consistently across Tasks 2-10.
- **Proportion:** plan is signature- and test-level; bodies are left to the implementer.
