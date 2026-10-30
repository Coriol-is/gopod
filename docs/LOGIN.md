# Private login delivery and group target confirmation

Login URLs, OAuth/device codes and raw CLI output are not public group content.
Private chats keep their ordinary interactive login flow. A group has no
sign-in of its own (D048, D049): `/login` from the configured owner in a
registered group starts no CLI and asks nothing. The owner receives the real step
in the private chat; the group receives one neutral line. Other members, bots,
unregistered groups and commands addressed to another bot (`/login@Other`) are
ignored. Redirects are limited to one per group per 30 seconds; the state is
in memory.

No URL, code, provider name or raw output is ever sent to a group. If private
delivery fails, no public fallback is sent.

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

## Group sign-in guidance (D048, D049)

A group has no sign-in of its own: the group runtime reads the owner's account (read-only, inference only). An
owner `/login` in a group (or the group login button) sends the owner the real step in the private chat
(`/provider <name>`, then `/login` there to refresh the account groups use) and the group one neutral line
without provider, account or error detail. No credential material is involved in either message. The earlier
private confirmation ("Authenticate group?") and the group-scoped login CLI path were removed by D049.
