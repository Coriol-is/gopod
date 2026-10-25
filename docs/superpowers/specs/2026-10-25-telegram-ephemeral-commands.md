# Private service replies and commands in Telegram groups

Date: 2026-10-25. Status: accepted for specification; implementation deferred.
Proposal: CP-019. Owner clarified that ordinary bot messages stay public.

## Scope and visibility policy
In group/supergroup chats use ephemeral delivery for service messages, slash-
command responses and errors. Ordinary conversational answers and requested
public content remain normal public messages. This is not a global ephemeral
switch and not disappearing private-chat history. Private DMs retain their
normal delivery. Visibility is an explicit field on a reply target, independent
of formatting and independent of the caller's permission to change shared state.

The policy covers success/error text, unknown/denied command feedback when
eligible, validation failures, pickers, /tasks pages, confirmations, callback
results and associated files. A task summary requested via /tasks is a command
response; an ordinary completed digest explicitly targeted at the group remains
public. Scheduled/IPC outputs have no implicit recipient: public content follows
its requested destination, while service/errors need an explicitly authorized
private recipient or remain operator diagnostics. Do not guess a group member
or turn every ordinary agent reply into a private reply.

## Verified API and SDK behavior
BotCommand.is_ephemeral declares hidden user-to-bot commands. The user's
command can then be invisible to other group members and bots. Outgoing
privacy is a separate send operation: sendMessage uses
EphemeralMessageParameters.receiver_user_id with eligible callback/reply context.
Do not assume an ordinary sendMessage reply inherits privacy automatically.
Message.message_id is 0 for ephemeral messages. ephemeral_message_id identifies
them and can be reused after expiry/deletion; receiver_user is supplied when
relevant. Edits/deletion use the dedicated editEphemeralMessage*/
deleteEphemeralMessage methods, not normal message ids.

For a non-admin bot, creation requires an eligible action within 15 seconds:
a callback_query_id or reply_parameters.ephemeral_message_id. An administrator
can create ephemeral messages for a non-bot member at other times, subject to
Telegram rules. A plain public text/slash message is not documented as equivalent
to an eligible ephemeral trigger. Receiving an action is not an unlimited
permission to message that user privately forever. Non-admin action replies
target the originating client; admin delivery may reach multiple active clients.
Delivery is not guaranteed,
especially offline; ephemeral messages may disappear after time or client restart.
These are Telegram visibility rules, not end-to-end encryption or a prohibition
on the recipient taking screenshots. Keep the exact API generation's nested
parameter shape (10.3); do not copy older top-level receiver_user_id samples.

Pinned go-telegram/bot v1.27.0 has the command flag, receiver/ephemeral-id fields,
send parameters and dedicated edit/delete methods. Current gopod does not use
them: publishCommands populates only command/description, sendCommandResponse
uses normal sendMessage, callback/page paths edit normal message ids. Existing
storage/dedup assume (chat_id,message_id), and queue source ids use that pair.
Thus enabling only the menu flag is unsafe: private command content can be
stored in shared group history and its answer sent publicly.

## Implementation contract
Introduce an explicit transport ReplyTarget with chat, actor/recipient, optional
topic, visibility, normal or ephemeral message id, eligible trigger and deadline.
Populate it from verified Telegram fields and policy; JSON/LLM text cannot grant
recipient access. Pass it through control requests/responses and frontend
send/edit/error helpers, command paging, task state buttons and confirmations.
The frontend selects transport; internal/control remains Telegram-independent.

Bind every private button/page token to chat AND recipient/actor, originating
visibility and exact action/scope. An ordinary public group token may keep its
existing group policy; private tokens cannot be reused by another member,
including under a forged callback, expired id or edited message. Preserve
visibility for each page, formatting fallback and attachment. No generic
send-to-main, HTML-to-plain, topic-gone or command-error fallback may turn a
private result into a public send. Incoming command addressed to another bot
must not be handled as ours just by stripping its @suffix.

Add a separate ephemeral input namespace. Do not upsert all message_id=0 inputs
into a single messages row or deduplicate them as chat:0. Use update identity
and a scoped observation/generation key with actor/recipient; an expiring id
alone is not durable replay authority. Do not seed public group history,
conversation continuity, shared memory extraction or unrelated LLM turns from
private command text or its response. A private UI does not itself create a
new privileged owner session or per-user sandbox. Initial support is deterministic
commands, not private free-text agent conversations inside a group.

Commands that mutate chat-shared tasks/provider configuration keep existing
Router permissions and show the scope before confirmation; secrecy of a button
does not make an unauthorized action permitted. Owner-only diagnostics remain
owner-only. /login credential flows require their separate authorized private-DM
route, not broad group-context authentication. Logs/telemetry retain only bounded
metadata by default, not private command arguments/results.

## Timing, recovery and failure
Handle fast commands within the action window. For slow commands, send a private
acknowledgement promptly, then edit that ephemeral message while Telegram still
allows it. Updating an existing private message must be validated in real-client
acceptance beyond the original trigger window; do not claim the initial ack
creates unlimited delivery rights. If the message expires or eligibility ends,
stop private output. Offer continuation in a DM only through an explicitly
consented, scoped route whose link contains an opaque expiring token, not the
private content itself. Never send an unsolicited DM as a hidden fallback.

Unsupported permission/context, timeout, rate limit, expired message and restart
must not silently publish a response. Publicly visible fallback is prohibited
for private service/command/error content. A generic retry/error can be shown in
an authorized private channel or logged without sensitive payload. Callback
answer toasts may convey short generic status to the pressing user; they are
not a substitute for a full private response or durable delivery.

Pending private UI work after restart must not run side effects again to recover
an expired display. Persist operation outcome where the underlying action needs
it; separate mutation success from unconfirmed/expired response delivery. Do
not replay a private result through the normal Telegram outbox or an IPC group
message without its visibility/recipient contract. Default ephemeral command
publishing stays off until all required receive/send/storage/failure paths are
qualified. Command configuration in group scopes is explicit and must not
accidentally alter private-chat command UX.

## Delivery slices and acceptance
1. Reply-target/visibility policy, decoder fixtures and safe storage/dedup;
   fail-closed command/error transport with no public fallback.
2. Fast service commands, /tasks list/state/paging, pickers and private
   actor-bound confirmation lifecycle. Publish scoped is_ephemeral flags only
   for commands whose complete paths pass the gate.
3. Slow-command acknowledgement/edit/expiry and explicit consented continuation;
   further media/private agent interactions are separately scoped.

Tests use at least two group members plus an outsider: only the intended actor
can view/operate each private response, page and confirmation; ordinary bot
content remains public. Cover id=0 collisions, reused ephemeral ids, duplicate
updates, long/paged and malformed text, callback deadline, offline/expired
message, admin/non-admin, public unknown commands without eligible triggers,
network/429/5xx, denied operations, another-bot suffix, topic loss and restart
before/after a successful mutation. Assert zero normal public SendMessage calls
for private results/errors, including every fallback. Real multi-account Telegram
acceptance is mandatory: fake API success cannot prove audience visibility.
No ephemeral commands or BotFather settings were enabled during research.

## References
- https://core.telegram.org/bots/api#ephemeral-messages-and-commands
- https://core.telegram.org/bots/api#ephemeralmessageparameters
- https://core.telegram.org/bots/api#message
- https://core.telegram.org/bots/api#botcommand
- https://core.telegram.org/bots/features#ephemeral-messages
