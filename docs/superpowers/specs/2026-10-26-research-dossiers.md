# Source-backed research dossiers and reviewed reports

Date: 2026-10-26. CP-023. Status: **proposed design**, not implemented or enabled.
Planning wave: 2; size estimate: L.

[Proposal](../../proposals/CP-023-research-dossiers.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

A prose answer alone loses source selection, exact evidence and revision history, making
research hard to inspect or continue.

## Dependencies and boundaries

CP-004 research sessions; CP-003 artifacts; CP-002 delivery; CP-020 for groups.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Support focused research and explicitly requested deep research. Depth selection is a
  user/model workflow decision with disclosed budget, not a keyword guarantee. Planning-only
  requests produce plans, not automatic research jobs.
- Persist dossiers with scope, questions, inclusion criteria, actual search log, sources,
  retained extracted text/hash, versioned claims, supporting/contradicting passages, locators
  and report revisions. Keep this corpus separate from personal-fact memory.
- Research writes are limited to the assigned namespace/conversation/task. Workers cannot spawn
  arbitrary workers, modify profiles, schedule work, use personal connectors or publish
  externally. Group source access requires qualified CP-020 capabilities.
- A public-source fetch broker excludes credentials/cookies, private/LAN/metadata addresses and
  unsafe redirects, pins validated DNS addresses per connection, and enforces
  response/text/time budgets. Suggested initial caps: 8 MiB response, 250k extracted
  characters/source and 40 sources/dossier; verify Pi storage and hostile-input cases before
  enabling.
- The host checks that quoted passages match retained source snapshots. A separate fresh read-
  only review evaluates every referenced claim at the exact revision; rejected claims are
  omitted and uncertain claims stay qualified. Passage matching is host mechanical evidence,
  semantic support/coverage remain model-reported.
- Export Markdown plus optional PDF, source inventory and evidence/search log as immutable
  artifacts. Preserve request language, visible citations and limitations.
  Inaccessible/scanned/paywalled sources remain unavailable, not silently verified. Failed
  review/export retains the dossier but cannot claim a completed report.
- Continuation creates a new bounded authorized run referencing prior versions; it does not
  blindly resume old network/actions. Quotas cover collection, review and export, and user
  cancellation preserves collected evidence.

## Acceptance

- Fake/public fixture source -> exact claim/passages -> revision-bound review -> readable
  Markdown/PDF -> scoped artifact outbox.
- Invented quote, stale revision, missing review decision and unsupported claim cannot enter a
  verified-looking final report.
- SSRF, DNS rebinding, redirect to private host, oversized/scanned input and cancellation fail
  boundedly with retained limitations.
- Both providers, new/resume, private/group roles and artifact routes are evaluated separately;
  no benchmark or truth guarantee is inferred from a passing pipeline.

## Migration, retention and rollback

Add dossier tables without bulk importing chat history or private reports. Back up DB, extracted
corpus and artifact references together. Retain prior report versions during rollback;
stop/reconcile pending exports before old code runs.

## Risks and decisions before implementation

Additional subscription turns and disk use; malicious sources; fluent but unsupported synthesis;
PDF quality and incomplete coverage.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

Exhaustive truth verification, paywall/CAPTCHA bypass, private-account retrieval by default or
an unbounded research swarm.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/research.js; src/research-fetch.js; docs/research.md; test/research.test.js`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
