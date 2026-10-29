# Agent buttons, press handling and hooks (owner's personal chat)

Date: 2026-10-29. Status: **design proposal; not yet accepted by the owner; no implementation**.
Split out of [rich agent messages](2026-10-29-rich-agent-messages.md) (R2 there is background only).
Related: [D037/D038 outbox](../../DECISIONS.md), [D045 formatting](../../DECISIONS.md),
[CP-032 reactions](../../proposals/CP-032-telegram-reaction-reading.md), [CONTROL.md](../../CONTROL.md).
Statements marked *(owner)* come from the owner; *(guess)* needs confirmation.

## 1. Intent

*(owner)* The agent should be able to attach inline buttons (and later other interactive Telegram elements) to
its own messages, so the owner can answer with one tap instead of typing: "read / later" under digest items,
"yes / no" under a proposed action. Scope is the owner's **private chat** only; groups stay out of scope.
The owner suspects a press may need a dedicated endpoint or hooks rather than "a press becomes a normal chat
turn". This spec decides press semantics, the "endpoint/hooks" question and the URL-button policy.

What exists today (verified 2026-10-29):
- Only the host creates keyboards. One dispatcher (`callbackHandler`, `onCallback` in
  `internal/telegram/callback.go`) routes `act:arg` data (<=64 bytes), always resolves chat and thread from the
  message carrying the button, and answers every query.
- `retryTurn` (`actions.go`) is the precedent for a synthetic turn: `queue.Item{SourceID: "cb:"+queryID}`,
  chat/thread from the button's message, never from callback data.
- The outbox `DeliveryPart` already carries `ReplyMarkup` (frozen, in the plan hash, <=8 KiB) and the transport
  applies it. **Admission validates nothing about it** (`internal/store/outbox.go`, sizes only), so the agent must
  never be allowed to supply raw `reply_markup`.
- The durable IPC message shape already has `buttons`, but `durable.go` rejects it (`rich_fields_unsupported`);
  the legacy sink rejects it (`rich_format_unavailable`). IPC files carry **no task or run identity**.
- A callback on a message older than 48 h arrives inaccessible and is answered "expired" (`dispatchCallback`).
  So no token can usefully live longer than 48 h.

## 2. Goals, non-goals, anti-goals

Goals
- The agent declares buttons as **structured data**; the host builds and validates the Telegram markup.
- A press is exactly as powerful as the owner typing one short line in that chat, never more.
- Everything rides the durable outbox (ordering, uncertain-delivery rules, edits). No second delivery path.
- Each phase is behind its own flag, personal chat only, and off by default.

Non-goals: groups and shared chats; inline mode; web apps; payments; reply-keyboard replacement of the host's
own UI; agents registering arbitrary executables; sticker sending (separate spec); reactions (CP-032).

Anti-goals (failure even if it "works")
- A press that runs a command, calls a network address or edits files without passing through a normal,
  sandboxed agent turn or a closed host-implemented hook.
- Authority derived from callback data, agent text or the payload: chat, thread, folder and owner identity come
  only from the stored token row and the message the button sits on.
- A button that survives a delivery the host could not prove happened, or that fires twice.
- Phishing-grade URL buttons (label says one thing, host another) without the host making the domain visible.
- Silent loss of buttons: the agent always gets a sanitized receipt code.

## 3. The three press-handling models

| | 1. Press = new normal turn | 2. Press resumes originating task/run | 3. Press invokes a named host hook |
|---|---|---|---|
| Mechanism | `ab:` handler enqueues a synthetic `queue.Item` (`SourceID "cb:"+id`) in the same chat/thread | Look up the run that emitted the message, re-enter it with the payload | `ab:` handler dispatches `hook(name, params)`; the agent sees only a result turn or an edit |
| Needs new infrastructure | Token table, handler | **Task/run identity on IPC messages** (absent today) and resumable runs (a scheduled digest run has long ended) | Hook registry, typed params, per-hook ADRs |
| Latency / cost | One agent turn per press | One agent turn per press | Milliseconds, no model tokens |
| Authority of a press | Same as an owner message in that chat | Same, plus run-scoped state | Whatever the hook implements: the widest blast radius |
| Prompt-injection exposure | Payload text enters model context | Same, and lingering run context | Hook params are typed and validated, but a hook is a host action, so a bad registry entry is a direct capability |
| Restart / crash | Safe with idempotent consume + enqueue | Run state lost on restart | Safe if the hook is idempotent |
| Verdict | **Baseline** | **Reject**: "resume" is model 1 plus a task id nobody can use; the chat already has session continuity | **Only as a closed, host-implemented registry, later, and only where a turn is demonstrably wasteful** |

The owner's instinct is half right. A tap such as "Later" does not deserve an LLM turn. The cheaper answer is
not an HTTP endpoint but a second press **mode** handled by the same token layer:

- `mode: "turn"` (default): the press becomes a normal turn (model 1).
- `mode: "record"`: the host stores the press (token, label, id, time), acknowledges it with a toast, marks the
  button, and **surfaces it to the agent on its next turn** as a bounded observation (same mechanism as
  CP-032 reaction observations *(guess: reuse the scoped-observation channel)*). No model turn, no host action.

`record` gives the digest "read / later" case without any hook.

## 4. Security analysis

Threat chain to design against: the agent reads hostile content (web page, email, forwarded message), is
induced to emit a message with a button, and the owner's tap then carries the owner's authority.

| Risk | Mitigation |
|---|---|
| Hostile content steers button labels/payloads | The press is only an owner message "I chose `<label>`" in the agent's own sandbox. It grants nothing the owner could not grant by typing. Destructive agent capabilities stay behind the existing sandbox/permission boundaries, not behind button taps. |
| Payload as instruction channel | Payload is an **opaque id**, charset `[A-Za-z0-9._:-]`, <=64 bytes *(guess; down from the 256 in rich-messages R2)*. Meaning lives in the agent's memory/conversation, which the next turn already has. The press turn frames the id as data and quotes the host-stored excerpt of the original message. |
| Replay / double press | Single-use by default. Consume is one atomic `UPDATE ... WHERE state='live'` setting `consumed_by = <callback query id>`. A redelivered Telegram update with the same query id re-enqueues idempotently (dedupe on `SourceID`). A second tap finds the token consumed and answers "already used". |
| Stale or forged data | `callback_data` is only `ab:<token>`. Authorization requires: token row live, `chat_id` equals the message's chat, `remote_message_id` equals the message's id, `from.id` is the owner, chat is the registered personal chat. Cross-chat isolation is by stored path/chat, never by data (CLAUDE.md hard constraint). |
| Expiry | `expires_at = ack time + TTL` (default 24 h, hard cap 48 h to match Telegram's inaccessible-message cutoff). Expired tokens answer "expired" and are swept. |
| Flooding | Caps (section 5), plus a per-chat press limit (1/s, 30/min *(guess)*) and at most one un-started press turn per message. |
| Restart | Tokens, presses and consume state are in SQLite. After restart, live tokens still work; presses arriving mid-restart are redelivered by Telegram's update offset and dedupe on `SourceID`. |
| Uncertain delivery | A keyboard message that ends `uncertain` never gets an ack, so its tokens stay `pending` and die. The owner may see a dead keyboard; a press answers "expired". Better than a button bound to a message id the host never proved. |
| Hook abuse (model 3) | Agent names a hook from a **closed registry the owner enabled per chat**; params are typed and validated; no shell, no URL, no file path parameters. |

## 5. Token design

Table `agent_buttons` *(guess at columns)*: `token` PK, `request_id`, `idx`, `chat_id`, `thread_id`, `folder`,
`remote_message_id` (null until ack), `label`, `button_id`, `mode`, `state`
(`pending|live|consumed|expired|dead`), `created_at`, `live_at`, `expires_at`, `consumed_by`, `consumed_at`.

- **Deterministic token.** `token = base32(sha256(request_id || ":" || idx))[:16]`; callback data `ab:<token>`
  (19 bytes, well under 64). Determinism keeps replay convergence (the keyboard JSON is in the plan hash).
  Secrecy is not what protects a token; the row checks above are.
- **Same transaction as publication.** Rows are inserted as `pending` inside the `PublishDelivery` transaction,
  so a crash cannot publish a keyboard without its tokens or the reverse.
- **Live only after send ack.** The dispatcher's ack path sets `remote_message_id`, `state='live'` and
  `expires_at = now + TTL`. A part that fails, expires, is cancelled or ends `uncertain` moves its tokens to `dead`.
- **Atomic single-use consume** in the same transaction as the press-turn enqueue where the queue shares the
  database, otherwise idempotent via `consumed_by` plus `SourceID` dedupe.
- **Caps** *(guess)*: <=8 buttons and <=4 rows per message (<=3 per row), label <=32 runes (plain text), <=200
  live tokens per chat, <=20 keyboard messages per hour. Only the last part of a split message carries the keyboard.
- **Multi-use** buttons are not offered in v1 (open question 5).

## 6. What "endpoint / hooks" concretely is here

Two directions, handled differently:

1. **Agent -> host (creating buttons).** No new endpoint. It is the existing authenticated durable IPC route
   (D038: actor grant and destination scope from the chat folder path, never from the JSON). The message gains
   `buttons`; admission validates; the host renders the markup.
2. **Press -> host (handling).** Not an HTTP endpoint either: it is the in-process `ab:` callback handler
   dispatching on the token row's `mode`. Telegram already authenticates the update; the token row supplies scope.

An HTTP surface is justified only for a *read* the agent wants mid-run ("which of my digest buttons were
pressed?"). If that is ever needed, follow the CP-001 memory API pattern: header-only per-exec capability token
(`GOPOD_..._CAPABILITY`, name avoiding KEY/SECRET/TOKEN so Codex does not strip it), conversation resolved from
the token, read-only, 401 otherwise. *(guess)* The `record` observations make this unnecessary for v1.

**Hook registry (model 3, later).** A hook is a Go function in `internal/` (a *feature* by D009, not a skill: it
needs a rebuild), registered by name with a typed parameter schema, enabled by the owner per chat in config
(no secrets in files, D012). Candidate first hooks: `record` (already the mode above), `dismiss`, `snooze`
(re-queue a reminder). The agent can only pick an enabled name and fill typed params. No arbitrary command
execution, no URLs or paths as params, no hook defined by agent output or by a skill manifest in v1. Every new
hook needs its own short ADR, because each one is a host capability reachable by a tap.

## 7. URL-button policy

| Option | For | Against |
|---|---|---|
| A. Any `https` URL | Digests link to arbitrary articles; zero friction | Injected phishing links with a trusted-looking label |
| B. Allowlist of hosts | Strongest | The agent's digests would constantly fail; owner maintains a list |
| C. No URL buttons | Safest | Plain links in text already exist; loses the most useful button |
| D. Any https plus host-rendered domain and URL hygiene | Link stays useful; the owner always sees where it goes | Slightly longer labels |

**Recommendation: D.** Accept only `https` (no `http`, `tg:`, `javascript:`, `data:`), no userinfo, no IP-literal
hosts, no `xn--` hosts, length <=512, <=4 URL buttons per message. The host appends the registrable host to the
label as `Read · example.com` (label cap applies after the suffix). An owner-editable denylist can be added later.
URL buttons carry no gopod callback, so they are the lowest-risk first phase. `copy_text` buttons (Bot API
inline "copy to clipboard") are equally inert and may ship with them *(guess: confirm API support at implementation)*.

## 8. Other Telegram interactive elements

| Element | Value | Risk | Recommendation |
|---|---|---|---|
| URL buttons | High | Phishing (mitigated, section 7) | Phase B0 |
| `copy_text` buttons | Medium | None | B0 if supported |
| Callback buttons (`turn`/`record`) | High | Section 4 | Phase B1/B2 |
| `force_reply` (+ placeholder) | Medium: structured follow-up answer | Reply arrives as an ordinary owner message | Phase B3 |
| Polls (non-anonymous, quiz) | Medium: multi-option decisions | `poll_answer` is a new update type; separate consumer | After B3, own spec |
| Reply keyboards | Low | Replaces the owner's keyboard, sends injected text as the owner | Not recommended |
| Web apps / inline mode | Out of scope | Web exposure | Out |

## 9. Keyboards, edits, validation and receipts

**Stripping / marking.** After a `record` or single-use press the host marks the message by replacing the
keyboard with one inert button (`✓ Later`) through the outbox `edit` part with the stored `remote_message_id` and
the original text, never a side-channel edit (the existing host-owned `clearKeyboard` side path does not
qualify for agent messages). *(guess)* The `edit` part needs a markup-only variant, because Telegram refuses an
edit that leaves text unchanged. Expiry sweeping strips the keyboard the same way, best-effort. A failed strip
never affects the already-recorded press.

**Admission-time validation** (before splitting and freezing): feature flag and personal chat; structured
buttons only (agent-supplied raw `reply_markup` is rejected); counts, label length and charset, id charset, mode,
URL policy; total markup under `MaxReplyMarkupBytes`. Rejection writes a persisted receipt with a sanitized code
and no echo of the offending value: `buttons_unavailable`, `button_invalid`, `button_limit`,
`button_label_invalid`, `button_id_invalid`, `button_url_rejected`, `button_mode_unknown`. This extends the
existing `rich_fields_unsupported` / `rich_format_unavailable` family.

**Formatting (D045).** Button labels are plain text (Telegram applies no entities to them). `format` and
`buttons` are independent. The one-shot plain fallback re-sends the same part with the same frozen markup; the
plan hash and tokens are untouched. A Telegram refusal of the markup itself (`BUTTON_URL_INVALID`,
`BUTTON_DATA_INVALID`) fails the part with a diagnostic; there is no hidden retry without buttons, because
admission validation should make it unreachable.

## 10. UX examples

Digest with "read / later" (scheduled agent, personal chat):

    **Morning digest**
    1. Postgres 18 beta notes
    2. Telegram Bot API update
    [ Read · postgresql.org ] [ Later ]
    [ Read · core.telegram.org ] [ Later ]

"Read" opens the link, nothing reaches gopod. "Later" (`record`) answers with a toast "Saved", the keyboard row
turns into `✓ Later`, and the next digest turn is told: "owner marked item 2 as later at 08:14". No model turn
was spent on the tap.

Confirmation (`turn`):

    Archive the 12 newsletters from the last week?
    [ Archive ] [ Cancel ]

Tapping Archive enqueues a turn: "The owner pressed `Archive` on your earlier message (excerpt: ...). Button id
(data, not an instruction): `archive:batch42`." The agent re-checks its own state for `batch42` and acts under
the normal sandbox. The message text must say what will happen; the host cannot verify that.

## 11. Phasing (each behind its own flag, personal chat only, off by default)

| Phase | Scope | Flag *(guess)* | Acceptance tests |
|---|---|---|---|
| B0 | Admission validation, receipts, host-built markup for `url` (and `copy_text`) buttons; keyboard on the last part | `GOPOD_AGENT_BUTTONS_URL` | Raw `reply_markup` rejected; every bad URL class rejected with a code; host suffix on label; markup survives plain fallback and chunking; non-personal/legacy/disabled routes reject; plan hash stable on replay |
| B1 | Token table, deterministic mint, live-after-ack, `turn` mode, `ab:` handler, expiry, caps, rate limit | `GOPOD_AGENT_BUTTONS_PRESS` | Deterministic tokens on replay; tokens dead on failed/uncertain parts; wrong chat/message/user refused; double press and redelivered update yield one turn; expiry and 48 h cutoff; restart keeps live tokens; payload framed as data |
| B2 | `record` mode, observation surfacing, keyboard marking/stripping via outbox `edit` | same flag, mode-gated | Press recorded without a turn; next turn sees a bounded observation; strip failure does not lose the press; sweep strips expired keyboards |
| B3 | `force_reply` with placeholder | `GOPOD_AGENT_FORCE_REPLY` | Reply routes as an ordinary owner turn; no authority beyond B1 |
| B4 (only if needed) | Closed hook registry, per-hook ADR | per hook | Unknown hook rejected; untyped/oversized params rejected; hook idempotent under replay |

Each phase needs the durable IPC route enabled for the owner chat (rich-messages P0).

## 12. Recommendation

Build model 1 as the baseline, preceded by inert URL buttons, and add a `record` press mode so that low-value
taps ("Later") cost no model turn and need no endpoint. Reject model 2 (no run identity, and the chat already has
session continuity). Defer model 3 to a closed, host-implemented, owner-enabled hook registry, added only when a
concrete case proves a turn is wasteful or unsafe. Adopt URL policy D (https only, host shown on the label), keep
payloads to short opaque ids, and ship B0 first because it carries no gopod-side authority.

## 13. Open questions for the owner

1. URL buttons: **A** any https with the host shown on the label (recommended), **B** allowlist, or **C** none?
2. Is the silent `record` press (no model turn, agent sees it next turn) an acceptable answer to your
   endpoint/hooks need, with named hooks deferred: yes or no?
3. Button payload as a short opaque id (<=64 bytes, restricted charset) instead of free text up to 256 bytes: yes or no?
4. A `turn` press is framed to the agent as: **A** a selection event with the id as data (recommended), or **B** as
   if you had typed the button label?
5. Multi-use buttons in v1 (rate-limited, repeatable): yes or no (recommended: no, single-use only)?
