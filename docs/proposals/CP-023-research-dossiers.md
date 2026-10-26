# CP-023 — Source-backed research dossiers and reviewed reports

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-26 |
| Origin | Owner requested codex-agent adoption specs and first/later-wave proposals |
| Size | L |
| Planning wave | 2 — candidate, not a change to the approved queue |
| Touches | research store/tools, scoped fetch, report export, review role |
| Depends on | CP-004 research sessions; CP-003 artifacts; CP-002 delivery; CP-020 for groups |
| Spec | [Design](../superpowers/specs/2026-10-26-research-dossiers.md) |

## Problem

A prose answer alone loses source selection, exact evidence and revision history, making
research hard to inspect or continue.

## Proposal

Add the bounded capability defined in the linked design, preserving Go,
Claude/Codex behavior and personal/group authority separation. Delivery stages
and prerequisites are in the [adoption map](2026-10-26-codex-agent-adoption.md).
This proposal authorizes no implementation, credentials, data migration or rollout.

## Out of scope

Exhaustive truth verification, paywall/CAPTCHA bypass, private-account retrieval by default or
an unbounded research swarm.

## Risks

Additional subscription turns and disk use; malicious sources; fluent but unsupported synthesis;
PDF quality and incomplete coverage.

## Acceptance

- Fake/public fixture source -> exact claim/passages -> revision-bound review -> readable
  Markdown/PDF -> scoped artifact outbox.
- Invented quote, stale revision, missing review decision and unsupported claim cannot enter a
  verified-looking final report.
- SSRF, DNS rebinding, redirect to private host, oversized/scanned input and cancellation fail
  boundedly with retained limitations.
- Both providers, new/resume, private/group roles and artifact routes are evaluated separately;
  no benchmark or truth guarantee is inferred from a passing pipeline.
