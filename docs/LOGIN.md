# Private login delivery and group target confirmation

Login URLs, OAuth/device codes and raw CLI output are not public group content.
Private chats keep their ordinary interactive login flow. Group authentication
requires an explicit request from the configured owner's Telegram actor and a
second confirmation in that owner's private chat. Other members cannot start
this cross-chat operation.

The private confirmation identifies the target group. Its opaque random token
is bound to actor, group, registered folder and a five-minute expiry; it is
single-use and checked again against current registration. Requests have a
30-second group cooldown and at most eight pending entries. Restart expires
in-memory confirmations; nothing is replayed automatically.

The hidden confirmation command passes through the control Router's owner-only
permission check and then requires the actual owner private chat/user. Only
successful private confirmation starts the CLI flow. If private delivery fails,
no public fallback is sent. Callback feedback is a short private toast; ordinary
group `/login` does not publish authentication material or service errors.

Display and authority are separate. A group flow displays URL/code/status in the
owner's DM, but uses the target group's folder/provider and `TierRegistered`.
It does not copy the owner's CLI home or turn the target into an owner runtime.
The owner must choose an account appropriate for that group's use; this delivery
feature is not an account-service isolation boundary.

Active auth input is intercepted before normal message persistence, media work,
model queueing and memory ingestion. Login commands are not persisted, including
the SDK's registered-handler path. Active-flow credential reply/quote metadata
is stripped before ordinary device-flow text can reach a model. Known pasted
device codes are consumed without becoming agent prompts. Arbitrary user-pasted
secrets outside a known active flow are not automatically classified or erased.

Raw login CLI stdout/stderr and transport errors are neither rendered to users
nor logged. Only controlled success/error text is delivered; local provider auth
status determines readiness rather than a substring such as “logged in” in
untrusted output. No account identity, inference or quota guarantee follows from
that local status. Credential-bearing private sends have a bounded dedicated
path without generic public/plain-text fallbacks or raw SDK error logging.

Interactive cleanup closes its attachment/EOF; verified termination of the CLI
process group and upstream token revocation are not promised. Managed auth-exec
lifecycle remains a separately qualified follow-up.

Existing global environment/vault/connected-service exposure is not fixed by
moving the login screen. The requested personal/group isolation audit and
remediation plan remain separate. No personal service account should be treated
as safe for a group merely because its login link was displayed privately.
Previously published group messages/authorization inputs are not deleted by
this patch; historical cleanup and any credential rotation are separate actions.

## Group sign-in guidance (D048)

A group has no sign-in of its own: the group runtime reads the owner's account (read-only, inference only). The
private confirmation above is unchanged. When a confirmed group flow finds no group profile, the owner receives
the real step in the private chat (`/provider <name>`, then `/login` there to refresh the account groups use) and
the group receives one neutral line without provider, account or error detail. No credential material is
involved in either message.
