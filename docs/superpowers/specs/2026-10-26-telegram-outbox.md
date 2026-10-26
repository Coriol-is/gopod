# Durable delivery and uncertain outcomes

Date: 2026-10-26. CP-002. Status: **proposed design**, not implemented or enabled.
Planning wave: 1; size estimate: M.

[Proposal](../../proposals/CP-002-telegram-outbox.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

A queued response or successful model turn is not evidence that Telegram received it. Process-
local IPC receipts cannot close a crash between network send and local acknowledgement.

## Dependencies and boundaries

Durable turns; CP-019 visibility policy for private group replies.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Persist immutable destination namespace/epoch, conversation/thread, original actor/task,
  visibility, payload reference, ordering key and expiry with every final delivery. States are
  pending -> sending -> sent | uncertain | failed | expired/cancelled; names are logical
  contracts, not an instruction to rename existing SQL enums blindly.
- Use a host-generated delivery ID and atomic final-result/outbox publication. On restart a
  sending row without authoritative remote acknowledgement becomes uncertain, not pending.
  Uncertain sends are not replayed automatically. Known unsent/retryable errors receive
  bounded retries with retry_after/backoff; an ambiguous connection failure is not classified
  as definitely unsent.
- Keep per-conversation final ordering and prevent streaming edits from also publishing a
  duplicate final answer. Streaming intermediate edits remain outside the durable queue. A
  final edit of a known placeholder and a new-message fallback must have one explicit delivery
  plan.
- Authentication challenges are special expiring references, resolved at send time only in the
  authorized private destination. Do not persist raw reusable login secrets as ordinary outbox
  text. Recheck revocation, topic retirement, quiet hours and private eligibility before send;
  never substitute a public or owner route.
- Expose queued, sent, uncertain and expired truthfully through task/status receipts; only
  confirmed Telegram acknowledgement records delivered history/message ID.

## Acceptance

- Legacy scheduled jobs that deliver via IPC do not acquire an additional
  automatic stdout/final-result send. One explicit delivery owner survives retry.
- Crash before send, after send but before DB ack, and during final transaction; only the
  ambiguous cases become uncertain and none are blindly resent.
- Fake 429 with retry_after and bounded 5xx retry; queue restart preserves original topic,
  order, visibility and expiry.
- Blocked/retired topic or expired private eligibility produces no alternate public send.
- One streamed final plus its IPC retry produces one durable logical delivery; no exactly-once
  network guarantee is asserted.

## Migration, retention and rollback

Add tables/columns without fabricating sent states for old messages. Existing ambiguous rows are
retained for reconciliation. Back up DB plus referenced artifacts under CP-007; rollback stops
sender first and reconciles all potentially sent rows.

## Risks and decisions before implementation

Unknown remote outcomes may require operator reconciliation. Outbox growth needs bounded
retention after audit; secret challenge handling must remain separate.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

Exactly-once Telegram delivery; putting every streaming edit in SQLite; general external-action
retries.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/store.js; src/service.js; src/telegram.js; test/reliability.test.js`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
