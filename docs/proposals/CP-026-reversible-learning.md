# CP-026 — Sourced reversible learning with host evidence

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-26 |
| Origin | Owner requested codex-agent adoption specs and first/later-wave proposals |
| Size | L |
| Planning wave | 3 after memory/profiles — candidate, not a change to the approved queue |
| Touches | lesson records/revisions, curator roles, feedback/rollback |
| Depends on | CP-024 provenance; CP-025 managed sections; CP-021 evidence; CP-006 idle scheduling |
| Spec | [Design](../superpowers/specs/2026-10-26-reversible-learning.md) |

## Problem

Useful corrections currently risk remaining isolated in transcripts; automatic prompt edits
without source authority or measured outcomes can entrench mistakes.

## Proposal

Add the bounded capability defined in the linked design, preserving Go,
Claude/Codex behavior and personal/group authority separation. Delivery stages
and prerequisites are in the [adoption map](2026-10-26-codex-agent-adoption.md).
This proposal authorizes no implementation, credentials, data migration or rollout.

## Out of scope

Training model weights, silent goal changes, unbounded autonomous self-improvement or copying
private lessons into shared source.

## Risks

Self-reinforcing errors, quota consumption and unwanted personality changes. A host receipt can
validate a measured check, not general usefulness.

## Acceptance

- Repeated sourced correction -> proposed lesson -> explicit trial -> matched host outcome ->
  possible promotion; every transition has checkable authority.
- Model-forged receipt, wrong revision, forwarded preference, permission expansion and foreign-
  namespace evidence are rejected.
- Rollback removes the current lesson from compiled context and projections without erasing
  audit; no silent revival.
- No evidence causes zero provider calls; cancellation commits no partial batch or coverage
  checkpoint.
