# Task outcomes, evidence and checkpoints

Date: 2026-10-26. CP-021. Status: **proposed design**, not implemented or enabled.
Planning wave: 1; size estimate: M.

[Proposal](../../proposals/CP-021-task-outcomes-checkpoints.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

Native process exit, model completion, goal achievement, verified external effects and Telegram
delivery are different outcomes; a single done flag obscures partial work and unsafe replay.

## Dependencies and boundaries

CP-004 session scope; CP-002 delivery states.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Store execution state independently of goal outcome achieved/partial/blocked/unknown and
  delivery state. Goal claims default to model_reported; the model cannot set host_verified or
  user_accepted. Host evidence is a separate typed receipt with its own
  checker/version/authority.
- Accept bounded structured outcome metadata through a provider-neutral side channel or final
  result adapter without replacing text streaming. Record checks/evidence
  references/limitations and a checkpoint: plan revision, last supported milestone, next safe
  step, unresolved effects. Do not invent progress not returned and committed.
- Bind every outcome/checkpoint to namespace/epoch, conversation, task and run. Commit final
  results, history and delivery references atomically with CP-002; a terminal model response
  cannot imply a confirmed Telegram send.
- Cancellation is requested before it is settled. Do not release the affected execution
  reservation or claim stopped while owned execution remains unknown; unrelated namespaces
  keep their independently safe admission limits. Unknown external effects require
  reconciliation and cannot be erased by clearing a status flag.
- Retain checkpoints across restart, but never blindly replay external actions. Default post-
  crash continuation is stopped pending reconciliation. Existing explicit owner authorization
  may cover bounded safe continuation only when scope/budget remain valid and no unresolved
  side effects require fresh consent; a model assertion or reused admission key is not
  authority.
- Status views show execution, goal authority, verification and delivery separately. This
  feature does not introduce a new general worker engine.

## Acceptance

- Exit 0 plus model achieved remains model_reported, not host verified or delivered.
- Partial/blocked/unknown persist across restart and are visible by exact scoped task ID.
- Cancel-requested execution with missing exit proof cannot be reported settled or used to
  blindly replay effects.
- Foreign namespace IDs, forged verification fields and oversized checkpoints fail closed;
  streaming remains usable on both providers.

## Migration, retention and rollback

Add nullable metadata; legacy results become unknown authority, never retroactively achieved.
Preserve old logs and pending uncertain effects. Back up before schema changes; old code must
not silently discard requested/unknown states.

## Risks and decisions before implementation

Overstating verification, unsafe replay and indefinitely reserved unknown work; reconciliation
needs explicit operator procedures.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

Automatic universal verification, automatic task replay, cancellation of already completed
remote actions or a new orchestration engine.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/outcomes.js; docs/task-outcomes.md; test/task-outcomes.test.js`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
