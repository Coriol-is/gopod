# CP-015 — Make recurring work a usable agent capability

| | |
|---|---|
| Status | done |
| Date | 2026-10-25 |
| Origin | Owner requested a separate investigation of failed IPC scheduling, then clarified the goal is generic recurring-task capability rather than creating a FreeFeed digest |
| Size | M |
| Touches | IPC protocol/results, scheduler validation and mutation coordination, task store, agent guide/policy, task control |
| Depends on | D011, D020, D022, existing filesystem IPC |
| Spec | [Agent scheduling](../superpowers/specs/2026-10-25-agent-scheduling.md) |

## Problem
An agent guessed a legacy/snake_case task payload and received no actionable
feedback inside its container. The existing IPC sink also created active tasks
with next_run=0. Stale workspace instructions encouraged direct SQL even though
the owner sees only a database-file bind within a read-only parent directory.
The generic task capability was not discoverable or reliable end to end.

## Proposal
Provide an embedded scheduling guide and runtime reference for both existing
and new workspaces. Validate exact IPC requests, compute a real first slot,
return source-scoped receipts, and support task inspection and lifecycle.
Commit mutations and replay responses atomically; serialize dispatch with task
management. Keep host path-derived authorization and current delivery semantics.

## Out of scope
Creating a user schedule, picking a timezone for a particular digest, changing
mount permissions, enabling direct SQL mutations, automatic task-result delivery,
per-topic scheduled execution, or changing another service.

## Acceptance
- Existing/custom chat identity files survive while SCHEDULING.md is seeded.
- Cron/interval/once receive nonzero future next_run; explicit IANA cron zones
  work in the minimal image, including DST; malformed/unsafe files fail clearly.
- An agent can list/get/update/pause/resume/cancel/delete its own tasks through
  exact IPC JSON, with full usable task ids and result receipts.
- Foreign-scope actions are refused; mutation and receipt replay cannot repeat
  side effects or let a stale poll revive an updated/paused/deleted task.
- Filesystem result/archive failures preserve host-owned operation authority;
  special files and namespace symlinks cannot block or escape the watcher.
- Build, vet, unit/end-to-end workflow tests and targeted race suites pass.
