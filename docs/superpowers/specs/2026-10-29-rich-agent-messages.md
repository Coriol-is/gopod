# Rich agent messages: formatting, buttons and stickers (personal chats)

Date: 2026-10-29. Status: **requirements accepted by the owner 2026-10-29 (decisions below); implementation phased and flag-gated**.
Scope: the owner's personal chat. Group chats are explicitly out of scope until the owner decides
(CP-020 keeps them closed anyway). Statements marked *(owner)* come from the owner; everything marked
*(guess)* is a proposal that needs confirmation.

## Owner decisions (2026-10-29)
1. Enabling the durable IPC route in production for the owner's personal chat, with a bounded live smoke, is approved (P0).
2. **Buttons are split out** into a separate spec ([agent buttons and hooks](2026-10-29-agent-buttons-and-hooks.md)): press handling probably needs a dedicated endpoint/hooks, so it is not part of this spec's implementation. R2 below is kept only as background.
3. Formatting: the agent chooses per message. `markdown` **and** `html` are supported; **`markdown` is the default** when `format` is omitted (`plain` stays available explicitly).
4. URL-button policy is deferred together with buttons.
5. Stickers: the agent may send **any sticker the bot has already seen**. *(guess, unconfirmed)* A sticker-only inbound message is treated like a captionless photo: it starts a normal turn with sticker metadata.

## Why

*(owner)* Agent messages look flat. The owner suspected that Telegram or the gateway escapes
formatting, wants to be able to ask the agent to use formatting on some tasks, wants the agent to be able
to add inline buttons and other Telegram extended formats, and wants stickers readable and writable.

What the code actually does today (verified 2026-10-29):

| Path | Behavior |
|---|---|
| Foreground final reply | `markdownToTelegramHTML`/`finalReplyHTML` (`internal/telegram/format.go`): bold, italic, inline code, fenced code (long blocks collapsed), links. Headings, lists, tables, quotes, strikethrough, spoilers stay raw text. Agent-written HTML is escaped. Streaming partials are plain text; formatting appears on the final edit. |
| Agent-initiated messages (scheduled tasks, digests; IPC file `{chatJid,text}`) | Legacy sink (`internal/telegram/ipc_sink.go`) sends **plain text**, no `parse_mode`. This is the case the owner most likely saw. |
| Agent instructions | The chat templates say almost nothing about what formatting Telegram renders, so the agent guesses. |
| Buttons | Only the host creates inline keyboards (pickers, confirmations, Stop/Retry/Login, reply actions, `/tasks`). The agent cannot. |
| Stickers | Not handled inbound (a sticker-only message never reaches the agent) and no outbound `sendSticker`. |

## Requirements

### R1 Formatting
1. A message from the agent declares `format`: `markdown` (**default**, owner decision), `html`, or `plain`.
   `markdown` goes through the host converter. `html` is accepted only after the host sanitizes it against the
   Telegram HTML allowlist (`b i u s code pre a blockquote tg-spoiler`, attributes limited to `href` and
   `expandable`, http/https/tg links only); anything else is escaped or rejected, so the agent can never
   inject arbitrary tags. Note the default change: agent-initiated messages that were plain text before are
   rendered as Markdown, so literal `*`/`_`/backticks in such text will now format; `plain` opts out.
2. The converter gains headings (rendered bold), unordered/ordered lists, blockquote (expandable when long),
   strikethrough and spoilers. Tables stay out of scope here (see
   `docs/research/2026-10-25-telegram-table-rendering.md`).
3. Applies to foreground replies (already Markdown) and to agent-initiated messages (new).
   **Two layers of fallback to plain text (owner decision: back off to plaintext on a send error):**
   (a) at **admission** the host converts, validates (converter + `chunkHTML`) and only then splits and freezes;
   markup it cannot validate is published as plain text from the start; (b) at **send time**, if Telegram
   explicitly refuses a formatted text part with a parse error (`bad_request`, "can't parse entities"), the
   dispatcher makes exactly **one** plain-text re-attempt of the same part. This is safe because an explicit
   refusal proves the message was not accepted, and it does not weaken the never-resend rule: ambiguous
   outcomes (timeouts, 5xx, restart in `sending`) still end `uncertain` and never fall back. The plain text is
   derived deterministically from the frozen payload (`htmlToPlain`), so the plan hash and immutability are
   untouched; the fallback attempt is recorded as its own attempt row with a `plain_fallback` marker, and the
   delivery diagnostic says "sent as plain text". This amends the D037 "no hidden second call" rule for this one
   explicit, recorded case and needs a D037 amendment when implemented.
4. The agent learns the supported subset from a short instruction snippet in the chat templates.
5. *(owner)* The owner can ask for formatting on particular tasks. IPC message files carry no task
   identity and the task schema has no format field, so a per-task default is **not buildable** without a
   task-identity channel. *(guess)* First cut: `format` is set per message by the agent, and the task prompt
   tells it to. Per the owner decision the default when `format` is omitted is `markdown`.

### R2 Agent buttons — DEFERRED to [a separate spec](2026-10-29-agent-buttons-and-hooks.md)
*Background only; not part of the first implementation.*
1. A message may carry up to 8 buttons in at most 4 rows: `url` buttons (http/https only) and `callback`
   buttons. *(guess)* Label ≤ 32 characters, payload ≤ 256 bytes.
2. Callback buttons are host-registered: the host stores `(token, chat, thread, task/run ref, payload, expiry)`
   and puts only an opaque token in Telegram's `callback_data`. Chat scope comes from the message the button
   sits on, never from agent text.
3. *(guess)* A press creates a normal user turn in the same chat/thread: `[button pressed: <label> | <payload>]`,
   attributed to the person who pressed. The payload is untrusted data. No press ever executes anything
   without a model turn. The press is acknowledged immediately (`answerCallbackQuery`) with an optional short toast.
4. *(guess)* Tokens expire (default 24 h), are bounded per chat (≤ 200 live), and can be single-use or
   multi-use; after a single-use press the host may strip the keyboard from the message.
5. Telegram's per-message limits, callback size and edit rules are respected by the host, not by the agent.

### R3 Stickers
1. Inbound: a sticker becomes agent input with metadata (emoji, set name, type). Static stickers may also be
   passed as an image (WebP); animated/video stickers are metadata only.
2. Outbound: the agent sends a sticker by reference to a host-maintained registry of known stickers
   (every sticker the bot has received; owner decision: any sticker already seen). No creation or modification
   of sticker sets.
3. Stickers are a separate delivery part kind with the same ordering/uncertain-delivery rules as text.

### R4 Later, not in the first cut
Polls, agent reactions, locations, native rich tables, edit-in-place updates of an agent message.

## Non-goals and anti-goals
- Non-goals: groups and shared chats; inline mode; web apps; payments; creating sticker sets; HTML from the agent.
- Anti-goals (would be failure even if it "works"): agent-controlled markup reaching Telegram unsanitized;
  buttons that act without a turn or outside the chat they sit in; formatting errors that drop a message;
  silently changing how existing scheduled digests look; a second delivery path that bypasses the outbox.

## Feasibility in personal chats (assessment)

**Verdict: feasible; no blocker in the personal chat.** The delivery layer already has what rich messages need,
the missing parts are small and additive, and everything can sit behind off-by-default flags.

| Part | What exists | Gap | Size |
|---|---|---|---|
| Delivery | Outbox `DeliveryPart` already carries `ParseMode` and `ReplyMarkup` (frozen JSON, covered by the plan hash); the transport applies both. Nothing validates them at admission today (only sizes). | Admission must whitelist `HTML` and validate buttons up front. A sticker needs a new part kind: schema `CHECK(kind IN ('text','edit','upload'))` migration (table rebuild), `validatePublish` case, `partHash` coverage, transport case. | S–M |
| Agent message contract | Durable IPC text route (`GOPOD_OUTBOX_IPC`) accepts `{chatJid,text}` via plain `json.Unmarshal`: unknown fields are accepted and silently ignored. | Additive optional fields `format`, `buttons`, `sticker`. Because extras are ignored on the legacy sink, rich fields must be **rejected explicitly** on every non-durable path (durable disabled, non-personal chat, non-owner folder) with a receipt code, never silently dropped. *(guess)* Extend this message shape rather than the V1 artifact request (V1 requests are gated by `artifactsEnabled` and no capability issuer exists yet). | S |
| Formatting | Converter exists (`format.go`, `chunkhtml.go`). | Subset extension plus tests; instruction snippet. | S–M |
| Buttons | Single callback dispatcher routes by action and answers every query. | Token table, one `ab:` handler, synthetic-turn creation, expiry/limits, keyboard stripping. | M |
| Stickers in | Media download pattern exists (photo/document). | Handler for `message.sticker`, metadata and optional image. | S–M |
| Stickers out | None. | Registry table, validation that a reference is known, part kind. | M |

**Hard prerequisite.** Rich messages from the agent should ride the **durable outbox route**, not the
legacy sink (which would need parallel formatting/button/sticker code and has no ordering or
uncertain-delivery handling). That route is built (PRs 1–3 of CP-002) but **disabled in production and not yet
smoke-tested live**. So the first step is enabling it for the owner's personal chat after the bounded live
smoke the HANDOFF already requires. Until then none of this reaches the owner.

**Risks.**
- Telegram HTML nesting rules and the 4096 limit: already handled by `chunkhtml`; the extended subset needs
  the same chunk-safety tests.
- Button spam/injection: bounded by caps, TTL and the "press = turn, never action" rule.
- A button press on an old message after a restart: tokens live in SQLite, so they survive restarts; expired or
  unknown tokens answer with a short "expired" toast.
- Sticker `file_id` stability: ids are bot-specific; the registry stores what the bot actually received.

## Design requirements found in review (must be in the plan)
- **Button tokens.** The keyboard JSON is in the plan hash, so tokens are minted **deterministically** from
  request identity + button index (random tokens break replay convergence), inserted in the **same
  transaction** as `PublishDelivery`, and become live only on **send ack**; a failed/expired/cancelled part
  kills them. TTL starts at ack, not publication (delivery expiry is also 24 h). Tokens live in SQLite so they
  survive restarts; single-use consume is one atomic statement in the same transaction as the turn enqueue,
  so a crash cannot lose or duplicate a press. A redelivered callback dedupes on `SourceID`.
- **Where buttons go.** Only the last part of a split message carries the keyboard.
- **Press-to-turn framing.** The pressed-button turn text labels the payload as untrusted data (quoted,
  length-capped, with the source message attached), and multi-use tokens have a per-chat press rate limit.
  Precedent: `retryTurn` in `internal/telegram/actions.go` enqueues a synthetic item with
  `SourceID "cb:"+id`; chat and thread come from the button's message, never from the data.
- **Receipt codes.** New sanitized codes for bad buttons, bad format and unknown sticker so the agent can
  correct itself.
- **Uncertain delivery.** A keyboard message in `uncertain` state is not resent (existing rule); its tokens
  stay inactive until an ack proves the message exists.
- **Keyboard stripping** after a single-use press must go through the outbox `edit` part with the stored
  remote message id, not a side-channel edit (that would be a second delivery path).
- **Inbound stickers.** A sticker-only message is dropped today and `TestSticker_Ignored` encodes it; the
  change is deliberate and must cover albums and reply-to context.
- **Legacy-path safety.** No behavior change for any path that does not opt in.

## Phasing (each behind its own flag, personal chat only)
1. P0: deploy current `main`, then enable `GOPOD_OUTBOX_DISPATCHER` + `GOPOD_OUTBOX_IPC` for the owner chat and
   run a bounded live smoke (approved by the owner).
2. P1: formatting for agent-initiated messages (markdown default, html sanitized), converter subset,
   instruction snippet, admission-time validation, explicit rejection of rich fields on non-durable paths.
3. P2: inbound stickers (registry fills from received stickers).
4. P3: outbound stickers (new part kind + migration + transport).
5. Buttons: separate spec; not scheduled.

## Remaining open questions
- Sticker-only inbound: model turn (my default) or context only — confirm when P2 starts.
