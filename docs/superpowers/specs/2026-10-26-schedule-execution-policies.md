# Schedule overlap, misfire and model-free reminders

Date: 2026-10-26. CP-022. Status: **proposed design**, not implemented or enabled.
Planning wave: 1; size estimate: M.

[Proposal](../../proposals/CP-022-schedule-execution-policies.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

A recurring job that misses several runs or overlaps itself needs an explicit policy; a simple
literal reminder should not consume model quota.

## Dependencies and boundaries

CP-015 existing scheduling; CP-021 outcome authority; CP-002 reliable notices.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Persist an explicit schedule kind: literal reminder versus agent job. A reminder stores
  authorized literal text/destination and publishes through the outbox without invoking a
  provider. It does not promise zero Telegram/network cost, only no model call.
- Use an atomic occurrence key containing task identity, schedule revision and nominal
  occurrence instant. Admission and advancement commit together. This prevents duplicate local
  admission, not exactly-once external effects.
- Overlap policies: skip (proposed new-job default), coalesce (at most one latest queued
  continuation), bounded queue as explicit opt-in. Running/cancel_requested/unknown execution
  still counts as occupied until settlement or documented reconciliation.
- Misfire policies: skip (proposed recurring agent-job default), coalesce latest, bounded catch-
  up with explicit maximum and grace/expiry. A one-time reminder retains at most one pending
  occurrence until its specified expiry. Never infer that all missed old jobs should run after
  restart.
- Preserve explicit CRON_TZ or a trusted configured timezone; ambiguous user intent requires
  clarification rather than guessed UTC/host time. Test the chosen cron library's DST/clock
  behavior and document duplicate-wall-time treatment before release.
- Optional max_runs/expiry/objective stops a workflow truthfully: exhausted is not achieved.
  Goal completion needs exact owner intent or an appropriate host checker, not a worker's
  prose. The existing /tasks panel shows these policies and next run/last skipped reason
  without exposing private payloads.

## Acceptance

- Repeated scheduler ticks/restart admit one occurrence; concurrent controllers must not claim
  unsupported distributed safety.
- Long-running and cancel-requested tasks exercise skip/coalesce/bounded queue without unbounded
  backlog.
- Clock jumps, DST and multiple overdue times exercise each misfire policy and expiry.
- A literal reminder reaches fake Telegram with zero provider invocations; no early main-slot
  acknowledgement counts as completion.

## Migration, retention and rollback

Retain historical behavior for legacy schedules until policy is explicitly selected or approved
in a migration preview. Version changes invalidate stale occurrence plans without resurrecting
sent reminders. Do not recreate the owner's live FreeFeed task while implementing this generic
capability.

## Risks and decisions before implementation

Policy changes can surprise existing schedules; clock/DST interpretation and late reminder
expiry require explicit defaults.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

Distributed workflow engine, automatic cloud billing or bulk rewriting existing schedules.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/scheduler.js; test/scheduler.test.js; docs/workflow.md`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
