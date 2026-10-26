# Exact scoped action approvals and publication grants

Date: 2026-10-26. CP-027. Status: **proposed design**, not implemented or enabled.
Planning wave: cross-cutting before integrations; size estimate: M.

[Proposal](../../proposals/CP-027-scoped-action-approvals.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

A generic yes or a model's claim of consent is insufficient authority for a particular external
send, publication, recipient or changed payload.

## Dependencies and boundaries

CP-020 broker authority; CP-002 uncertain effects; CP-019 private owner prompts.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Define brokered prepare -> approve -> commit actions with namespace/epoch, actor/task, action
  kind, exact destination, canonical payload/artifact hash, policy revision, expiry and one-
  shot nonce. Preparation has no external side effect.
- Show the actual intended content and destination to the authorized owner privately. Approval
  binds the exact digest and scope; changed content/recipient/revision or
  expired/replayed/forwarded approval fails closed. A model cannot approve its own action.
- Honor already authorized bounded workflows through explicit persisted grants (destination,
  action class, data-source scope, budget/rate, expiry). Do not repeatedly ask for the same
  valid authorization, and do not infer a broad grant from silence or old unrelated messages.
- Separate read and publish authority. Private agent access to group data does not authorize
  sending private-derived content back; publication needs an exact approved payload or a
  separately scoped publishing workflow. The approving DM does not change the target execution
  namespace.
- Commit uses the durable approval record and authoritative action registry. Record
  pending/committed/uncertain effects; unknown remote results are not automatically replayed.
  A local idempotency key is not a universal exactly-once guarantee.
- This contract governs only brokered action surfaces with upstream credentials held outside
  untrusted execution. Full-access personal shells/native integrations that can bypass the
  broker are not silently claimed to be covered.

## Acceptance

- A prepared action sends nothing; exact valid owner approval permits one intended commit.
- Recipient/body/artifact/policy change, replay, expiry, forged callback/forward or wrong
  namespace prevents commit.
- Network ambiguity creates uncertain state and status-only reconciliation; no duplicate side
  effect is inferred safe.
- A valid scoped prior grant works without redundant confirmation, while a different
  destination/data class requires new authority.

## Migration, retention and rollback

Existing ad hoc confirmations keep their original scope; never upgrade them into broad grants.
Legacy ambiguous actions remain uncommitted/uncertain until explicitly reconciled. No new
external action backend is enabled by saving this spec.

## Risks and decisions before implementation

Consent confusion, payload leaks in previews, replay and bypass through unmediated
credentials/integrations.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

Universal interception of arbitrary shell/browser actions, automatic payments/deletions, or
requiring confirmation for every reversible local step.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/action-policy.js; src/action-registry.js; docs/action-policy.md`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
