# Group mentions, registration and privacy-mode delivery

Date: 2026-10-25. Read-only incident analysis; no Telegram settings or access
rights changed by the investigation. Group registration was performed by the
owner through the existing command.

## Evidence and correction

The group is registered and the bot is a normal member. The inspected incoming
message containing the bot username was actually a slash command (its payload
starts with `/`), not an ordinary `@username` prompt. A command does not create
an agent turn. Initial classification based only on username occurrence and
message length was wrong; those fields cannot identify an ordinary prompt.

No new ordinary mention reached the persisted normal-message path during the
observation. Webhook was unset, ordinary-message subscription remained active,
and no polling conflict/authentication error was observed. This does not capture
or prove the contents of Telegram updates excluded by allowed_updates.

An isolated, temporary Go-overlay reproduction of a registered group message
with ordinary mention text and a mention entity created a durable turn in the
correct folder. Empty registration name/trigger fields are handled by IFNULL.
No Docker, model or live send was used; the temporary test is not a committed
production feature or a real-client acceptance result.

## Telegram behavior

With privacy mode enabled, Telegram documents delivery of explicitly addressed
commands, eligible general commands, messages sent via the bot and replies to
messages meant for it. Plain text merely containing its username is not listed
as an ordinary-message delivery guarantee. Administrators receive all messages;
disabling privacy requires re-adding the bot for the change to take effect.

Guest bots can be summoned by a user mentioning them; Telegram documents a
dedicated guest update and a limited reply opportunity. This describes the
interaction, not necessarily a separate UI mode deliberately chosen by the
user. Current gopod does not subscribe to/handle guest_message. Registration of
an ordinary chat does not install a Guest handler or authorize Guest callers to
inherit owner sessions, tools or private memory.

Sources checked on the date above:

- [Privacy mode](https://core.telegram.org/bots/features#privacy-mode)
- [Message delivery FAQ](https://core.telegram.org/bots/faq#what-messages-will-my-bot-get)
- [Guest interactions](https://core.telegram.org/bots/features#guest-interactions)

## Safe current guidance and follow-up

Use an ordinary Reply to an existing message from the bot in the registered
group to exercise the supported delivery route. Actual model execution may
still require that group's own runtime/auth readiness; this analysis does not
prove a model response or silently share owner OAuth credentials.

Do not silently grant administrator status or disable privacy to work around
the issue. Current gopod has no local mention-only gate for received registered
group text, so broadening Telegram visibility can also broaden agent triggers.

A future registered-group/Guest entry design needs explicit namespace, actor,
reply-target, registration and budget rules before enabling its subscription.
Unregistered-chat hints are service messages and retain CP-019's private-only
policy: ephemeral delivery needs eligible context/rights, no public fallback,
no unsolicited DM, and rate limiting. These remain future implementation scope.
