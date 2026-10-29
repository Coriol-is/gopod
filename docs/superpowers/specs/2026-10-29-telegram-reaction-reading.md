# Telegram reaction reading, including custom emoji

Date: 2026-10-29. CP-032. Status: owner-requested specification; implementation and activation not started.
Scope includes private chats and groups, with explicit unavailable states where Telegram provides no supported transport.
Owner clarification: a bot without administrator access must behave correctly too.

## Product contract

Gopod can read observed reaction changes on known messages, preserve custom emoji identity,
and expose scoped feedback to the next conversation turn. A reaction alone does not start an
LLM run, send a reply, approve an action, complete a task, or create a durable memory/preference.
Removing a reaction removes that actor's observed selection; it is not a negative vote.
This is a reading feature. Existing outgoing progress reactions remain unchanged.

Ordinary messages and commands continue to work when reaction reading is unavailable.
The UI must distinguish unavailable, unobserved, partially observed and observed-empty.
Never claim “no reactions” merely because no updates arrived.

## Telegram evidence and feasibility

Checked against official [Bot API](https://core.telegram.org/bots/api#update),
[reaction events](https://core.telegram.org/bots/api#messagereactionupdated), and
[custom emoji lookup](https://core.telegram.org/bots/api#getcustomemojistickers).

- Reaction updates require administrator status and explicit subscriptions. Bot-generated reactions
  are excluded. Anonymous counts may be grouped and delayed by minutes.
- Actor events carry old/new selections, actor, chat, message and date; count events carry snapshots.
  Neither reaction event supplies a topic identifier or message body.
- Types include emoji, custom emoji ID and paid. Custom emoji metadata lookup accepts up to 200 IDs.
- Updates are retained for at most 24 hours. There is no documented Bot API reaction-history retrieval method.

**Inference/design boundary:** administrator status cannot be granted in an ordinary bot-user DM;
therefore private native-reaction delivery is unsupported by this documented transport. A future
API change requires fresh documentation and a live qualification before claiming support.
The [MTProto reaction API](https://core.telegram.org/api/reactions) is a different transport;
its existence does not grant the current bot access or authorize account login.

## Capability matrix and no-admin behavior

| Context | Read capability | Behavior |
|---|---|---|
| Registered ordinary private chat | `unavailable: private_transport_unsupported` | No native-reaction claims; regular conversation continues |
| Registered group/supergroup, enabled scope, confirmed bot administrator | `eligible_unverified`, then `observed` after an actual event | Process authorized new events; first event is not proof of complete history |
| Registered group, bot is ordinary member | `unavailable: admin_required` | No polling workaround, no repeated notices, no inferred empty state |
| Bot left/kicked or scope disabled/retired | `unavailable: scope_disabled` or `not_member` | No collection or agent-context injection |
| Membership lookup fails or status stale | `unknown: permission_unverified` | Preserve prior evidence with timestamp; do not infer demotion or zero reactions |
| Anonymous reaction-count event | Aggregate-only evidence | No fabricated user attribution |
| Channel/business/guest/unknown chat | `out_of_scope` | Ignore safely; no automatic registration |

Capability has separate fields: subscription configuration, scope policy, membership status/checked_at,
last observed event, and coverage gaps. `observed` means evidence exists, not continuous health.
Disabling chat privacy is not a substitute for administrator rights. Do not request unrelated rights,
promote the bot automatically, read via a user account, scrape clients, or change chat settings.
An owner/admin may grant administrator status manually; no particular extra administrative privilege
is required by this design beyond what Telegram documents for reaction delivery.

Add `my_chat_member` alongside the two reaction update types while the feature is enabled, to observe
self promotion/demotion/removal. Check self membership at startup for enabled groups, on explicit
diagnostic refresh and before an authorized group turn if the check is older than five minutes.
Coalesce checks, cap them globally and honor `retry_after`. Membership updates also update the cache.
This does not enable new groups or mutate the CP-020 isolation policy.

After demotion, mark a coverage gap and suppress context injection until eligibility is confirmed.
A delayed event already delivered by Telegram may be stored as historical observed evidence within
an enabled scope, tagged with its event/receipt times and permission uncertainty; it cannot restore
an eligible status. Unknown or disabled scopes are rejected before persistence or metadata lookup.
After promotion, admit new events but retain the gap; there is no historical catch-up promise.

Expose the capability in existing scoped `/telegram` diagnostics through `internal/control`.
In a non-admin group show “Reaction reading unavailable: bot administrator access is required”.
Do not emit an unsolicited public message on every failure or suggest that reactions are absent.
Reuse existing diagnostic authorization/delivery policy; this feature must not bypass CP-019 privacy.
There is no automatic button-based feedback substitute. Such feedback would be a separate, visibly
labelled interaction under the buttons spec, not a native Telegram reaction.

## Current implementation and required seams

- `internal/telegram/bot.go`: explicit updates currently include message, callback and draft Stop only.
- `internal/telegram/handler.go`: non-message updates currently return without reaction handling.
- `internal/telegram/reactions.go` / `status.go`: outgoing reactions only; do not mix with incoming feedback.
- `internal/store/messages.go`: message identity is `(chat_jid, tg_message_id)`.
- `go-telegram/bot v1.27.0`: `models/reaction.go` already models emoji/custom/paid and both update types.
- Existing offset middleware persists a cursor before handler work; SDK `get_updates.go` advances its
  internal cursor before processing completes. Merely moving the middleware write is insufficient
  for crash-safe ingestion. Unknown reaction discriminators lose extra fields in the SDK model.

No SDK upgrade is assumed. An implementation must prove durable raw admission before the next
`getUpdates` call acknowledges a batch. Add a narrow host-controlled polling/admission boundary
(or an equivalently verified SDK hook), rather than relying on asynchronous middleware ordering.
Never run two pollers. Preserve callback/message/Stop behavior and regression-test all three.
Persist admitted reaction records and batch cursor atomically. Other updates in the same batch
must also be durably admitted for later dispatch before advancing that shared cursor; a reaction-only
transaction cannot authorize skipping an uncommitted message/callback/Stop. Preserve their existing
replay semantics rather than replaying side effects blindly; explicit ignored/malformed records
must have a bounded disposition so a poison update cannot stall the poller. On database failure,
do not advance/acknowledge the batch; retry with bounded backoff and report degraded health.
A Telegram outage beyond retention still creates an irrecoverable coverage gap.

## Normalized event and identity

Version 1 event fields:

| Field | Contract |
|---|---|
| `bot_identity`, `update_id` | Stable numeric bot ID plus original update ID; unique ingestion key |
| `namespace`, `epoch`, `chat_jid`, `message_id` | Host-resolved CP-020 authority; never selected by actor/content |
| `event_kind` | `actor_change` or `count_snapshot` |
| `event_at`, `received_at` | Original Unix time and host time, both retained |
| `actor_kind`, `actor_id` | `user`, `chat`, or `unknown`; chat identity is never a person |
| `old`, `new` | Canonical sets for actor events, including empty set for removal |
| `counts` | Canonical type/count pairs for aggregate snapshots |
| `target_status` | `resolved`, `unknown_message`, `retired_scope`, `ambiguous_mapping` |
| `conversation_id` | Derived only from the exact host message index, nullable |
| `coverage` | `observed_only`, with interval/gap references; never assumed complete |

Reaction identity is a discriminated union: `emoji:<exact Unicode sequence>`,
`custom_emoji:<opaque string ID>`, or `paid`. Do not normalize variation selectors,
parse custom IDs as floating-point numbers, or collapse a custom emoji into its fallback emoji.
Unknown variants retain bounded raw JSON and type in the admission layer, remain opaque and
are excluded from interpreted feedback. Reject negative counts and invalid known variants.
Missing/both actor fields are represented as ambiguous/unknown, never attributed to the owner.
IDs for actors, chats and messages must use distinct typed keys.

## Storage, ordering and message correlation

All persistence lives in `internal/store`, using additive SQLite migrations:

1. `telegram_reaction_events`: immutable normalized evidence keyed by `(bot_identity, update_id)`,
   payload hash, admission disposition, scope, dates and bounded unknown-variant data.
2. `telegram_reaction_actor_state`: latest observed selection per scope/message/actor, including
   empty tombstones so a delayed add cannot undo a newer removal.
3. `telegram_reaction_count_state`: latest anonymous count snapshot per scope/message, independent
   of actor state. Replace snapshots; never add them together or sum them with actor observations.
4. `telegram_message_refs`: exact inbound and acknowledged outbound message-to-conversation mapping;
   include scope epoch, direction, part/delivery reference, timestamps and retirement marker.
5. `telegram_custom_emoji_cache`: metadata keyed by custom ID; no chat/user membership stored in cache.
6. Capability/coverage state: scoped permission observations and collection-gap intervals.

Apply evidence and derived state in one transaction. Same update ID/hash is a no-op; conflicting
payload is quarantined with an operator diagnostic. Order state updates by `(event_at, update_id)`;
older evidence may be retained but cannot overwrite newer state. Compare update IDs only as a tie
breaker, not an eternal globally increasing clock. Serialize concurrent writes transactionally.
`new` is the observed actor selection, not an increment; compute added/removed by set difference.
Disagreement between stored state and incoming `old` records a gap rather than inventing history.

Populate message refs from inbound persistence and every acknowledged outbound message path,
including chunked replies and durable outbox parts. A mapping must commit with the host ack where
possible. Direct-send crash before persistence remains unmapped; never guess a message ID.
Do not attach draft IDs to final messages. Editing preserves the same reference; deletion marks it.
For a multi-part answer retain per-message reactions; do not manufacture unique answer-level votes.

A reaction on an unseen message stays unresolved. Reconcile when an exact scoped mapping becomes
available. Never guess main thread, use the most recent turn, fetch arbitrary history, or copy
content from another chat to fill a gap. Epoch retirement prevents old group evidence from entering
new sessions. User reactions to both known user and bot messages can be stored; context selects
only targets relevant to the current conversation and existing authorization.

## Custom reactions

The ID is sufficient for correct capture and removal even offline. Optional metadata resolution uses
`getCustomEmojiStickers`; match responses by custom ID, not positional order. Store returned fallback
emoji/file IDs/format flags as descriptive metadata, never authoritative sentiment. Unknown art
renders as `custom_emoji:<id>`, not a guessed thumbs-up/down. A custom reaction that visually resembles
👍 remains distinct from Unicode 👍 and from other custom IDs.

Suggested implementation bounds: batches of 100 unique IDs, two concurrent lookups, positive cache
TTL seven days, negative TTL one hour, 10,000 cache entries with LRU eviction. Honor 429 delays;
timeout, omission, deletion or inaccessible metadata must not lose or block the underlying event.
Do not fetch sticker media, execute animation formats, render arbitrary HTML, or require Premium
merely to store received IDs. Sender/outgoing custom-reaction restrictions are separate from reading.
Paid reactions are opaque count evidence, not money received, a user vote or a spending instruction.

## Agent consumption and privacy

At the next ordinary authorized turn, add a host-labelled, bounded reaction context section for that
conversation only: target reference, actor kind, added/removed selections or anonymous count snapshot,
event time and evidence limitations. At most 20 target messages and 4 KiB serialized context per turn;
truncate explicitly, preserving full scoped evidence in the store within retention.
Do not persist this context as a synthetic user message or continually repeat an unbounded event log.
Current selections are labelled observations as-of their timestamps; not “new” on every turn.
Custom emoji labels, names and message excerpts are untrusted data, never instructions.

No feedback-triggered run, task approval, preference learning, cross-chat search or personal-group
transfer. A 👍 is not authorization to send mail, publish, restart, delete or spend. Anonymous counts
cannot identify a participant. A group reaction cannot alter the owner's personal memory.

Proposed configurable defaults: event retention 30 days, unresolved targets seven days, at most
10,000 retained target messages per scope; current state/tombstones expire with their target.
After eviction, events older than the recorded retention floor cannot recreate current state.
Store a coverage gap on capped admission; never silently present a partial result as complete.
Collect only admitted reaction fields, not complete User/Chat profiles. Redact actor IDs/content
from routine logs; diagnostics expose counts/status/timestamps. Backups include the new tables;
scope deletion/retirement follows existing retention policy and must include reaction evidence.

## Rollout and acceptance

Feature flag `GOPOD_REACTION_READING=1`, default off. Off means no new subscriptions, collection,
metadata calls or context. On still respects registered scopes and group enablement. Do not change
production flags/admin rights/group activation as part of merging this specification.

| Stage | Deliverable / gate |
|---|---|
| R0 | Confirm transport constraints, durable admission seam and message-index coverage; no live enablement |
| R1 | Typed events, migrations, actor/count state, custom-ID preservation, replay and recovery tests |
| R2 | Membership capability transitions, diagnostics, unresolved-target reconciliation and cache |
| R3 | Bounded conversation-context injection, scope/epoch isolation and outgoing-path regressions |
| R4 | Explicitly authorized controlled-group qualification; private/no-admin unavailable UX verified |

Required tests:
- Add/change/remove emoji and multiple custom IDs; custom vs Unicode identity; missing metadata.
- Duplicate/reordered/concurrent updates; same-second changes; conflicting duplicate; stale add after removal.
- Anonymous count snapshots (including empty), delayed counts and actor_chat without user attribution.
- Unknown variant, invalid union/count, SDK decode failure, poison batch and oversized input bounds.
- Crash before admission, after commit/before acknowledge, restart with replay, DB-full failure;
  concurrent message/callback/Stop must not skip an uncommitted reaction event.
- Known/unknown/deleted targets, chunks, topics, group epochs, private/group isolation and retention eviction.
- Bot member without admin: ordinary chat works, no false zero counts, no model calls or repeated notices.
- Promotion/demotion, missed membership update, failing/stale lookup, disabled flag and disabled groups.
- Live controlled group: actual ordinary and custom emoji add/remove, matching IDs and target mapping;
  revoke admin and verify unavailable/gap, restore admin and verify new events without invented catch-up.
- Private chat: show unsupported transport honestly; a synthetic handler fixture is not live delivery proof.

## Open boundary

Full native reaction reading in private DMs remains blocked by the documented transport. This spec
supports the private-chat capability/error contract, not unavailable data acquisition. If native DM
reading remains mandatory, evaluate a separately authorized CP-028 account-reader design or a future
Bot API capability; neither is silently bundled here. No paid provider calls or account sessions
are needed to implement the group reader and custom-ID support.
