# Scoped location context and expiry

Date: 2026-10-26. CP-029. Status: **proposed design**, not implemented or enabled.
Planning wave: 4 deferred; size estimate: M.

[Proposal](../../proposals/CP-029-scoped-location.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

Location can improve nearby/weather/route answers but must not become an inferred permanent home
address or a personal fallback in group chats.

## Dependencies and boundaries

CP-020 namespace privacy; CP-024 provenance; CP-027 cross-publication.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Accept authenticated direct location intent with conversation/namespace and event revision.
  Distinguish explicit saved default, temporary pin and live update; a forward is source data,
  not authority to set current/default location.
- Explicit place in the request takes precedence; proposed temporary TTL is 12 hours measured by
  host clock. Only explicit owner intent creates a durable default. Never invent coordinates
  from language/timezone/IP or infer home from repeated pins.
- Stale live edits cannot resurrect expired/retired location. Expose
  inspect/clear/default/temporary state without printing precise coordinates into public
  diagnostics or unrelated memory.
- Initial rollout is personal-only. Group-specific public place context may be added only with
  explicit group grants; it never falls back to owner location. Personal-to-group publication
  needs separate authority.
- Optional maps/geocoding/route providers are disabled until credentials, cost/budget, precision
  and retention are explicitly chosen. A no-key public fallback has its own limitations and
  privacy disclosure; this spec claims no provider pricing or service guarantee.

## Acceptance

- Fake clock tests default/temporary/expiry/request override; stale live events cannot
  reactivate cleared data.
- Forwarded pin and unauthorized sender cannot change defaults.
- Group request cannot retrieve or infer personal current/default coordinates.
- Missing provider/key/budget yields a clear limited result, not invented
  distance/route/location.

## Migration, retention and rollback

No automatic import of old pins or inferred defaults. Preserve current behavior until opt-in;
deletion/backup limits are explicit and account-side erasure is not promised.

## Risks and decisions before implementation

Precise location is sensitive; provider charges and external disclosure need explicit
configuration.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

First-wave work, passive tracking, inferred home, automatic paid map access or sharing private
coordinates with groups.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/location.js; test/location.test.js; shared-skill/google-maps/`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
