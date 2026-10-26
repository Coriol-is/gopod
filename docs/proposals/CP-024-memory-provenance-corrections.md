# CP-024 — Versioned memory provenance, corrections and forgetting

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-26 |
| Origin | Owner requested codex-agent adoption specs and first/later-wave proposals |
| Size | L |
| Planning wave | 3 — candidate, not a change to the approved queue |
| Touches | memory store/API, source graph, context compiler |
| Depends on | Existing memory/supersede/search; CP-020 namespaces; CP-025 projections |
| Spec | [Design](../superpowers/specs/2026-10-26-memory-provenance-corrections.md) |

## Problem

Current search/source strings and supersede support do not by themselves explain fact authority,
retain a correction chain or prevent forgotten evidence being re-extracted.

## Proposal

Add the bounded capability defined in the linked design, preserving Go,
Claude/Codex behavior and personal/group authority separation. Delivery stages
and prerequisites are in the [adoption map](2026-10-26-codex-agent-adoption.md).
This proposal authorizes no implementation, credentials, data migration or rollout.

## Out of scope

Replacing the whole memory engine, accessing private data from groups, guaranteed account-wide
erasure or identity inference from weak evidence.

## Risks

Graph complexity, overconfident facts, privacy leaks in provenance metadata and false promises
about forgetting all copies.

## Acceptance

- Correct a sourced fact and inspect both revisions; stale derived records become review-needed
  and do not silently override the correction.
- Forget preview/confirm affects only selected namespace/IDs; repeat extraction from blocked
  source cannot resurrect it.
- Direct owner message versus forward/model inference have different authority and cannot be
  laundered through summaries.
- Legacy items remain searchable with unknown provenance; no fabricated source or owner
  confirmation is added during migration.
