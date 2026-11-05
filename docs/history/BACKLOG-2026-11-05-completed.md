# Completed and superseded backlog ledger — 2026-11-05

Archived from the active backlog after reconciliation at `a4078ec`. Pi release
`757faa2` includes all slices below. Parent proposals keep their remaining scope
in [BACKLOG](../BACKLOG.md); deployment does not close client/native acceptance.

## Completed implementation slices

| Slice | Merge | Evidence |
|---|---|---|
| Queue idle handoff / lost wakeup, #168 | `01cc7e0` | [Review and stress checks](../reviews/2026-11-04-queue-idle-handoff.md) |
| Final presentation parity, #169 | `c1966dc` | [Delivery review](../reviews/2026-11-04-delivery-resume.md) |
| Main/fallback control ownership, #170 | `dacefc0` | [Topic review](../reviews/2026-11-04-topic-resume.md) |
| Receipt-backed DB-ACK repair, #171 | `930586a` | [Reconciliation review](../reviews/2026-11-05-topic-reconcile.md) |
| Durable photo-to-document fallback, #172 | `a016b82` | [Delivery outcomes](../reviews/2026-11-05-delivery-outcomes.md) |
| D065 immutable code-inline alternatives, #173 | `0aa6628` | [Review](../reviews/2026-11-05-code-inline-alternatives.md) |
| Process-local successful-final ownership, #174 | `40f368f` | [Review](../reviews/2026-11-05-final-owner-handoff.md) |
| Stopped-result publication fence, #175 | `a63c324` | [Review](../reviews/2026-11-05-stopped-result-publication.md) |
| D066 durable Stop intent and late evidence, #176 | `51fbb7a` | [Review](../reviews/2026-11-05-durable-stop.md) |
| D067/D068 immutable task claims and matching receipts, #177 | `cbdd9e3` | [Review](../reviews/2026-11-05-task-ipc-claims.md) |

Reviews record source checks and required CI. #168–#172 were first deployed in
[d58cdd2](../deployments/2026-11-05-queue-delivery-ownership.md); #173–#177 followed
in [757faa2](../deployments/2026-11-05-stop-ipc-rollout.md). The old queue baseline
failure and task receipt/archive race are historical repaired defects, not active
work. The separate runner force-drain observation remains in the active backlog.

## Completed operations

- Pi controller-only release `757faa2`: exact-main CI, isolated readiness,
  controller/owner health, database integrity and unchanged state hashes verified;
  Telegram version confirmed. No worker deployment.
- D068: three scheduling guides migrated with originals preserved; actual matching
  Node consumer passed read-only list, identical replay and changed-byte conflict.
  Arbitrary external consumers and complete IPC restore remain unqualified.
- Backup cleanup: 18 old automatic database-only sets removed (~119 MiB);
  all 20 matched-stopped sets, three database-only anchors, manual/auth backups,
  guide originals and rollback images retained. This is not CP-007 retention automation.

All operational evidence is in the [release report](../deployments/2026-11-05-stop-ipc-rollout.md).
Earlier completed CPs remain in the [proposal registry](../proposals/README.md)
and stable roadmap milestone record; no duplicate implementation task is retained.

## Rejected and superseded choices

| Choice | Disposition / authority |
|---|---|
| Interrupt an ordinary run when a new user message arrives | Owner rejected in D053; ordinary input queues and explicit Stop controls cancellation. |
| D046 early cancellation during ordinary restart | Superseded by D054 drain-first restart; force remains explicit. |
| D067 unchanged-consumer/no-migration assumption | Superseded by D068 exact-byte filename+hash matching; legacy status-only receipt readers are unqualified. |
| 2026-10-25 queue and earlier November iteration recommendations | Superseded by the owner-approved 2026-11-04 priority and lowered backup position. |

Decision authority: [DECISIONS](../DECISIONS.md). These are retired choices,
not cancelled parent CPs. No CP registry row is explicitly rejected/superseded;
paused CP-037, deferred CP-004 credentials and proposed research remain recorded.

## Full historical snapshots

- [Backlog before housekeeping](BACKLOG-2026-11-05-before-housekeeping.md)
- [Handoff before housekeeping](HANDOFF-2026-11-05-before-housekeeping.md)
- [Roadmap before housekeeping](ROADMAP-2026-11-05-before-housekeeping.md)

Snapshots preserve original evidence and outdated recommendations. Use current
[BACKLOG](../BACKLOG.md), [HANDOFF](../HANDOFF.md) and [ROADMAP](../../ROADMAP.md)
for next actions.
