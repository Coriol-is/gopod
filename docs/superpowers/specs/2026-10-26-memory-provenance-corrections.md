# Versioned memory provenance, corrections and forgetting

Date: 2026-10-26. CP-024. Status: **proposed design**, not implemented or enabled.
Planning wave: 3; size estimate: L.

[Proposal](../../proposals/CP-024-memory-provenance-corrections.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

Current search/source strings and supersede support do not by themselves explain fact authority,
retain a correction chain or prevent forgotten evidence being re-extracted.

## Dependencies and boundaries

Existing memory/supersede/search; CP-020 namespaces; CP-025 projections.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Add namespace-bound stable memory IDs, revisions, typed source references,
  confidence/authority, validity/review dates and active/superseded/needs_review/retired
  states. Inferred facts never become direct-owner-confirmed because a model labels them user
  facts.
- Corrections require expected revision and preserve earlier audit. A source
  correction/retirement marks dependent derived memories for review; dependency traversal must
  remain namespace-scoped and bounded against cycles.
- Provide explain(id) with authorized provenance and forget_preview identifying exact scope,
  affected records and dependent contexts. Confirmed logical forgetting creates scoped
  tombstones and blocks re-extraction from the same source references; never infer cross-
  namespace deletion authority.
- Working retrieval/vector/FTS/context filters exclude retired records and qualify
  stale/conflicting sources. Existing native transcripts, exported files, backups and
  provider-side state may still contain copies: logical forgetting is not semantic unlearning
  or guaranteed physical erasure. Offer a separately authorized fresh-session procedure when
  appropriate.
- Keep SQLite authoritative and readable Markdown derived. Preserve the current search/vector
  infrastructure; add quality contracts rather than replacing it wholesale with the donor's
  owner-global memory model.
- Physical purge/backup expiry requires separate explicit retention/deletion decisions. Group
  memory cannot consult private owner facts; future one-way private reads do not grant reverse
  group access or publication rights.

## Acceptance

- Correct a sourced fact and inspect both revisions; stale derived records become review-needed
  and do not silently override the correction.
- Forget preview/confirm affects only selected namespace/IDs; repeat extraction from blocked
  source cannot resurrect it.
- Direct owner message versus forward/model inference have different authority and cannot be
  laundered through summaries.
- Legacy items remain searchable with unknown provenance; no fabricated source or owner
  confirmation is added during migration.

## Migration, retention and rollback

Add revisions/source tables; mark existing provenance unknown unless backed by real retained
references. Do not delete or re-embed all memories automatically. Coordinate vector/FTS
consistency and backup restore with tombstones.

## Risks and decisions before implementation

Graph complexity, overconfident facts, privacy leaks in provenance metadata and false promises
about forgetting all copies.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

Replacing the whole memory engine, accessing private data from groups, guaranteed account-wide
erasure or identity inference from weak evidence.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/memory.js; docs/memory.md; test/memory.test.js`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
