# Telegram capability diagnostics and mode-aware guidance

Date: 2026-10-25. Status: design only; implementation deferred. CP-017.

## Problem and verified evidence
A setting enabled in BotFather does not mean gopod receives or handles its
updates. In the deployed bot, read-only getMe reports guest queries and private
topics enabled, users allowed to create/delete topics, groups allowed, group
privacy enabled, inline and business support disabled. These are dated facts,
not hardcoded defaults. Bot-to-bot enablement is not exposed by the inspected
getMe fields and remains unknown without separate evidence.

Current bot.New calls the SDK constructor, whose getMe response is used for
identity validation; gopod does not retain a capability snapshot. allowedUpdates
contains only message, callback_query and stopped_message_generation. The default
handler drops non-message updates other than Stop. There are no inline, guest or
business dispatchers. Draft sending already has per-chat runtime fallback.

The pinned go-telegram/bot v1.27.0 models.User.SupportInlineQueries has JSON tag
support_inline_queries, while Telegram returns supports_inline_queries. A local
JSON fixture with the official field=true decodes false. Fix/test the decoder
or use a narrowly typed raw-response adapter before trusting this flag. Avoid
an unsupported claim that a dependency update automatically fixes it.

## Capability contract
Use a small typed manifest, not a general plugin registry. For each mode expose:
- Telegram setting: enabled, disabled, unknown; evidence source and checked_at.
- Required update subscription and whether gopod requested it.
- Implementation: unsupported, notice-only, partial, ready.
- Local policy and context permissions (registered chat, actor, group admin or
  business connection rights), plus required runtime/provider availability.
- Effective availability and an actionable explanation. Unsupported, disabled,
  forbidden, transient failure and unknown are distinct states.

getMe can reveal private topics, user topic creation, group invitation/privacy,
inline queries, guest queries and business connection capability. Use getChat /
getChatMember only when context rights matter; a global flag is not per-chat
permission. Business enablement is not an active connection or can_reply grant.
Do not infer bot-to-bot or all BotFather settings from getMe. Managed-bot access
settings concern managed-user access and do not replace a self BotFather dump.
No session tokens, raw updates, provider credentials or private chat titles
belong in a public report.

## Lifecycle and UX
Fetch a bounded-time snapshot at startup; failure is a degraded/unknown state,
not a reason to stop ordinary DM service or mark every mode disabled. Cache with
TTL and last-success time. Refresh on an owner command and selected capability
errors, with backoff/rate limits; do not poll every message. A diagnostic refresh
must not call getUpdates concurrently, reset cursor/allowed_updates, change a
webhook, toggle BotFather settings or send probe messages.

Route all commands through internal/control. Proposed /telegram shows a compact
available-modes summary for this authorized context; /telegram doctor refreshes
and returns detailed configuration to the owner. /help guest, /help inline and
/help topics give deterministic guidance without consuming LLM tokens. Full
configuration is owner-only; ordinary users see only relevant availability and
a private-chat help link. Refresh reports stale/error status honestly.

If Telegram does not deliver an interaction (for example disabled inline), the
bot cannot intercept it or respond there. Explain this in help/doctor rather
than promising universal automatic hints. For enabled-but-unimplemented modes,
opt in only to an explicitly designed notice-only handler: inline uses its own
answerInlineQuery route; guest uses its own one-response query route. No normal
owner runner, credentials, tasks or private memory may be inherited by a guest
query sharing a numeric chat id. Rate-limit notices and avoid surprise DMs or
bot-to-bot loops. Actual guest agent functionality remains a separate subphase
of CP-014; this spec does not enable it.

User messages containing unsupported media need the media capability matrix,
not a false instruction to change BotFather. Do not claim every limit is a
Telegram setting or treat an API 429/network/5xx as disabled capability.

## Implementation slices and acceptance
1. Correct decoder fixtures; typed capability snapshot, local manifest and pure
   effective-state resolver; startup cache with unknown/stale handling.
2. Scoped Router summary/doctor/help, context permissions and rate-limited refresh.
3. Explicitly approved notice-only mode dispatchers, with update subscriptions
   derived from actual handlers rather than a blanket receive-all subscription.

Test missing/false/true fields, SDK regression, failed refresh/stale cache,
Telegram-on+handler-off, update-filter mismatch, group/business rights, privacy,
public versus owner output, guest namespace separation and transient API errors.
Live acceptance compares doctor with read-only getMe and known BotFather settings;
toggling a mode or contacting another bot requires a deliberately authorized test.
No diagnostic feature was implemented or enabled during this research.

## Official references
- https://core.telegram.org/bots/api#user
- https://core.telegram.org/bots/api#getupdates
- https://core.telegram.org/bots/features#inline-requests
- https://core.telegram.org/bots/features#guest-bots
- https://core.telegram.org/bots/features#bot-to-bot-communication
