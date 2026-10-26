# CP-022 — Schedule overlap, misfire and model-free reminders

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-26 |
| Origin | Owner requested codex-agent adoption specs and first/later-wave proposals |
| Size | M |
| Planning wave | 1 — candidate, not a change to the approved queue |
| Touches | scheduler admission, schedule store, tasks panel |
| Depends on | CP-015 existing scheduling; CP-021 outcome authority; CP-002 reliable notices |
| Spec | [Design](../superpowers/specs/2026-10-26-schedule-execution-policies.md) |

## Problem

A recurring job that misses several runs or overlaps itself needs an explicit policy; a simple
literal reminder should not consume model quota.

## Proposal

Add the bounded capability defined in the linked design, preserving Go,
Claude/Codex behavior and personal/group authority separation. Delivery stages
and prerequisites are in the [adoption map](2026-10-26-codex-agent-adoption.md).
This proposal authorizes no implementation, credentials, data migration or rollout.

## Out of scope

Distributed workflow engine, automatic cloud billing or bulk rewriting existing schedules.

## Risks

Policy changes can surprise existing schedules; clock/DST interpretation and late reminder
expiry require explicit defaults.

## Acceptance

- Repeated scheduler ticks/restart admit one occurrence; concurrent controllers must not claim
  unsupported distributed safety.
- Long-running and cancel-requested tasks exercise skip/coalesce/bounded queue without unbounded
  backlog.
- Clock jumps, DST and multiple overdue times exercise each misfire policy and expiry.
- A literal reminder reaches fake Telegram with zero provider invocations; no early main-slot
  acknowledgement counts as completion.
