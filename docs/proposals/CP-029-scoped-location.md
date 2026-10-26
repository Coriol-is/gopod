# CP-029 — Scoped location context and expiry

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-26 |
| Origin | Owner requested codex-agent adoption specs and first/later-wave proposals |
| Size | M |
| Planning wave | 4 deferred — candidate, not a change to the approved queue |
| Touches | location records, trusted intake, optional map tools |
| Depends on | CP-020 namespace privacy; CP-024 provenance; CP-027 cross-publication |
| Spec | [Design](../superpowers/specs/2026-10-26-scoped-location.md) |

## Problem

Location can improve nearby/weather/route answers but must not become an inferred permanent home
address or a personal fallback in group chats.

## Proposal

Add the bounded capability defined in the linked design, preserving Go,
Claude/Codex behavior and personal/group authority separation. Delivery stages
and prerequisites are in the [adoption map](2026-10-26-codex-agent-adoption.md).
This proposal authorizes no implementation, credentials, data migration or rollout.

## Out of scope

First-wave work, passive tracking, inferred home, automatic paid map access or sharing private
coordinates with groups.

## Risks

Precise location is sensitive; provider charges and external disclosure need explicit
configuration.

## Acceptance

- Fake clock tests default/temporary/expiry/request override; stale live events cannot
  reactivate cleared data.
- Forwarded pin and unauthorized sender cannot change defaults.
- Group request cannot retrieve or infer personal current/default coordinates.
- Missing provider/key/budget yields a clear limited result, not invented
  distance/route/location.
