# Managed profile sections and concurrent edit protection

Date: 2026-10-26. CP-025. Status: **proposed design**, not implemented or enabled.
Planning wave: 3 prerequisite; size estimate: M.

[Proposal](../../proposals/CP-025-managed-profile-updates.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

Seed-only templates do not update existing custom profiles safely; parallel tasks/providers can
overwrite changes read at an older revision.

## Dependencies and boundaries

CP-020 profile policy; existing seeded AGENTS/CLAUDE files.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Separate trusted image/controller policy from editable persona/preferences and generated
  lesson sections. Apply equivalent semantics to Claude/Codex and to foreground/task/review
  turns; file names are implementation details, not authorization.
- Host-mediated reads return a version/hash. Patches require that version and exact bounded edit
  scope; stale writes return a conflict and require reread. Atomic replacement preserves
  unrelated custom content and managed section markers.
- Distribute versioned trusted policy to existing, new, resumed and fresh sessions without
  overwriting custom profiles. Native policy/tool grants are enforced outside prompt text;
  compare runtime policy fingerprints and serialize reconciliation under the proper slot.
- Workers/reviewers cannot mutate owner preferences or stable core through service tools. Owner-
  approved edits and approved learning trials have separate scoped writer roles. Unknown
  namespace/profile never inherits personal defaults.
- CAS coordinates cooperating writers; it does not stop arbitrary code holding a writable bind
  mount. Personal default editing behavior is preserved in the initial compatibility slice;
  stronger protected-group writes require CP-020's host/filesystem boundary before any
  security claim.

## Acceptance

- Two writers read one revision: first patch commits, stale second is rejected and custom text
  survives.
- Image policy update reaches legacy/resume/fresh sessions for both providers without rewriting
  unrelated preferences.
- Workers, forwards and forged owner/profile claims cannot acquire profile writer authority.
- Managed section collision, unsafe projection path and interrupted write retain original bytes
  and a recoverable conflict.

## Migration, retention and rollback

Inventory existing files and preserve backups/checksums. Seed only genuinely missing files.
Preview managed-section adoption/collisions, never silently replace a whole personal profile.

## Risks and decisions before implementation

Lost custom text; policy drift across providers; mistaking a hash check or high-priority prompt
for a sandbox.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

Autonomous personality replacement, changing owner goals, or full personal filesystem lockdown
as a side effect.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/profiles.js; docs/prompts.md; docs/memory.md`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
