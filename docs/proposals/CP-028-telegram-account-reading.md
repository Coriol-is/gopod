# CP-028 — Scoped Telegram user-account reading

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-26 |
| Origin | Owner requested codex-agent adoption specs and first/later-wave proposals |
| Size | L |
| Planning wave | 4 deferred — candidate, not a change to the approved queue |
| Touches | private read broker, optional account auth, source ingestion |
| Depends on | CP-020 credential isolation; CP-027 grants; CP-024 provenance; CP-007 private backups |
| Spec | [Design](../superpowers/specs/2026-10-26-telegram-account-reading.md) |

## Problem

Reading Telegram sources beyond the bot's retained authorized messages may be useful, but a
user's account session can expose far more than the requested channel.

## Proposal

Add the bounded capability defined in the linked design, preserving Go,
Claude/Codex behavior and personal/group authority separation. Delivery stages
and prerequisites are in the [adoption map](2026-10-26-codex-agent-adoption.md).
This proposal authorizes no implementation, credentials, data migration or rollout.

## Out of scope

First-wave implementation, account-wide search by groups, sending as the user, CAPTCHA/2FA
bypass or automatic history ingestion.

## Risks

Broad account credential authority, source privacy, client/API changes and accidental public
disclosure.

## Acceptance

- Synthetic account has allowed public peer and forbidden private peer; only allowed bounded
  reads succeed.
- Forged peer IDs, pagination escape, download oversize, expired session/challenge and forwarded
  commands fail safely.
- Agent filesystem/env/commands cannot retrieve account credentials; denied group queries
  disclose no private peer metadata.
- No write operation exists in the exposed registry; live account acceptance is separate
  explicit authorization.
