# Sourced reversible learning with host evidence

Date: 2026-10-26. CP-026. Status: **proposed design**, not implemented or enabled.
Planning wave: 3 after memory/profiles; size estimate: L.

[Proposal](../../proposals/CP-026-reversible-learning.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

Useful corrections currently risk remaining isolated in transcripts; automatic prompt edits
without source authority or measured outcomes can entrench mistakes.

## Dependencies and boundaries

CP-024 provenance; CP-025 managed sections; CP-021 evidence; CP-006 idle scheduling.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Default first slice is proposal-only: propose a bounded scoped lesson with original evidence,
  expected benefit and observable check, then obtain owner acceptance. Automatic trial
  installation requires a separately approved policy, not silence.
- Keep SQLite lesson/revision/coverage records authoritative and human-readable playbook
  projections derived. Allowed targets are explicit managed sections only; stable policy,
  permissions, budgets, credentials and owner goals cannot be learned edits.
- Preferences require direct authenticated owner statements, not forwards, summaries or inferred
  intent. Proposal and independent review run fresh read-only roles with only assigned source
  access; neither role can write profiles or manufacture host receipts.
- Accepted rules begin as unproven trials. Promotion requires a matching host outcome/check
  receipt for the unchanged candidate revision/hash; model review, task completion and a
  positive-sounding reply are not evidence of improvement. Regression or owner feedback
  retires the trial; retain rollback/audit history.
- Run bounded idle batches with no calls when no new evidence exists; yield to intake. A
  proposed initial policy allows one offered question per local day and a small explicitly
  budgeted curator batch. Questions respect quiet hours and uncertain notification delivery,
  without repeat spam.
- Lesson scope follows personal/group namespaces; no owner-global sharing of private learning
  into public groups. Forgetting invalidates dependent lessons and removes them from ordinary
  context; retained audit/copy limits remain explicit.

## Acceptance

- Repeated sourced correction -> proposed lesson -> explicit trial -> matched host outcome ->
  possible promotion; every transition has checkable authority.
- Model-forged receipt, wrong revision, forwarded preference, permission expansion and foreign-
  namespace evidence are rejected.
- Rollback removes the current lesson from compiled context and projections without erasing
  audit; no silent revival.
- No evidence causes zero provider calls; cancellation commits no partial batch or coverage
  checkpoint.

## Migration, retention and rollback

Start disabled/proposal-only with no historical auto-install. Inventory existing profiles and
preserve unrelated text; promotion/automatic questions need distinct configuration approval and
evaluation before rollout.

## Risks and decisions before implementation

Self-reinforcing errors, quota consumption and unwanted personality changes. A host receipt can
validate a measured check, not general usefulness.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

Training model weights, silent goal changes, unbounded autonomous self-improvement or copying
private lessons into shared source.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/learning.js; docs/learning.md; test/learning.test.js`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
