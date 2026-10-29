# Agent buttons and hooks (deferred)

Date: 2026-10-29. Status: **placeholder; no design yet**. Split out of
[rich agent messages](2026-10-29-rich-agent-messages.md) by the owner.

## Why separate
The owner expects button presses to need a dedicated endpoint/hooks rather than the simplest
"press becomes a normal chat turn" model, and wants the URL-button policy decided together with it.

## Material already gathered (from the rich-messages review)
- Delivery already carries `reply_markup` (frozen in the plan hash); the transport applies it. Admission does
  not validate it yet.
- Callback dispatch is one router (`callbackHandler`, `onCallback`); `retryTurn` is the precedent for a
  synthetic turn (`SourceID "cb:"+id`, chat/thread from the button's message, never from callback data).
- Token requirements: deterministic from request identity + button index, inserted in the same transaction as
  publication, live only after send ack, TTL from ack, atomic single-use consume with the turn enqueue,
  per-chat caps and press rate limits, keyboard stripping through the outbox `edit` part, keyboard on the last
  part only, press payload framed as untrusted data.
- Prompt-injection chain: content the agent read can induce buttons whose press carries owner authority.

## To decide in this spec
1. Press semantics: a new turn, resuming the originating task/run (needs task/run identity on IPC messages),
   or a hook/endpoint that invokes a named handler.
2. What the "hooks endpoint" is, who authenticates it, and how it is scoped to the chat.
3. URL-button policy: any http(s) link, an allowlist, or none.
4. Behavior of keyboard messages in `uncertain` delivery.
