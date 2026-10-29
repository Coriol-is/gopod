# Rich agent messages: formatting, buttons and stickers (personal chats)

Date: 2026-10-29. Status: **requirements and feasibility only; no implementation or enablement**.
Scope: the owner's personal chat. Group chats are explicitly out of scope until the owner decides
(CP-020 keeps them closed anyway). Statements marked *(owner)* come from the owner; everything marked
*(guess)* is a proposal that needs confirmation.

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
1. A message from the agent declares `format`: `plain` (default, today's behavior), `markdown`.
   *(guess)* Raw agent HTML is not accepted: the host converts Markdown to the Telegram HTML subset, so the
   agent can never inject arbitrary tags.
2. The converter gains headings (rendered bold), unordered/ordered lists, blockquote (expandable when long),
   strikethrough and spoilers. Tables stay out of scope here (see
   `docs/research/2026-10-25-telegram-table-rendering.md`).
3. Applies to foreground replies (already Markdown) and to agent-initiated messages (new).
   The outbox transport never falls back at send time (a Telegram `bad_request` fails the part), so the
   fallback happens at **admission**: the host converts and validates the HTML (converter + `chunkHTML`)
   before publishing, and converts first, then splits, then freezes. Markup that cannot be validated is
   published as plain text, never as a part that can later be refused.
4. The agent learns the supported subset from a short instruction snippet in the chat templates.
5. *(owner)* The owner can ask for formatting on particular tasks. IPC message files carry no task
   identity and the task schema has no format field, so a per-task default is **not buildable** without a
   task-identity channel. *(guess)* First cut: `format` is set per message by the agent, and the task prompt
   tells it to; the chat-wide default stays `plain` so existing digests do not change unannounced. A global
   `markdown` default for owner-chat IPC is the alternative (open question 3).

### R2 Agent buttons
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
   (populated from stickers the owner sent and from sets the owner allows). No creation or modification of
   sticker sets.
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

## Suggested phasing (each behind its own flag, personal chat only)
1. P0: enable durable IPC for the owner chat after the live smoke (prerequisite, owner-authorized).
2. P1: formatting for agent-initiated messages + converter subset + instruction snippet + per-task `format`.
3. P2: agent buttons (token table, `ab:` handler, synthetic turn).
4. P3: inbound stickers.
5. P4: outbound stickers (registry + part kind).

## Open questions for the owner
1. May the durable IPC route be enabled in production for your personal chat, with a bounded live smoke? (P0;
   without it none of this reaches you. Everything else can be built behind flags against the fake transport.)
2. Button press: an ordinary new turn in the same chat (my default, small), or resuming the originating task/run
   (needs task/run identity on IPC messages, which the host does not have today)?
3. Formatting opt-in: per message by the agent (my default) or `markdown` as the default for owner-chat IPC?
4. URL buttons: any http(s) link, or an allowlist? (The agent can put arbitrary links under trusted text.)
5. Stickers: any sticker the bot has seen or only from sets you list; and should a sticker-only message start a
   model turn or be recorded as context only?
