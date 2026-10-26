# CP-021 — Task outcomes, evidence and checkpoints

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-26 |
| Origin | Owner requested codex-agent adoption specs and first/later-wave proposals |
| Size | M |
| Planning wave | 1 — candidate, not a change to the approved queue |
| Touches | task/run store, status UI, provider result adapters |
| Depends on | CP-004 session scope; CP-002 delivery states |
| Spec | [Design](../superpowers/specs/2026-10-26-task-outcomes-checkpoints.md) |
| Plan | [Task reliability plan](../superpowers/plans/2026-10-26-reliability-tasks.md) |

## Problem

Native process exit, model completion, goal achievement, verified external effects and Telegram
delivery are different outcomes; a single done flag obscures partial work and unsafe replay.

## Proposal

Add the bounded capability defined in the linked design, preserving Go,
Claude/Codex behavior and personal/group authority separation. Delivery stages
and prerequisites are in the [adoption map](2026-10-26-codex-agent-adoption.md).
This proposal authorizes no implementation, credentials, data migration or rollout.

## Out of scope

Automatic universal verification, automatic task replay, cancellation of already completed
remote actions or a new orchestration engine.

## Risks

Overstating verification, unsafe replay and indefinitely reserved unknown work; reconciliation
needs explicit operator procedures.

## Acceptance

- Exit 0 plus model achieved remains model_reported, not host verified or delivered.
- Partial/blocked/unknown persist across restart and are visible by exact scoped task ID.
- Cancel-requested execution with missing exit proof cannot be reported settled or used to
  blindly replay effects.
- Foreign namespace IDs, forged verification fields and oversized checkpoints fail closed;
  streaming remains usable on both providers.

## 2026-10-26 source-backed refinement

The linked spec and plan now define the bounded initial Go slice, concrete
run/attempt identity, source/provider resolution boundary, crash/non-replay
policy and migration gates. Status stays proposed/design-only. Saving this plan
does not enable the feature, change live schedules or authorize implementation.
