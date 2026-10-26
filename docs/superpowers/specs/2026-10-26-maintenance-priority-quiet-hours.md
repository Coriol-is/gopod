# Foreground priority and quiet-hour delivery

Date: 2026-10-26. CP-006. Status: **proposed design**, not implemented or enabled.
Planning wave: 1 support; size estimate: S–M.

[Proposal](../../proposals/CP-006-yielding-maintenance-and-quiet-hours.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

Maintenance must not delay interactive turns or corrupt context when interrupted; proactive
output needs a truthful delayed-delivery policy.

## Dependencies and boundaries

CP-002 durable delayed delivery; existing cancellation/admission.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Classify user turns separately from compaction/extraction/consolidation/learning and proactive
  notification work. New interactive intake cancels or parks same-namespace maintenance and
  re-arms it only after a bounded idle debounce.
- Canceled maintenance advances no partial checkpoint or destructive context replacement. Retry
  from a fresh source revision; preserve already committed batches and avoid duplicate
  application.
- First slice uses an operator-configured quiet window/timezone, not guessed host timezone or a
  fabricated current env setting. Per-chat windows remain a later extension. Direct user
  replies are exempt.
- Queue proactive output through CP-002 until the window ends, retaining original
  time/source/visibility and expiry. Coalesce or expire stale notices; do not flush an
  unbounded overnight backlog.
- Authentication challenges and transient private/ephemeral destinations must not wait past
  validity: expire or request a new challenge in the authorized private route. No public
  fallback. Existing one-minute extraction/cancellation fixes are baseline, not proof that
  this whole contract is implemented.

## Acceptance

- Foreground intake during maintenance reaches normal admission latency; no partial summary
  replaces conversation state.
- Delayed output retains source topic and timestamp; direct messages at night still receive
  immediate replies.
- DST/timezone transitions and expired private payloads are tested with a fake clock.
- Restart, repeated idle wakeups and cancellation do not create duplicate maintenance or
  delivery backlog.

## Migration, retention and rollback

Default rollout keeps quiet hours disabled until explicitly configured. Reconcile existing
maintenance checkpoints; do not delete context or enable model-driven periodic jobs implicitly.

## Risks and decisions before implementation

Starvation of maintenance; stale notifications; private reply eligibility expiring while
delayed.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

A new general priority scheduler or per-chat quiet-hour configuration in the first slice.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/service.js; docs/workflow.md; test/assistant.test.js`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
