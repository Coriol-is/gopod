# CP-030 — Authenticated agent mailboxes and bounded delegation

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-26 |
| Origin | Owner requested codex-agent adoption specs and first/later-wave proposals |
| Size | L |
| Planning wave | 4 deferred — candidate, not a change to the approved queue |
| Touches | peer registry, mailbox transport, task acceptance |
| Depends on | CP-020 role/credential boundary; CP-027 send/task grants; CP-021 outcomes; CP-002 delivery |
| Spec | [Design](../superpowers/specs/2026-10-26-agent-mailboxes.md) |

## Problem

Independent assistants could exchange scoped requests/results, but peer messages must not become
owner authority or unrestricted cross-agent delegation.

## Proposal

Add the bounded capability defined in the linked design, preserving Go,
Claude/Codex behavior and personal/group authority separation. Delivery stages
and prerequisites are in the [adoption map](2026-10-26-codex-agent-adoption.md).
This proposal authorizes no implementation, credentials, data migration or rollout.

## Out of scope

First-wave implementation, arbitrary bot-to-bot execution, automatic peer discovery, SSH host
access or exactly-once remote work.

## Risks

Transitive delegation, public data leaks, loops, peer impersonation and remote side effects that
cannot be reconciled locally.

## Acceptance

- Forged/expired/replayed envelope, unknown peer and changed recipient fail before task
  execution.
- Known peer request remains pending until owner/grant acceptance; receiving text cannot mint
  approval or source authority.
- Offline peer/restart produces bounded retained/uncertain states without replay loops or task
  fanout.
- Cross-namespace data and unauthorized reply artifacts are denied; no secret token or private
  payload enters diagnostics.
