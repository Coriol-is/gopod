# Silent mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A per-chat `/silent` toggle under which a streamed agent turn shows only its progress (status header and tool steps) and delivers the answer as one final message, never streaming answer text into the draft or placeholder.

**Architecture:** The toggle is a Router command (`internal/control`) that persists `silent:<chatFolder>` in `router_state`, exactly like `/model` persists `model:<chatFolder>`. The Telegram bot reads the flag once at the start of `streamReply` and, when set, keeps the progress view for the whole turn: answer chunks only accumulate for a Stop. Nothing changes in the runner, the final-reply path (D025), the sync path or the voice reply.

**Tech Stack:** Go, `testing` stdlib, existing `fakeTelegram` fixture.

**Spec:** Design agreed in chat on 2026-11-01 (owner: "по твоим догадкам по реализации все верно"). Summary:

- `/silent` with no argument shows `Silent mode: on|off` plus a picker (`on` / `off`, current marked); `/silent on|off` sets it; the button re-dispatches the command through the Router (D028). Perm `ChatLocal`; in a group only the owner may use it (same as `/model`).
- Scope is the chat (all threads), persisted across restarts, default off.
- In silent mode the draft (or edit-path placeholder) shows the status header plus the last five tool steps until the end; a run without steps shows the header alone after `headerQuiet`. The final reply goes out as today.
- Stop in silent mode delivers the text received so far, as today (D021); with no text, the stopped note.
- Docs: CONTROL.md rows (§ChatLocal table, §6.1 pickers), ADR D055 extending D024.

## Global Constraints

- Commands go through `internal/control`; authorization is the Router's `Perm` plus `groupMemberDenied` (CLAUDE.md, D011).
- State key format `silent:<chatFolder>`, value `"1"` for on, `""` for off (`store.SetState`/`GetState`; a missing key is off).
- Reply copy is English, like the other control commands. Progress strings and header format are unchanged.
- No new packages; no runner changes.

## Review Focus

1. A chat that is not registered calls `/silent`: reply "Chat not registered." with `Code: 1` (test in Task 1).
2. `/silent maybe` (unknown argument): usage reply with `Code: 1`, state unchanged (test in Task 1).
3. Silent mode with a plain run (no `Progress` channel, e.g. Codex): draft never shows chunk text; the final `sendMessage` carries the reply (test in Task 2).
4. Stop during a silent turn after text arrived: the partial text is the stopped reply (test in Task 2).
5. The store read fails (closed DB): the turn streams normally (treated as off) and the error is logged once; no test, reviewed by eye in Task 2.

---

### Task 1: `/silent` command in `internal/control`

**Files:**
- Create: `internal/control/silent.go`
- Create: `internal/control/silent_test.go`
- Modify: `cmd/gopod/main.go` (after the `RegisterModelCommand` call, ~line 513)

**Interfaces:**
- Produces: `func RegisterSilentCommand(r *Router, currentFn func(chatFolder string) bool, setFn func(chatFolder string, on bool) error, chatFolderLookup func(chatID int64) string)`
- Produces: `const SilentStateKey = "silent:"` (store key prefix) — Task 2 reads the same key.

- [ ] **Step 1: Write the failing test** `internal/control/silent_test.go`

```go
func TestSilentCommand(t *testing.T) {
	r := New(nil)
	state := map[string]bool{}
	RegisterSilentCommand(r,
		func(folder string) bool { return state[folder] },
		func(folder string, on bool) error { state[folder] = on; return nil },
		func(chatID int64) string {
			if chatID == 1 {
				return "owner"
			}
			return ""
		})
	call := func(args ...string) Response { /* Dispatch Command{Name: "silent", Args: args, Caller: Caller{ChatID: 1, Source: SourceTelegram}} */ }

	resp := call()
	// "Silent mode: off"; picker buttons on/off, "✓ off" marked
	resp = call("on")       // state["owner"] == true; text contains "on"
	resp = call()           // text contains "Silent mode: on"; "✓ on" button
	resp = call("off")      // state["owner"] == false
	resp = call("maybe")    // Code == 1, state unchanged
	// ChatID 99 (unregistered): Code == 1
}
```

Assert the picker with `resp.Buttons`: two buttons in one row, labels `✓ off` / `on` first, then `on` marked after `on`. Assert `Command == "silent"` and `Args == []string{"on"}` on the `on` button.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/control -run TestSilentCommand`
Expected: FAIL, `undefined: RegisterSilentCommand`

- [ ] **Step 3: Implement `RegisterSilentCommand` in `internal/control/silent.go`**

Mirror `RegisterModelCommand` (handlers.go:233): `r.Register("silent", "silent", "show or toggle silent mode for this chat (progress only, answer at the end)", PermChatLocal, ...)`, `groupMemberDenied` check, folder lookup with "Chat not registered." / `Code: 1`. No args: text `"Silent mode: " + cur + "\n\nOn: the draft shows only progress (steps and elapsed time); the answer arrives as one message at the end.\nUsage: /silent on | /silent off"` with `Buttons: pickerButtons("silent", []string{"on", "off"}, cur)`. Arg `on`/`off` (case-insensitive): call `setFn`; reply `"Silent mode on. Applies from the next message."` / `"Silent mode off. Applies from the next message."`. Any other arg: `"Usage: /silent on | /silent off"`, `Code: 1`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/control -run TestSilentCommand`
Expected: PASS

- [ ] **Step 5: Wire it in `cmd/gopod/main.go`** right after `RegisterModelCommand`:

```go
control.RegisterSilentCommand(router,
	func(chatFolder string) bool {
		v, _ := st.GetState(context.Background(), control.SilentStateKey+chatFolder)
		return v == "1"
	},
	func(chatFolder string, on bool) error {
		v := ""
		if on { v = "1" }
		if err := st.SetState(context.Background(), control.SilentStateKey+chatFolder, v); err != nil {
			return fmt.Errorf("persist silent mode: %w", err)
		}
		return nil
	}, chatFolderLookup)
```

- [ ] **Step 6: Build and vet**

Run: `go build ./... && go vet ./internal/control ./cmd/...`
Expected: no output

- [ ] **Step 7: Commit**

```bash
git add internal/control/silent.go internal/control/silent_test.go cmd/gopod/main.go
git commit -m "control: add /silent per-chat toggle with on/off picker"
```

### Task 2: Silent streaming in `internal/telegram`

**Files:**
- Modify: `internal/telegram/streaming.go` (`streamReply`, ~lines 243-360)
- Test: `internal/telegram/progress_stream_test.go` (append)

**Interfaces:**
- Consumes: `control.SilentStateKey` (Task 1), `b.store.GetState`, `item.Folder`.
- Produces: `func (b *Bot) silentMode(ctx context.Context, item queue.Item) bool` in `streaming.go`.

- [ ] **Step 1: Write the failing tests** (append to `progress_stream_test.go`)

A helper `setSilent(t, b, item)` that calls `b.store.SetState(ctx, control.SilentStateKey+item.Folder, "1")`.

```go
// Silent mode: steps keep showing after answer text; the final message carries the answer.
func TestStreamReply_SilentKeepsProgressUntilFinal(t *testing.T)
```
Flow: `okDrafts(fake, "601")`, `newStreamFixture`, `setSilent`, start `streamReply`; send step `Bash go test ./...`; `waitFor` draft `"⚙️ go test ./...\n❘"`; send chunks `"All tests "`, `"pass."`; `time.Sleep(50ms)`; assert no `sendMessageDraft` text contains `"All tests"`; send step `Read /workspace/chat/main.go`; `waitFor` draft `"⚙️ go test ./...\n📖 main.go\n❘"`; `s.finish("All tests pass.")`; assert exactly one `sendMessage`, text contains `"All tests pass."`, not `"go test"`.

```go
// Silent mode on the edit path: the placeholder shows steps only; the final edit is the answer.
func TestStreamReply_SilentEditPathShowsStepsOnly(t *testing.T)
```
Flow: same `fake.respond` as `TestStreamReply_EditPathShowsProgressInPlaceholder`; `setSilent`; step `Edit /workspace/chat/a.go`; chunks `"**Fixed** a.go"`; `time.Sleep(50ms)`; assert no `editMessageText` text contains `"Fixed"` yet; `s.finish("**Fixed** a.go")`; assert last edit has `parse_mode == "HTML"` and `<b>Fixed</b>`.

```go
// Silent mode with a plain run (no Progress): the draft shows only the header, the final message the text.
func TestStreamReply_SilentPlainRunShowsHeaderOnly(t *testing.T)
```
Flow: build `&runner.AgentStream{Chunks: chunks, Result: result}` (Progress nil), `setSilent`; send chunk `"Hello"`; `time.Sleep(50ms)`; assert no draft text contains `"Hello"`; finish with `"Hello"`; assert one `sendMessage` with `"Hello"`.

```go
// Stop in silent mode after text arrived delivers that text.
func TestStreamReply_SilentStopDeliversPartialText(t *testing.T)
```
Flow: as `TestStreamReply_StopAfterAnswerTextDeliversIt` but with `setSilent` and no `waitFor` on answer text (wait for the step draft instead); after the stop, the first `sendMessage` text contains `"Half an answer"`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/telegram -run 'TestStreamReply_Silent'`
Expected: FAIL (`undefined: control.SilentStateKey` until Task 1 is merged into the branch; afterwards, assertions fail because answer text is drafted).

- [ ] **Step 3: Implement**

In `streaming.go`:
- `func (b *Bot) silentMode(ctx context.Context, item queue.Item) bool`: returns false when `b.store == nil`; `GetState(ctx, control.SilentStateKey+item.Folder)`; on error log at Warn (`"silent mode lookup failed"`, chat id, err) and return false; else `v == "1"`.
- In `streamReply`, after `status := b.turnStatus(item)`: `silent := b.silentMode(ctx, item)`.
- In `addChunk`: write to `full` always; when `silent`, return `true` right after (`status.Set(stThinking)` still runs; `accumulated`, `answering`, `dirty`, overflow untouched).
- `view()` is unchanged: with `answering` never set it keeps the header-and-steps view; for a plain run it shows the header after `headerQuiet` and otherwise an empty `accumulated` (the opening cursor draft stays).

- [ ] **Step 4: Run the telegram suite**

Run: `go test ./internal/telegram -run 'TestStreamReply|TestRunAgentStreaming' -race`
Expected: PASS, including the four new tests.

- [ ] **Step 5: Commit**

```bash
git add internal/telegram/streaming.go internal/telegram/progress_stream_test.go
git commit -m "telegram: silent mode streams progress only, answer in the final message"
```

### Task 3: Docs

**Files:**
- Modify: `docs/CONTROL.md` (ChatLocal table after the `/model` row ~line 193; §6.1 pickers paragraph ~line 340)
- Modify: `docs/DECISIONS.md` (append D055)
- Modify: `ROADMAP.md` (a row in the table where `/model`-style UX items live, status done after merge)

- [ ] **Step 1: CONTROL.md** — table row: `` | `/silent [on\|off]` | — | ChatLocal | Show (picker) or toggle silent mode: the draft shows only progress, the answer arrives as one final message | ``. §6.1: add `/silent` with no args offers `on`, `off` to the pickers paragraph.

- [ ] **Step 2: DECISIONS.md** — `## D055 — Silent mode: progress-only streaming per chat`, Date 2026-11-01, Status Accepted, Extends D024. Context (narration and answer deltas in the draft are noise for long coding turns; owner asked for a toggle), Decision (the four bullets of the spec summary above, plus the `silent:<folder>` key and that Stop keeps D021 behaviour), Consequences (silent is per chat not per thread; a plain run shows only the elapsed-time header; the owner sees the answer later than before, by design).

- [ ] **Step 3: Commit**

```bash
git add docs/CONTROL.md docs/DECISIONS.md ROADMAP.md
git commit -m "docs: record /silent progress-only streaming (D055)"
```

### Task 4: Verify, PR, merge

- [ ] **Step 1:** `go build ./... && go vet ./... && go test ./... -race` — all PASS.
- [ ] **Step 2:** `gofmt -l internal cmd` prints nothing.
- [ ] **Step 3:** Push `feat/silent-mode`, open PR titled `telegram: add /silent progress-only streaming mode`, body: what/why, test list, D055 link.
- [ ] **Step 4:** Independent review (reviewer subagent on the diff), fix findings, wait for green CI, `gh pr merge --squash --admin --delete-branch`.
- [ ] **Step 5:** `/handoff` on `main` (HANDOFF.md + ROADMAP row), as a follow-up docs PR if needed.
