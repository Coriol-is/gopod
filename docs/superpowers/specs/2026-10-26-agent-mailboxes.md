# Authenticated agent mailboxes and bounded delegation

Date: 2026-10-26. CP-030. Status: **proposed design**, not implemented or enabled.
Planning wave: 4 deferred; size estimate: L.

[Proposal](../../proposals/CP-030-agent-mailboxes.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

Independent assistants could exchange scoped requests/results, but peer messages must not become
owner authority or unrestricted cross-agent delegation.

## Dependencies and boundaries

CP-020 role/credential boundary; CP-027 send/task grants; CP-021 outcomes; CP-002 delivery.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Optional trusted peer identities/endpoints and authenticated per-mailbox capabilities are
  configured by the operator. Model text cannot enroll peers, supply arbitrary endpoints or
  obtain mailbox credentials. Transport and callbacks require fixed destination/network
  policy.
- Version envelopes with sender/recipient IDs, request/correlation ID, expiry, bounded content
  and optional artifact references. Incoming text is untrusted data. Authenticate and scope
  before persistence/download; unknown senders cannot create jobs or permissions.
- Received task requests remain proposals until exact owner acceptance or a valid narrow
  persisted delegation grant. Accepted work receives a fresh bounded task/role; peers cannot
  spawn worker trees, widen grants, select private namespaces or change provider/account
  authority.
- Outgoing content is an external publication governed by CP-027; no automatic private
  history/profile/credential attachment. A group peer cannot query owner private memory
  through a friendly personal assistant.
- Separate queued/sent/received/accepted/executed/goal/delivered outcomes; enforce
  deduplication, expiry, rate/budget and bounded fanout. Remote cancellation is not proven
  until acknowledged with appropriate evidence; local cancellation cannot undo completed
  remote effects.
- Do not equate this transport with Telegram bot-to-bot feature flags. No laptop SSH/full host
  execution skill is included automatically.

## Acceptance

- Forged/expired/replayed envelope, unknown peer and changed recipient fail before task
  execution.
- Known peer request remains pending until owner/grant acceptance; receiving text cannot mint
  approval or source authority.
- Offline peer/restart produces bounded retained/uncertain states without replay loops or task
  fanout.
- Cross-namespace data and unauthorized reply artifacts are denied; no secret token or private
  payload enters diagnostics.

## Migration, retention and rollback

No peers, mailboxes, credentials or background polling are enabled initially. Each
peer/destination and permitted data scope requires explicit onboarding; preserve audit and
reconcile pending external requests before rollback.

## Risks and decisions before implementation

Transitive delegation, public data leaks, loops, peer impersonation and remote side effects that
cannot be reconciled locally.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

First-wave implementation, arbitrary bot-to-bot execution, automatic peer discovery, SSH host
access or exactly-once remote work.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/mailbox.js; src/agent-mail.js; docs/agent-messaging.md`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
