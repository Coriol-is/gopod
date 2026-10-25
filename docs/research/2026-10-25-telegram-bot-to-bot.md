# Telegram bot-to-bot feasibility

Date: 2026-10-25. Research only; no peer contacted or mode enabled.

## Verified platform behavior
Current official Bot Features documents bot-to-bot communication. In groups,
explicit bot commands/mentions and replies can be delivered when at least one
participant has the mode enabled. Receiving all other bots' messages additionally
requires the receiver's mode and group admin status or disabled group privacy.
Private bot-to-bot sends accept a bot @username and require both peers' modes.
Business-account routes have separate connection/access constraints. This is
not Guest Mode, inline search, or authority to click another bot's keyboards.
The older FAQ's blanket no-bot-visibility claim is superseded for these documented
contexts; prefer the current detailed feature description.

getMe's inspected fields do not expose bot-to-bot enablement. Current deployment
state is unknown. Managed-bot access settings describe user access restrictions,
not a complete view of a normal bot's BotFather toggles.

## Repository findings
Incoming registered-chat messages enter the ordinary runner path without a
separate IsBot trust gate. IsBot is recorded in stored messages, not a sufficient
execution/command policy. There is no peer registry, correlated request protocol,
maximum exchange depth, per-peer budget or dedicated low-privilege session.
Queue message deduplication limits identical Telegram updates, not a conversation
loop with a fresh message id on every reply. Chat registration is not peer
consent. The current IPC sink accepts numeric/canonical Telegram ids and cannot
send a peer @username as an arbitrary canonical id.

## Useful bounded applications (proposals)
- Delegate a human-selected question to a known specialist bot, then return its
  cited response to the originating topic. Separate API/tool access is preferable
  when it offers a stable structured interface.
- Integrate a cooperating catalog/service bot for a narrowly defined lookup or
  generation request, if that peer supports bot-to-bot and permits this workflow.
  Midlibrarian compatibility and any particular third-party command were not
  verified; receipt of its card does not prove direct integration is possible.
- Use two owned test bots to exercise a bounded coordinator/specialist protocol
  before considering third-party peers. This is not a reason to create a fleet
  for existing local scheduler, provider routing or tool calls.

## Minimum implementation contract
Default deny bot-originated execution until a server-owned peer policy allows
it. Bind a peer's numeric identity to its configured username; distinguish a
human forwarding a bot card from the bot acting as a sender. Do not grant owner
commands, credentials, shared private history or task creation from an external
bot message. Send only an explicit bounded task payload with approved disclosure.
Use dedicated low-privilege conversation/storage context and a request id,
origin topic, expected peer, deadline and maximum turns (initially one request/
response), byte/token/spend and rate limits. Ignore unsolicited bot replies and
self messages, validate response provenance, and never auto-forward an external
instruction to another bot. Stop on errors or budget exhaustion; expose an
owner kill switch. Treat response text/files/links as untrusted evidence.
A mode switch alone does not provide these controls.

Recommendation: ship diagnostics and faithful card inputs first; then a small
opt-in experiment with two owned bots or one confirmed service peer, if a real
workflow requires it. No benefit was established for replacing gopod's internal
queue, direct FreeFeed fetch or provider handoff with Telegram bot chatter.

## Sources
- https://core.telegram.org/bots/features#bot-to-bot-communication
- https://core.telegram.org/bots/api#user
- Repository: internal/telegram/handler.go, inbound_context.go and
  internal/telegram/ipc_sink.go; internal/ipc/authz.go.
