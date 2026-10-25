# CP-012 — Register additional agent providers and fallback choices

| | |
|---|---|
| Status | accepted |
| Date | 2026-10-25 |
| Origin | Owner requested saving extensibility specs; implementation is deferred |
| Size | M |
| Touches | future provider registry, capability validation, model/auth menus, confirmed quota choices |
| Depends on | CP-011, D028, D030, D031 |
| Spec | [Provider registry and routing](../superpowers/specs/2026-10-25-provider-registry.md) |

## Problem
`AgentProvider` is extensible, but application wiring, menu names and quota
fallback pairs explicitly list Claude/Codex. `Image()` also assumes a Docker
artifact. Adding an interface implementation alone does not make a new provider
available safely throughout the product.

## Proposal
Register provider descriptors and capabilities centrally; derive menus,
validation and explicitly configured fallback choices from that registry.
Use CP-011 runtime compatibility without conflating provider and sandbox.

## Out of scope
Adding a provider now, unattended failover, quota polling, new billing paths,
credential discovery, arbitrary executable commands from Telegram, or model selection research.

## Acceptance
- Existing Claude/Codex behaviour, sessions and confirmation policy are unchanged.
- One registration supplies user-facing selection and capability checks.
- Context/session identities remain scoped to chat/thread/provider and execution domain.
- Quota choices are allowlisted and confirmed; adding a provider never silently
  enables a paid request, runtime migration or a fallback loop.

## Next step
Implement after the runtime contract baseline is validated; add and qualify
individual providers through separate bounded tasks.
