# Dynamic Telegram UX: buttons, status, rich replies

Status: **proposed** (2026-10-25). Research and design by the controller;
every point marked *owner* below waits for the owner's answer, everything
marked *call* is the controller's decision and can be overturned in review.
Covers ROADMAP Phase 8 items T6 (inline keyboards) and T7 (rich messages)
and adds the rest of the "dynamic" chat experience around them.

## Intent

The owner wants talking to gopod in Telegram to feel closer to the Claude
and Codex chat apps: you see that the assistant is working and on what, you
can act on an answer with one tap (again, copy, show more, stop), settings
are picked from buttons instead of typed, and errors tell you what to do
next. Phase 8 already built the hard parts: native draft streaming with a
Stop button (D021), conversations per private-chat topic (D022), tool-step
lines in the draft (D024, PR #22), collapsed or attached long code (D025),
the agent gate with a visible wait (D026) and restart notices (D027). What
is missing is everything that needs a *callback*: gopod has no inline
keyboard and handles no `callback_query` at all. Most of this spec is that
missing layer, plus status and input-context polish that is cheap once the
layer exists.

Why now: Bot API 10.x (go-telegram/bot v1.27.0, already in `go.mod`) has
the pieces (button `style`, `copy_text`, `disabled` buttons, rich messages
with tables and thinking blocks, ephemeral messages), and two reference
bots show which of them earn their keep.

## Sources

- gopod `main` at `2a8c242`, plus the open branches for D024
  (`feat/coding-progress-clean`, PR #22) and D027 (`feat/lifecycle-notices`).
- **neuro-horoscope** (owner's example). The TypeScript part is the
  Cloudflare Worker in `worker/` (package `zodiacle-worker`), built on
  **grammY** 1.4x with `@grammyjs/auto-retry`; `@grammyjs/menu`,
  `@grammyjs/commands` and `@grammyjs/auto-chat-action` are dependencies
  but nothing under `worker/src` imports them. The Python side (`app/`,
  `core/`) is the older bot on python-telegram-bot 22.x and is context only.
  Paths below are relative to `worker/src/`.
- **OpenClaw** Telegram channel, `extensions/telegram/src/` at `46b1ecd6ed`
  (2026-04-09), grammY with `@grammyjs/runner` and
  `@grammyjs/transformer-throttler`; user docs `docs/channels/telegram.md`.
- Bot API changelog 9.0-10.3 (core.telegram.org/bots/api-changelog,
  read 2026-10-25) and the go-telegram/bot v1.27.0 module source.

## Goals

1. One callback layer: every inline button press is answered, authorized
   from the chat it was pressed in, and routed either to a *turn action*
   (telegram package) or to a *control command* (`internal/control`).
2. Act on a reply in one tap: Regenerate, Copy, Retry after an error,
   Stop on every path (not only in drafts).
3. Show state without reading logs: reaction on the user's message tracks
   the turn (queued, working, tool, done, error, stalled); the draft shows
   elapsed time, step count and queue position.
4. Settings and risky commands by buttons: model/provider pickers,
   confirmations, paged long outputs, all through the Router.
5. The agent sees what the user pointed at: replied-to, quoted and
   forwarded text go into the prompt; albums and audio are accepted.
6. Rich messages (T7) for final replies where they render better than
   HTML, behind a flag and with the HTML path as fallback.

## Non-goals

- Inline mode, Mini Apps, payments/Stars, polls, business-account
  features (checklists need a business connection: `sendChecklist` is
  for connected business accounts only).
- A webhook transport; gopod stays on long polling.
- A second channel or a messaging abstraction (CLAUDE.md).
- Parallel threads (T5/T5b) and anything in the runner beyond what a
  button needs.
- Localising for many languages: two languages at most (see Q4).

## Anti-goals

- No button wall. A reply carries at most one row of at most three buttons
  by default; more only behind a "⋯" button.
- No authorization from callback data. `callback_data` is a routing hint,
  like `thread_id` (D022); permission comes from the chat of the message
  the button sits on and from the Router's `Perm` (D011).
- No secrets, prompts or file paths in `callback_data`, toasts or status
  lines (D012); callback data holds ids only.
- No control command wired to a button handler directly: a button that
  changes settings dispatches a `control.Command` (CLAUDE.md rule).
- No reflow of the streaming core: buttons attach to messages that
  already exist; the draft/edit policy of D021 stays.

## Constraints (from CLAUDE.md and ADRs)

- Go, no CGO; go-telegram/bot only; DB through `internal/store`.
- D011: admin actions via `internal/control`, `Perm` checked by the Router.
- D012: secrets only from env; the slog redactor; D024's progress-line
  redactor applies to every status string.
- D020: a turn's reply id is the text message; nothing here may create a
  second "reply" for a turn or break `recordReply` memoization.
- D021: drafts are private-chat only; the edit path is the fallback and
  the only path in groups. Stop ends a turn `stopped`.
- D022: every send carries `message_thread_id`; a callback must answer in
  the topic of the message that carries the button.
- D025: final replies go through `sendFinal`; any keyboard is attached
  there (last chunk only).
- D026: the gate wait notice is the first state of the status line.
- D027: lifecycle notices stay plain text, no buttons.

## What exists today

| Area | gopod today | Where |
|---|---|---|
| Acknowledge | 👀 on receipt, 👍 done, 👎 error, fixed set | `internal/telegram/reactions.go:13-48`, `handler.go:144` |
| Typing | `sendChatAction typing` loop while a sync turn runs | `handler.go:441` |
| Streaming | `sendMessageDraft` with Stop in private chats; placeholder + `editMessageText` elsewhere; 1 s tick | `streaming.go:181`, `draft.go:112-204` |
| Stop | draft Stop button only (`stopped_message_generation`) | `stop.go:79`, `handler.go:31` |
| Progress | last five tool steps in the draft (PR #22, not on `main`) | `progress.go` on `feat/coding-progress-clean` |
| Waiting | gate wait shown as stoppable draft or placeholder | `agentwait.go:56-210` |
| Formatting | Markdown → Telegram HTML, plain-text fallback; long code collapsed (>12 lines) or attached (>60 lines) | `format.go:25-57`, `longcode.go:60-177`, `chunkhtml.go` |
| Errors | three fixed English strings, 👎 reaction | `streaming.go:570-591` |
| Commands menu | one global `setMyCommands` built from `Router.List()`, no scopes, no language | `bot.go:215-243` |
| Callbacks / keyboards | **none**: no `callback_query` handler, no `reply_markup` anywhere | — |
| Reply context | `reply_to_message` stored in `messages`, **not** put into the prompt; quote and forward origin ignored | `handler.go:655-658` |
| Media in | photo, document, voice (Whisper); no audio file, video note, album grouping | `handler.go:55-110`, `media.go`, `voice.go` |
| Media out | TTS voice reply (`/voice` mode), code snippets as documents | `voice.go:159`, `document.go` |
| Proactive | scheduler and IPC messages to main chat, plain text | `ipc_sink.go`, `internal/scheduler` |
| Restarts | restart/back-online notices (D027, branch) | `feat/lifecycle-notices` |

## Reference comparison

✅ has it, ➖ partial, ❌ no. References are file:line in the reference
repos (worker = `neuro-horoscope/worker/src`, OC =
`openclaw/extensions/telegram/src`, OC doc = `docs/channels/telegram.md`).

| Feature | gopod | neuro-horoscope worker | OpenClaw |
|---|---|---|---|
| Library | go-telegram/bot 1.27 | grammY + auto-retry (`bot.ts:1,23`) | grammY + runner + throttler (`package.json`) |
| Streaming | ✅ draft + edit fallback | ❌ answer sent whole (`reading.ts:127`) | ✅ draft, fallback to edit (`draft-stream.ts:65,165,344`), 1 s throttle (`:9`), min first-chunk size (`:126`) |
| Reasoning lane | ❌ | ❌ | ✅ separate "Reasoning" preview, final without it (`reasoning-lane-coordinator.ts:9-20`, OC doc :290) |
| Status reactions | ➖ 3 fixed | ❌ | ✅ queued/thinking/tool/coding/web/done/error/stall/compacting variants (`status-reaction-variants.ts:91-102`), allowed-emoji check (`:157-218`), debounce 700 ms, stall 25 s/60 s (core config `schema.base.generated.ts:18144`) |
| Callback safety net | ❌ | ✅ middleware answers every callback, logs, sends a generic error (`features/core.ts:108-150`) | ✅ one `callback_query` handler, edit-in-place helpers (`bot-handlers.runtime.ts:1105-1180`) |
| Inline keyboards | ❌ | ✅ prefix-routed callbacks (`core.ts:170,203,246,296`) | ✅ agent-sent buttons, unknown callbacks go to the agent as text (OC doc :369-433) |
| Edit in place | ➖ streaming only | ✅ settings screens edit the same message (`core.ts:343`, `moon.ts:104`, `retention.ts:149`) | ✅ (`bot-handlers.runtime.ts:1129-1170`) |
| Sub-menu swap | ❌ | ✅ keyboard swapped for a reason picker, then restored, toast via `answerCallbackQuery` (`core.ts:246-286`) | ➖ |
| Pickers | ❌ (`/model`, `/provider` typed) | ✅ sign/focus/language/gender pickers (`features/core/keyboards.ts:75-120`) | ✅ provider → model picker, 8 per page (`model-buttons.ts:42-55,164,197`) |
| Pagination | ❌ | ❌ | ✅ command list pages (`bot-handlers.runtime.ts:1394`) |
| Confirmations | ❌ | ❌ | ✅ exec approvals with buttons, 64-byte callback guard (`approval-callback-data.ts:1-20`, `exec-approvals.ts`, OC doc :813) |
| Button style | ❌ | ❌ | ✅ `danger`/`success`/`primary` (`bot-handlers.runtime.ts:~1170`) |
| Feedback on reply | ❌ | ✅ reaction counters as buttons (`keyboards.ts:29-56`) | ✅ user reactions become agent events (`bot-handlers.runtime.ts:778`, OC doc :683) |
| Reply keyboard | ❌ | ✅ persistent, resized, matched with `hears` (`keyboards.ts:98-104`, `core.ts:196`) | ❌ |
| Commands menu | ➖ one global list | ✅ per-language menus and explicit `allowed_updates` (`features/core/telegram-setup.ts:3-33`) | ✅ 100-command cap, retries smaller on `BOT_COMMANDS_TOO_MUCH` (`bot-native-command-menu.ts:13,106,329`) |
| Chat action | ➖ typing | ✅ typing before LLM (`features/dream.ts:137`) | ✅ with 401 backoff (`sendchataction-401-backoff.ts:52`) |
| Error UX | ➖ fixed strings | ✅ localized generic error (`core.ts:126-137`) | ✅ friendly reply, per-chat cooldown (`error-policy.ts:8-17`, OC doc :857) |
| Retry/backoff | ✅ draft 429 hold, final send retry (D021) | ✅ auto-retry 2×≤3 s; cron push honours `retry_after`, marks 403 as blocked (`jobs.ts:104-209`) | ✅ throttler + retry config (OC doc :764) |
| Reply/forward context | ➖ stored only | ❌ | ✅ reply metadata in the envelope, forward bursts debounced (`bot-handlers.runtime.ts:156-280,490`) |
| Albums | ❌ | ❌ | ✅ `media_group_id` grouping (`bot-handlers.runtime.ts:990`) |
| Voice out | ✅ TTS | ❌ | ✅ voice vs audio decision, `[[audio_as_voice]]` tag (`voice.ts:3-35`, OC doc :585) |
| Topics | ✅ private-chat threads | ❌ | ✅ thread-aware sessions, auto topic labels from first message (`auto-topic-label.ts:8-28`) |
| Proactive | ✅ scheduler, plain | ✅ cron pushes with buttons (`jobs.ts:162`, `wrangler.jsonc` crons) | ✅ |
| Update dedup | ✅ offset persisted | ✅ `claimUpdate` per update id (`bot.ts:26`) | ✅ offset store |
| Waiting-for-input state | ✅ `/login` code | ✅ `/dream` arms "next message is the dream" (`features/dream.ts:11`) | — |

Lessons: the worker is a *button-first* bot with no streaming; its value
for gopod is the callback hygiene (answer every query in `finally`, edit in
place, swap and restore keyboards, toasts) and the per-language menus.
OpenClaw is the closest analogue to gopod (an agent behind Telegram) and
shows what an agent chat needs on top of streaming: status reactions with
stall detection, approvals, pickers, error cooldown, reply context.

## Bot API features checked against go-telegram/bot v1.27.0

All exist in the module (`methods.go`, `methods_params.go`, `models/`):
`SendMessageDraft`, `SendRichMessage`, `SendRichMessageDraft` (10.1),
`EditMessageText.RichMessage`; `InputRichMessage{Markdown, HTML, Blocks}`
with block types including Table, Details, Thinking (`<tg-thinking>` in
HTML), Preformatted, ExpandableBlockQuotation; `InlineKeyboardButton`
with `Style`, `IconCustomEmojiID`, `CopyText`, `Disabled` (9.4/10.3);
`EphemeralMessageParameters` and `EditEphemeralMessage*`/`DeleteEphemeralMessage`
(10.2/10.3); `ReplyKeyboardMarkup.IsPersistent`, `InputFieldPlaceholder`;
`BotCommandScopeChat`, `BotCommand.IsEphemeral`; `AnswerCallbackQuery`,
`EditMessageReplyMarkup`; `DeleteMessageReaction` (10.0); `CreateForumTopic`/
`EditForumTopic`; `Message.Quote`, `ExternalReply`, `ForwardOrigin`;
`MessageEffectID`; `AllowedUpdates`. Not verified anywhere: how clients
render rich messages, whether `style` colours show on every client, and
ephemeral delivery in private chats (the docs say delivery "is not
guaranteed"). These go to the live list.

## Proposed feature set

Ranked by value for a one-owner personal assistant, then by effort/risk.
Effort: S ≤ ½ day, M ≈ 1-2 days, L > 2 days for one agent.

### F1. Callback layer (foundation) — value high, effort M, risk low

- **API:** `callback_query` (handler type `HandlerTypeCallbackQueryData`,
  prefix match), `AnswerCallbackQuery`, `EditMessageReplyMarkup`,
  `EditMessageText`.
- **Integration:** new `internal/telegram/callback.go`. Codec
  `act:arg1:arg2` (≤ 64 bytes, checked at build time of the keyboard;
  *call*). One dispatcher: resolve the registered chat from
  `callback.Message.Chat.ID` (same `resolveRegistered` as messages), take
  the thread from that message, then route by prefix: turn actions (F2,
  F3, F5) to telegram handlers; settings actions (F7, F8) to the Router as
  a `control.Command` with a `Caller` built exactly like a slash command.
  A deferred `answerCallbackQuery` in every path (the worker's `finally`
  pattern), optional toast text. Unknown or stale data → toast "button
  expired". Inaccessible message (older than 48 h) → toast only.
- **Startup:** `allowed_updates` becomes explicit:
  `message, callback_query, stopped_message_generation` (and
  `message_reaction` only if Q9 says yes). Today gopod relies on the
  default set (`handler.go:32-34`).
- **User sees:** buttons that respond in under a second, never a spinner
  that hangs.

### F2. Stop on every path — value high, effort S, risk low

The edit path (groups, chats that refused drafts, the gate placeholder)
has no Stop today. Add a `⏹ Stop` inline button (`style: danger`) to the
placeholder; the callback calls the same in-flight cancel as
`handleGenerationStopped` (`stop.go:79`), keyed by turn id in the data
(`st:<turnID>`), authorized by chat match. The final edit removes the
keyboard. Stop in drafts stays native.

### F3. Reply actions: Regenerate, Copy, More — value high, effort M, risk medium

- One row under the **last** chunk of the final reply (`sendFinal`):
  `🔁` Regenerate (`rg:<turnID>`), `📋` Copy (`copy_text`, only when the
  reply or its single code block is ≤ 256 chars; the Bot API limit for
  copy text, live check), `⋯` (more: "steps", "as file").
- **Regenerate** enqueues a new queue item in the same `(chat, thread)`
  with the original user text from the turn row and a short prefix
  ("give a different answer to the previous request"); the CLI sessions
  cannot be rewound, so the agent sees its first answer (*owner* Q2).
  New reply is a new message; the old one keeps its text.
- **Steps**: the D024 step list of the turn, kept in memory per turn for
  1 h (not persisted, *call*), sent as an expandable quote.
- **As file**: the reply as `reply.md` document (reuses `document.go`).
- Only the newest reply in a conversation keeps its keyboard; the
  previous one is cleared with `editMessageReplyMarkup` when a new final
  goes out (*call*, keeps history quiet; worker-style).
- Durable turns: keyboard attachment happens after `recordReply`; a lost
  keyboard edit is logged, never retried by the queue.

### F4. Humane errors with Retry — value high, effort S, risk low

Replace the three fixed strings (`streaming.go:570-591`) with a small
table: what happened, what to do, one button. Session expired / not
logged in → `🔑 /login` button (dispatches the login command). Agent
crash/timeout → `🔁 Retry` (`rt:<turnID>`, re-enqueues the same text).
Gate timeout → "busy, retry". Cooldown: the same error kind in the same
chat at most once per 60 s; repeats only change the reaction (OpenClaw's
error policy, shorter window, *call*). No stderr or paths in the text.

### F5. Status reactions as a state machine — value medium-high, effort S, risk low

`👀` queued → `🤔` agent started → `👨‍💻` tool running (D024 progress) →
`👍` done / `😱` error / `🥱` no output for 25 s / `😨` for 60 s, `✍`
during `/compact`. Debounced 700 ms per message so rapid tool calls do
not hit the rate limit; final state always written. All from Telegram's
allowed reaction list (`reactions.go:41-43` warns ✅/❌ are not). Owner Q3.

### F6. Live status line — value medium-high, effort M, risk medium

One header line above the D024 steps in the draft and in the placeholder:
`⏳ 0:42 · 5 steps` while working; `в очереди: 2-й` while the turn waits
behind another conversation of the chat (queue position, new read-only
accessor on the queue); the D026 wait notice stays the first state.
Elapsed time updates on the existing 1 s tick, so no extra API calls on
the draft path; on the edit path only every 5 s (*call*, edit budget).
No model output, no paths in the header.

### F7. Pickers through the Router — value medium, effort M, risk low

`/model` and `/provider` without arguments reply with buttons (current one
marked, `style: primary`); a press dispatches `model <name>` /
`provider <name>` via the Router and edits the same message to the result
(worker `core.ts:343`, OpenClaw `model-buttons.ts`). Needs
`control.Response.Buttons [][]control.Button{Label, Command, Args, Style}`,
frontend-agnostic (CLI ignores them, prints labels). That is a change to a
D011 interface: record as ADR (D028, *call*).

### F8. Confirmations for risky commands — value medium, effort S after F7

`/clear`, `/provider` (it clears the main session), `/chats unregister`,
`/container restart|stop` answer "Are you sure?" with `Yes`
(`style: danger`) / `Cancel`. The Yes button carries a one-time nonce
(`cf:<nonce>`) mapping in memory to the pending `control.Command` for
5 min; a restart forgets it ("expired, run the command again"). The
Router re-checks `Perm` when the confirmed command runs. List per owner Q6.

### F9. Paged long outputs — value medium, effort M

`/logs`, `/recall`, `/tasks list`, `/help`: one message with `‹ 1/4 ›`
buttons editing in place, instead of several chunks. Pages are computed
from the stored `Response.Text` kept 10 min in memory (*call*); expired →
re-run hint.

### F10. Reply, quote and forward context into the prompt — value high, effort S

If the user replies to a message, the prompt gets a short header:
`[in reply to <sender>: "<first 500 chars>"]`, or the exact `Quote.Text`
when the user quoted a part; forwarded messages get
`[forwarded from <origin>]`. Replying to one of gopod's own old answers
then works as "about this". Built in `handler.go` before enqueue; the data
already reaches `persistMessage`. Text only, redacted by the slog rules
when logged.

### F11. Media inputs — value medium, effort M

Albums: collect messages with one `media_group_id` for 1 s, enqueue once
with all paths. Audio files and video notes: transcribe like voice
(Whisper accepts both after download). Stickers: ignored with no reply
(*call*). Chat action `upload_document` / `record_voice` while sending
documents and TTS.

### F12. Commands menu by scope and language — value medium, effort S

Publish Public + ChatLocal commands for the default scope and add
OwnerOnly ones in `BotCommandScopeChat{owner}`; one `ru` variant via
`language_code` if Q4 picks Russian (worker `telegram-setup.ts:29-33`).
On `BOT_COMMANDS_TOO_MUCH` drop owner extras first (OpenClaw).

### F13. Rich messages for final replies (T7) — value medium, effort M, risk high

`sendRichMessage` with `InputRichMessage.Markdown` set to the agent's
reply: native tables, headings, lists and `<tg-thinking>`/details instead
of our HTML conversion. Behind `GOPOD_RICH_REPLIES=1` (default off until
the live check), private chats only, final reply only; on any 400 the
chat falls back to today's HTML path, sticky like drafts (`draft.go:91`).
D025 still applies: documents for >60-line code are sent first. Then
optionally `sendRichMessageDraft` for streaming. Owner Q5.

### F14. Proactive messages with buttons — value low-medium, effort S

Scheduled task results get `⏸ Pause` / `▶ Run now` (Router commands
`tasks pause|run <id>`); IPC messages stay plain. Related: CP-004.

### F15. Topic titles — value low, effort S, cost per use

Rename a new private-chat topic from its first message with
`editForumTopic`: a heuristic (first 4 words, ≤ 25 chars) costs nothing;
an LLM label costs a gated background run (OpenClaw does the latter).
Owner Q7.

### Rejected or deferred

- **Persistent reply keyboard** (worker): useful for menus in a consumer
  bot, noise for an assistant you type to. Deferred unless Q8 says yes.
- **Agent-authored buttons** (OpenClaw passes unknown callbacks back to
  the agent as text): needs a reply-tag protocol in `OutputPolicy`;
  deferred to after F3.
- **Ephemeral messages**: aimed at one user in a group; gopod is mostly
  private. Candidate later for permission-denied replies in groups.
- **Show diff** button: needs the runner to exec `git diff` in the
  workspace and a file reply; better after CP-003 (typed file replies).
  Owner Q10.
- **Checklists** (`sendChecklist`): business accounts only.
- **User reactions as feedback**: needs `message_reaction` updates
  (privacy toggle in groups); low value for one user. Q9.

## Decisions for the owner

1. **Q1 Reply buttons:** a row under every final reply, or only on the
   newest one in a conversation? *Recommendation:* newest only, previous
   cleared.
2. **Q2 Regenerate semantics:** new turn in the same session (the agent
   sees its first answer and is asked for another), or a fresh
   `RunFresh` without the session? *Recommendation:* same session; it is
   the only way to keep context, and the CLIs cannot rewind.
3. **Q3 Status reactions:** replace 👀/👍/👎 with the state machine?
   *Recommendation:* yes, with the stall states.
4. **Q4 UI language:** today mixed (English errors, Russian wait and
   lifecycle notices). One language, or follow `language_code`?
   *Recommendation:* Russian for everything the bot itself says, in one
   `strings.go`; command descriptions in both.
5. **Q5 Rich messages:** off by default until verified live, then on for
   private chats? *Recommendation:* yes.
6. **Q6 Confirmations:** which commands? *Recommendation:* `/clear`,
   `/provider`, `/chats unregister`, `/container restart|stop`.
7. **Q7 Topic titles:** none, heuristic, or LLM? *Recommendation:*
   heuristic only.
8. **Q8 Persistent reply keyboard:** *Recommendation:* no.
9. **Q9 User reactions on replies** as a signal to the agent:
   *Recommendation:* no for now.
10. **Q10 Show diff / show full output** for coding turns:
    *Recommendation:* after CP-003; until then "as file" covers long text.

## Controller calls (made here, reviewable)

- Callback data format `act:args`, ids only, ≤ 64 bytes, asserted in a test.
- Button state in memory with TTLs (steps 1 h, confirmations 5 min, pages
  10 min); turn ids in data where durability matters (Regenerate, Retry,
  Stop) so they survive restarts via the `turns` table.
- Every callback answered, also on panic or error (deferred).
- `control.Response.Buttons` as the only new control-surface type; ADR D028.
- Keyboard only on the last chunk of a reply; never on documents.
- Edit-path status updates at most every 5 s; reactions debounced 700 ms.
- Error cooldown 60 s per chat and kind.

## Risks

- **Rate limits.** Telegram allows about one message per second per chat
  (20/min in groups); `editMessageReplyMarkup`, reactions and chat actions
  all count. Mitigation: debounce, edit-path status every 5 s, keyboards
  cleared only once per new reply, reuse the 429 hold of D021.
- **Flicker.** Editing a placeholder's text and keyboard separately
  flickers; always send text and markup in one `editMessageText` call.
- **Drafts vs edits.** Drafts cannot carry inline keyboards (only the
  native Stop); buttons appear on the final message. Status on drafts is
  free (no edit budget); on the edit path it is not.
- **Groups.** Any member can press a button on a registered group's
  reply; that equals sending a message there. Owner-only actions are
  still refused by the Router. Stop in groups: anyone in the chat can
  stop a turn (*call*; matches "anyone can talk to it").
- **Stop accessibility.** Native Stop only exists in private-chat drafts;
  F2 closes the gap; a stale Stop after the turn ended toasts "already
  finished".
- **Durable turns.** Regenerate/Retry create new turns; a crash between
  the final send and the keyboard edit leaves a reply without buttons
  (accepted). Callbacks never touch an existing turn row except Stop.
- **Secrets.** Status lines, toasts and reply headers pass the D024
  redactor; callback data has ids only; the confirm text names the
  command, not its secret arguments (`/secret` gets no confirm button
  that echoes args).
- **Cost.** Regenerate and Retry are full agent runs and pass the D026
  gate; LLM topic labels would be background runs (Q7).
- **Rich messages.** New API, unknown client rendering, unknown limits;
  sticky fallback and a flag keep the blast radius to one chat.

## Verify live (real bot, private chat with topics, and one group)

1. Callback round trip under 1 s; `answerCallbackQuery` toast shows.
2. Stop button on the edit path in a group stops the turn; turn ends
   `stopped`; a second press toasts.
3. Regenerate in a topic answers in that topic; the old reply loses its
   keyboard; restart between reply and press still works (turn id).
4. `copy_text` copies on iOS, Android, Desktop; real length limit.
5. Button `style` colours render (or degrade) on each client.
6. Status reactions: allowed emoji accepted, no 429 during a 20-step
   tool turn; stall states appear on a slow turn.
7. Rich message with a table and `<tg-thinking>` renders on all clients;
   fallback works on a forced 400.
8. Reply/quote context reaches the agent (ask "what did I quote?").
9. Album of 3 photos → one turn.
10. Commands menu: owner chat shows owner commands, other chats do not;
    `ru` client sees Russian descriptions.

## Done means

Every button gopod shows does something within a second or says why it
cannot; no turn can only be stopped by waiting; errors tell the user the
next step; the owner never types a model name; and nothing above changed
who may do what.
