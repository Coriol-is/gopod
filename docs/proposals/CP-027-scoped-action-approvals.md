# CP-027 — Exact scoped action approvals and publication grants

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-26 |
| Origin | Owner requested codex-agent adoption specs and first/later-wave proposals |
| Size | M |
| Planning wave | cross-cutting before integrations — candidate, not a change to the approved queue |
| Touches | action registry, approval store, private owner UI |
| Depends on | CP-020 broker authority; CP-002 uncertain effects; CP-019 private owner prompts |
| Spec | [Design](../superpowers/specs/2026-10-26-scoped-action-approvals.md) |

## Problem

A generic yes or a model's claim of consent is insufficient authority for a particular external
send, publication, recipient or changed payload.

## Proposal

Add the bounded capability defined in the linked design, preserving Go,
Claude/Codex behavior and personal/group authority separation. Delivery stages
and prerequisites are in the [adoption map](2026-10-26-codex-agent-adoption.md).
This proposal authorizes no implementation, credentials, data migration or rollout.

## Out of scope

Universal interception of arbitrary shell/browser actions, automatic payments/deletions, or
requiring confirmation for every reversible local step.

## Risks

Consent confusion, payload leaks in previews, replay and bypass through unmediated
credentials/integrations.

## Acceptance

- A prepared action sends nothing; exact valid owner approval permits one intended commit.
- Recipient/body/artifact/policy change, replay, expiry, forged callback/forward or wrong
  namespace prevents commit.
- Network ambiguity creates uncertain state and status-only reconciliation; no duplicate side
  effect is inferred safe.
- A valid scoped prior grant works without redundant confirmation, while a different
  destination/data class requires new authority.
