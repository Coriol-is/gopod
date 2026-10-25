# Dynamic Telegram UX Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Inline buttons and callbacks in gopod's Telegram frontend (Stop on every path, Regenerate/Copy/Retry, pickers, confirmations, paging), a visible turn state (status reactions, elapsed time, queue position), richer input context, and rich-message final replies behind a flag.

**Architecture:** One callback dispatcher in `internal/telegram/callback.go` routes `act:args` data either to turn actions in the telegram package or to `control.Command`s dispatched through the Router, so D011 permissions keep applying. `control.Response` gains frontend-agnostic `Buttons`. Status UX hooks into the existing streaming loop and reaction helper; nothing in the D021 draft/edit policy changes.

**Tech Stack:** Go 1.26, `github.com/go-telegram/bot` v1.27.0 (Bot API 10.3), ncruces SQLite, existing queue/turns/runner.

**Spec:** `docs/superpowers/specs/2026-10-25-telegram-dynamic-ux-design.md` (status: proposed; owner questions Q1-Q10 open). Phases marked **(Qn)** must not start before the owner answered Qn.

## Global Constraints

- Go only, no CGO; DB only through `internal/store`; admin actions only through `internal/control` (CLAUDE.md, D011). A button that changes settings dispatches a `control.Command`; the Router checks `Perm`.
- Authorization from the chat of the message carrying the button (`CallbackQuery.Message`), never from `callback_data` (spec anti-goals). Callback data holds ids only, ≤ 64 bytes, asserted in tests.
- Every callback is answered (`AnswerCallbackQuery`), including on error and panic.
- Every send and edit carries the thread of the message it answers (D022).
- D020: the turn's reply id stays the text message; keyboards are attached after `recordReply`; a failed keyboard edit is logged, never retried by the queue.
- D024 redactor on every status string and toast; no model output in headers.
- Commit messages `<area>: <imperative summary>` with a why-body. **Never commit unless the owner allowed commits for this run.** PR-based flow (CLAUDE.md).
- Gate for every task: `go build ./... && go vet ./... && go test ./...` green.

## Prerequisites

- PR #22 (D024, `feat/coding-progress-clean`) merged: Phase 3 reads its `Progress` events and `progress.go`.
- `feat/lifecycle-notices` (D027) merged or explicitly parked: Phase 5 moves user-facing strings and must include its notices.
- HANDOFF live checklist item 00 (drafts, Stop) run on a real bot before Phase 2 ships, so regressions are attributable.

## Review Focus

1. A button pressed in a chat that is not registered, or by a non-owner on an owner-only action: toast, no state change, Router logs the denial.
2. A button on a message older than 48 h (inaccessible message) or after a restart: toast "expired", no panic.
3. Regenerate in topic 7 while main runs: waits per chat order, answers in topic 7.
4. A 20-step tool turn: no 429 from reactions or status edits.
5. Drafts unsupported (group): Stop button on the placeholder works; final edit removes it.

## File Structure

- Create `internal/telegram/callback.go`, `callback_test.go`: codec, dispatcher, answer guarantee.
- Create `internal/telegram/keyboard.go`, `keyboard_test.go`: builders for reply actions, Stop, confirmations, pages.
- Create `internal/telegram/actions.go`, `actions_test.go`: Regenerate, Retry, Steps, As-file turn actions.
- Create `internal/telegram/status.go`, `status_test.go`: reaction state machine and status header.
- Create `internal/telegram/errors.go`, `errors_test.go`: error table, cooldown.
- Create `internal/telegram/strings.go`: user-facing strings (Phase 5).
- Create `internal/telegram/inbound_context.go`, `_test.go`: reply/quote/forward header, albums.
- Create `internal/telegram/rich.go`, `rich_test.go`: rich final replies.
- Modify `internal/telegram/bot.go` (handler registration, allowed updates, menus), `handler.go` (inbound context, albums), `streaming.go` (Stop button, keyboards on final, status header), `reactions.go`, `stop.go`.
- Modify `internal/control/control.go` (`Response.Buttons`), the handlers of `model`, `provider`, `clear`, `logs`, `recall`, `tasks`, `help`; `cmd/gopod` CLI printer for buttons.
- Modify `internal/queue/queue.go`: read-only `Position(folder string, turnID int64) int`.
- Docs: `docs/DECISIONS.md` (D028 callback layer, D029 rich replies), `docs/CONTROL.md` §3 and §6, `docs/HANDOFF.md`, `ROADMAP.md`.

**Shared-file hot spots** (sequence or rebase carefully): `streaming.go` (Tasks 2, 5, 7, 8, 15), `bot.go` (1, 12), `handler.go` (13, 14), `control/control.go` (9). `callback.go` gets one registration line per action: append-only, trivial merge.

## Phase 1 — Callback foundation, Stop everywhere, humane errors

Ships: buttons work end to end; groups can stop a turn; errors say what to do. Sequential (Tasks 2-3 depend on 1).

### Task 1: Callback dispatcher

**Files:** Create `internal/telegram/callback.go`, `callback_test.go`; modify `bot.go`.

**Interfaces:**
- Produces: `type cbData struct{ Act string; Args []string }`; `func encodeCB(act string, args ...string) (string, error)` (error if > 64 bytes or an arg contains `:`); `func decodeCB(s string) (cbData, bool)`; `type cbHandler func(ctx context.Context, q cbQuery) (toast string, err error)`; `type cbQuery struct{ ID string; ChatID int64; ThreadID int; MessageID int; UserID int64; Chat store.RegisteredChat; Data cbData }`; `func (b *Bot) onCallback(act string, h cbHandler)`; `func (b *Bot) callbackHandler(ctx context.Context, api *bot.Bot, upd *models.Update)`.
- Consumes: `resolveRegistered`, `inboundThreadID` (on the callback's message).

- [ ] **Step 1: Failing tests** `TestEncodeCB_RejectsOver64Bytes`, `TestDecodeCB_RoundTrip`, `TestCallback_UnknownActAnswersExpired`, `TestCallback_UnregisteredChatAnswersAndIgnores`, `TestCallback_HandlerErrorStillAnswers`, `TestCallback_PanicStillAnswers`, `TestCallback_InaccessibleMessageToastOnly` (fake API recording `answerCallbackQuery` exactly once per query).
- [ ] **Step 2:** Run; expect FAIL.
- [ ] **Step 3:** Implement; register with `api.RegisterHandler(bot.HandlerTypeCallbackQueryData, "", bot.MatchTypePrefix, b.callbackHandler)` in `New`. Set `AllowedUpdates` explicitly to `message`, `callback_query`, `stopped_message_generation` (check `bot.WithAllowedUpdates` in v1.27.0; record the list in a comment).
- [ ] **Step 4:** `go test ./internal/telegram/...` PASS. Commit `telegram: route inline button callbacks through one dispatcher`.

**Acceptance:** a test button sent by a debug path returns a toast within the test; the default handler still sees every message.

### Task 2: Stop button on the edit path (depends on 1)

**Files:** Modify `streaming.go`, `stop.go`, `agentwait.go`; create `keyboard.go`, `keyboard_test.go`.

**Interfaces:**
- Produces: `func stopKeyboard(turnID int64) models.InlineKeyboardMarkup` (`⏹`, style `danger`, data `st:<turnID>`); `func (b *Bot) stopTurn(turnID int64, chatID int64) bool` extracted from `handleGenerationStopped`.
- Consumes: `registerInflight`, `inflightStop`.

- [ ] **Step 1: Failing tests** `TestPlaceholderCarriesStopButton`, `TestStopCallbackCancelsTurnInSameChat`, `TestStopCallbackFromOtherChatIsRefused`, `TestStopCallbackAfterFinishToastsFinished`, `TestFinalEditRemovesKeyboard`, `TestWaitPlaceholderCarriesStopButton`.
- [ ] **Step 2:** Run; FAIL.
- [ ] **Step 3:** Attach the markup in `sendPlaceholderText`; every text edit of the placeholder passes the same markup (one call, no flicker); the final edit passes an empty markup. Turn ends `stopped` via the existing path.
- [ ] **Step 4:** PASS. Commit `telegram: stop button on placeholder replies`.

**Acceptance:** in a group test chat a long turn can be stopped; HANDOFF gets the live item.

### Task 3: Humane errors and Retry (depends on 1)

**Files:** Create `errors.go`, `errors_test.go`; modify `streaming.go` (`handleStreamError`), `handler.go` (`runAgentSync` error branch), create `actions.go` (Retry only).

**Interfaces:**
- Produces: `type errKind int` (`errAuthExpired`, `errNotLoggedIn`, `errAgentFailed`, `errBusy`, `errDelivery`); `func classifyErr(err error) errKind`; `func (b *Bot) replyError(ctx context.Context, item queue.Item, err error)`; `func (b *Bot) retryTurn(ctx context.Context, q cbQuery) (string, error)` registered as `rt`.
- New queue items for retries use `SourceID = "cb:" + callbackQueryID` so `InsertTurn` dedup never swallows them and a double tap is one turn.

- [ ] **Step 1: Failing tests** `TestReplyError_AuthExpiredOffersLogin`, `TestReplyError_AgentFailedOffersRetry`, `TestReplyError_CooldownSuppressesSecondWithin60s`, `TestRetryCallback_EnqueuesOriginalTextSameThread`, `TestRetryCallback_DoubleTapOneTurn`, `TestReplyError_NoStderrInText`.
- [ ] **Step 2-4:** FAIL → implement → PASS. Commit `telegram: humane error replies with a retry button`.

**Acceptance:** forced agent failure shows text + Retry; Retry produces a reply in the same topic.

## Phase 2 — Reply actions (Q1, Q2)

Ships: one row under the newest reply. Depends on Phase 1.

### Task 4: Reply keyboard builder

**Files:** `keyboard.go`, `keyboard_test.go`.

**Interfaces:** `func replyKeyboard(turnID int64, reply string) models.InlineKeyboardMarkup` → `🔁 rg:<id>`, `📋` `CopyText` only when `copyable(reply)` (≤ 256 runes, or a single code block that fits), `⋯ mo:<id>`; `func moreKeyboard(turnID int64) ...` → `Steps st2:<id>`, `As file af:<id>`, `‹ back mb:<id>`.

- [ ] Tests `TestReplyKeyboard_CopyOnlyWhenShort`, `TestReplyKeyboard_AllDataUnder64Bytes`, `TestMoreKeyboard_BackRestores`. Implement. Commit `telegram: build reply action keyboards`.

### Task 5: Attach to the final reply, clear the previous one (depends on 4)

**Files:** `streaming.go` (`sendFinal` gains `markup models.ReplyMarkup`), `resume.go`, new in-memory `lastReply map[convKey]msgRef` on `Bot`.

- [ ] Tests `TestSendFinal_KeyboardOnLastChunkOnly`, `TestSendFinal_NoKeyboardOnDocuments`, `TestNewReplyClearsPreviousKeyboardSameConversation`, `TestOtherThreadKeyboardUntouched`, `TestStoppedReplyGetsNoRegenerate` (stopped turns get `🔁` only, per Q2 answer).
- [ ] Implement: markup attached after `recordReply`; previous reply's markup cleared with `EditMessageReplyMarkup`, error logged only. Commit `telegram: action buttons under the newest reply`.

### Task 6: Turn actions (depends on 5)

**Files:** `actions.go`, `actions_test.go`; `streaming.go` keeps the D024 step list per turn in `stepLog` (ring of the last 50 steps, 1 h TTL).

**Interfaces:** `func (b *Bot) regenerate(ctx, q cbQuery) (string, error)` (`rg`): `store.GetTurn`, check `turn.ChatID == q.ChatID`, enqueue `Text = regeneratePrefix + turn.Text`, same `ThreadID`, `SourceID = "cb:"+q.ID`; `steps` (`st2`): send the stored steps as an expandable quote, or toast "no steps kept"; `asFile` (`af`): send `reply.md` via `sendDocument`; `more`/`back` (`mo`/`mb`): swap keyboards in place.

- [ ] Tests `TestRegenerate_SameThreadNewTurn`, `TestRegenerate_TurnFromOtherChatRefused`, `TestRegenerate_WorksAfterRestart` (turn loaded from store), `TestSteps_ExpiredToast`, `TestAsFile_SendsDocumentInThread`, `TestMoreBack_SwapsMarkupOnly`.
- [ ] Commit `telegram: regenerate, steps and as-file actions`.

**Acceptance (live):** spec live items 3 and 4.

## Phase 3 — Visible state (Q3)

Independent of Phase 2 after Phase 1; can run in a parallel worktree. Conflicts with Phase 2 only in `streaming.go` (different functions).

### Task 7: Status reactions

**Files:** Create `status.go`, `status_test.go`; modify `reactions.go`, `streaming.go`, `handler.go`, `control` compact handler hook (✍).

**Interfaces:** `type turnState int` (`stQueued`, `stThinking`, `stTool`, `stDone`, `stError`, `stStallSoft`, `stStallHard`, `stCompacting`); `type statusReactor struct{...}`; `func (b *Bot) newStatusReactor(chatID int64, msgID int) *statusReactor`; `func (r *statusReactor) Set(s turnState)` (debounced 700 ms, final states immediate); `func (r *statusReactor) Touch()` (resets stall timers 25 s / 60 s).

- [ ] Tests `TestStatusReactor_DebouncesBursts` (fake clock), `TestStatusReactor_FinalStateAlwaysWritten`, `TestStatusReactor_StallAfter25s`, `TestStatusEmojiAllAllowed` (against a list copied from the Bot API ReactionTypeEmoji doc).
- [ ] Commit `telegram: turn state as message reactions`.

### Task 8: Status header and queue position

**Files:** `status.go`, `streaming.go`, `agentwait.go`, `internal/queue/queue.go`, `queue_test.go`.

**Interfaces:** `func (q *Queue) Position(folder string, turnID int64) int` (0 = running or unknown, n = n-th waiting); `func statusHeader(elapsed time.Duration, steps int, pos int) string` (`⏳ 0:42 · 5 steps`, `в очереди: 2-й`).

- [ ] Tests `TestQueuePosition_Waiting`, `TestStatusHeader_Format`, `TestDraftShowsHeaderAboveSteps`, `TestEditPathHeaderEvery5s`, `TestHeaderDropsWhenAnswerTextStarts`.
- [ ] Commit `telegram: elapsed time and queue position in the draft`.

**Acceptance (live):** spec live item 6.

## Phase 4 — Buttons for commands (Q6)

Depends on Phase 1. Independent of 2 and 3; owns `internal/control`.

### Task 9: `control.Response.Buttons` and ADR

**Files:** `internal/control/control.go`, `control_test.go`, `cmd/gopod` CLI printer, `docs/CONTROL.md` §3, `docs/DECISIONS.md` (D028).

**Interfaces:** `type Button struct{ Label string; Command string; Args []string; Style string }`; `Response.Buttons [][]Button`; telegram renders each as data `cc:<token>` where token maps (in memory, 10 min) to `Command{Name, Args}`; pressing dispatches through `Router.Dispatch` with a `Caller` built from the callback (same `IsOwner` rule as slash commands).

- [ ] Tests `TestResponseButtons_CLIPrintsLabels`, `TestCommandButton_DispatchesThroughRouter`, `TestCommandButton_OwnerOnlyRefusedForNonOwner`, `TestCommandButton_ExpiredToken`.
- [ ] Write D028 (callback layer, buttons as Router commands, data rules). Commit `control: buttons in command responses`.

### Task 10: Pickers and confirmations (depends on 9)

**Files:** handlers of `model`, `provider`, `clear`, `chats.unregister`, container commands; `telegram/callback.go` (`cf` act); `docs/CONTROL.md`.

- [ ] `/model` and `/provider` with no args return buttons (current marked, `primary`); press edits the picker message to the result (`EditMessageText` on `q.MessageID`).
- [ ] Confirmable commands return `Code = control.CodeConfirm` plus a Yes (`danger`)/Cancel pair; Yes re-dispatches with an internal `confirmed` flag the Router re-checks with `Perm`.
- [ ] Tests `TestModelPicker_EditsInPlace`, `TestClear_AsksConfirmation`, `TestConfirm_YesRunsOnce`, `TestConfirm_CancelEditsToCancelled`, `TestConfirm_CLIFlagSkipsPrompt` (`--yes`).
- [ ] Commit `control: pickers and confirmations for risky commands`.

### Task 11: Paged outputs (depends on 9)

**Files:** `telegram/keyboard.go` (`pageKeyboard`), `handler.go` (`dispatchSlash` rendering), handlers of `logs`, `recall`, `tasks`, `help`.

- [ ] Replies longer than one message become pages (`pg:<token>:<n>`), edited in place; tests `TestPages_SplitOnLineBoundaries`, `TestPages_NavigateEditsSameMessage`, `TestPages_ExpiredToast`.
- [ ] Commit `telegram: page long command replies`.

## Phase 5 — Menus and strings (Q4)

Independent after Phase 1; touches `bot.go` (menus) and many files for strings: run it **after** Phases 2-4 merge, or confine Task 12 to `bot.go` and do Task 12b last.

### Task 12: Scoped commands menu

**Files:** `bot.go`, `bot_test.go` (new tests), `internal/control` (`List` already returns `Perm`).

- [ ] `publishCommands` sends default scope = Public + ChatLocal, `BotCommandScopeChat{owner}` = all; a `ru` variant via `LanguageCode` if Q4 says so; on `BOT_COMMANDS_TOO_MUCH` drop owner extras and retry once. Tests `TestPublishCommands_OwnerScopeHasOwnerOnly`, `TestPublishCommands_DefaultScopeHasNoOwnerOnly`, `TestPublishCommands_TooMuchRetriesSmaller`.
- [ ] Commit `telegram: commands menu per scope`.

### Task 12b: One strings file

- [ ] Move every user-facing string of the telegram package (errors, wait, lifecycle, resume banner, toasts) into `strings.go` in the language Q4 picks; `TestStrings_NoEnglishLeftInUserPaths` (grep-style test over the package, allowlist for logs). Commit `telegram: user-facing strings in one place`.

## Phase 6 — Input context

Independent of Phases 2-5; owns `handler.go` inbound path.

### Task 13: Reply, quote and forward header

**Files:** create `inbound_context.go`, `_test.go`; modify `handler.go` (before `queue.Item` build).

**Interfaces:** `func inboundContext(m *models.Message) string` → `[in reply to <name>: "<≤500 chars>"]` (uses `m.Quote.Text` when present), `[forwarded from <origin>]`, empty otherwise.

- [ ] Tests `TestInboundContext_ReplyToBotAnswer`, `TestInboundContext_QuoteWins`, `TestInboundContext_ForwardFromUserChannelHidden`, `TestInboundContext_TruncatesRunes`, `TestInboundContext_ReplyToTopicRootIgnored` (a topic's service root is not a reply).
- [ ] Commit `telegram: pass replied and quoted text to the agent`.

### Task 14: Albums, audio, video notes

**Files:** `handler.go`, `media.go`, `voice.go`; tests `TestAlbum_ThreePhotosOneTurn` (fake clock, 1 s window), `TestAudioFile_Transcribed`, `TestVideoNote_Transcribed`, `TestSticker_Ignored`; chat actions `upload_document`/`record_voice` in `document.go`/`voice.go`.
- [ ] Commit `telegram: albums, audio files and video notes as input`.

**Acceptance (live):** spec live items 8 and 9.

## Phase 7 — Rich final replies (T7, Q5)

Depends on Phase 2 Task 5 (`sendFinal` markup signature). Risky: ship dark.

### Task 15: Rich reply path behind a flag

**Files:** create `rich.go`, `rich_test.go`; modify `streaming.go` (`sendFinal`), `internal/config` (`GOPOD_RICH_REPLIES`, default off), `docs/DECISIONS.md` (D029).

**Interfaces:** `func (b *Bot) sendRich(ctx context.Context, chatID int64, thread int, replyTo int, md string, markup models.ReplyMarkup) (int, error)` using `SendRichMessage{RichMessage: InputRichMessage{Markdown: md}}`; sticky per-chat fallback `richSupported/markRichUnsupported` modelled on `draft.go:91-110`.

- [ ] Step 1: read the `sendRichMessage` and `InputRichMessage` pages; write limits (length, which markdown, tables, `<tg-thinking>`) in a comment at the top of `rich.go`.
- [ ] Tests `TestSendFinal_RichWhenFlagOnPrivateChat`, `TestSendFinal_RichFallsBackOn400Sticky`, `TestSendFinal_GroupNeverRich`, `TestSendFinal_RichStillAttachesLongCodeFirst`, `TestSendFinal_RichReplyIDRecorded`.
- [ ] Commit `telegram: rich-message final replies behind a flag`.

**Acceptance (live):** spec live item 7 on iOS, Android, Desktop before the flag defaults on.

## Phase 8 — Optional extras (Q7, Q10)

- **Task 16:** task-result buttons (`⏸`/`▶`) via Router commands `tasks pause|run`. Depends on Task 9.
- **Task 17:** heuristic topic titles via `EditForumTopic` on the first message of a new topic (`TestTopicTitle_FirstFourWords`). Independent.

## Parallel execution map

| Wave | Tasks | Notes |
|---|---|---|
| 1 | 1 → 2 → 3 | sequential, one agent; foundation |
| 2 | Phase 2 (4-6), Phase 3 (7-8), Phase 4 (9-11), Phase 6 (13-14) | four worktrees; `streaming.go` touched by 5, 7, 8 (different functions: rebase the later PR); `callback.go` registrations append-only |
| 3 | 12, 12b, 15, 16, 17 | 12b last (touches many files); 15 after 5 |

Effort (one agent incl. review fixes): Phase 1 ~2 days, Phase 2 ~2 days, Phase 3 ~1.5 days, Phase 4 ~2.5 days, Phase 5 ~1 day, Phase 6 ~1.5 days, Phase 7 ~1.5 days plus live time, Phase 8 ~0.5 day.

## Live verification

Run the spec's "Verify live" list after each wave on a real bot: a private chat with topics enabled and one registered group; record results in HANDOFF and in D028/D029 consequences. Items that need clients other than the owner's phone (copy_text, button styles, rich rendering) are marked pending until checked on Desktop and a second mobile OS.

## Docs per phase

Each phase PR updates HANDOFF (state, live items), ROADMAP Phase 8 rows (T10-T16), and CONTROL.md when command behaviour changes. ADRs: D028 with Task 9 (or Task 1 if Phase 4 is postponed), D029 with Task 15.

## Self-review

- Every spec feature F1-F15 maps to a task: F1→1, F2→2, F3→4-6, F4→3, F5→7, F6→8, F7→9-10, F8→10, F9→11, F10→13, F11→14, F12→12, F13→15, F14→16, F15→17.
- No task authorizes from callback data; every settings button goes through the Router.
- Owner gates: Q1/Q2 before Phase 2, Q3 before Task 7, Q4 before 12b, Q5 before the flag flips, Q6 before Task 10, Q7 before 17.
