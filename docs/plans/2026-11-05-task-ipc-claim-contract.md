# Task IPC request ownership before receipt publication

Status: accepted by owner 2026-11-05 as D067; implementation in progress.
Design baseline: `1972dd3`; implementation baseline `0d18156`. This is a repair of the accepted
same-filename replay contract, not a new user-facing task feature.

## Verified failure

The [retained diagnostic](../reviews/2026-11-02-task-ipc-replay-race.md) was rerun
on this baseline: PASS (0.909 s), reporting `replacement archived, receipt absent,
exactly one task`. PASS demonstrates the defect. The temporary test copy was
removed byte-for-byte unchanged. No production mutation occurred.

`processTaskFile` reads `tasks/<name>`, commits the host task journal, publishes a
receipt, then `archiveFile` renames the pathname. After observing the receipt,
a caller may legitimately replace that path. Archival then consumes the new
request without processing it. The existing transaction prevents duplicate task
mutation but cannot recover a receipt for bytes never processed by the watcher.

## Accepted decision

Adopt a host-only durable claim spool for the task channel. Atomically take a
request pathname out of the writable inbox **before reading or dispatching it**.
Process and archive that claim exclusively; never archive the inbox pathname
as completion of a previous request. Restart must resume outstanding claims
before admitting another request with the same source and filename.

The new spool is persistent operational state outside all ordinary agent mounts.
It requires same-filesystem atomic rename, recovery and explicit backup/rollback
handling. This is the architectural choice requiring approval under CLAUDE.md;
ordinary source edits and regression tests do not independently require approval.
D067 records the owner acceptance.

| Option | Benefit | Cost / reason |
|---|---|---|
| Host-only claim before processing (recommended) | A replacement remains pending and gets its own success/conflict receipt; original filename replay identity stays intact | New persistent spool, recovery protocol and same-filesystem requirement |
| Durable receipt reconciliation with archive ownership | Could keep inbox layout and repair missing responses | Needs durable records for errors/conflicts and atomic ownership of exactly the consumed bytes; journal-only reconciliation cannot identify an unprocessed replacement |
| Receipt before archive plus inode/hash comparison | Small patch | Rejected: comparison followed by pathname rename still races |
| Archive before receipt, without recovery | Small patch | Rejected: process death loses the receipt with no remaining input |

## Required contract

### Binding and isolation

- Only `tasks` changes. Message/artifact delivery and D038's durable text route
  retain their own protocol. Do not silently generalize this change to messages.
- Bind a claim to host-resolved source identity, current namespace/epoch and the
  original request filename. Request JSON cannot select the source, spool path,
  privilege or recovery namespace. A remapped/retired namespace holds its old
  claim; it must never be replayed into the replacement namespace.
- Allocate a private claim slot and durably record its binding before moving
  the source into it. The singleton controller owns slots; existing slots are
  recovered, never overwritten. Use pinned no-follow directory descriptors.
- Locate the spool outside IPC mounts, including the protected group IPC mount.
  Refuse symlinks, nonregular payloads and unsafe spool directories. The elevated
  host-root agent remains outside this protection boundary, as in D058–D062.
- Require source and spool to support same-filesystem atomic rename. On EXDEV or
  an unsafe/unavailable spool, leave input pending and log a bounded diagnostic;
  do not fall back to copy-then-delete or execute without claiming.
- A rename prevents pathname replacement but does not revoke previously open
  write descriptors. Read at most 64 KiB + 1 from the claimed regular file and
  persist a separate immutable host-owned byte snapshot before dispatch. All
  retries/hash checks use that snapshot, never reread the mutable claimed inode.

### Processing and completion

1. Recover the bound claim slot, or durably prepare metadata and atomically claim
   a pending inbox entry. Sync the affected directories before any side effect.
2. Persist the bounded byte snapshot before parsing/dispatch. Crash before this
   step resumes the claimed file; no task side effect has been admitted yet.
3. Validate JSON and existing authorization rules against the original binding.
   Preserve stable schedule IDs and journal identity based on original filename,
   source folder and payload/scope hash; private spool names are not request IDs.
4. Run the existing transactional task journal. Identical bytes reuse its result;
   conflicting bytes receive the existing truthful conflict error. No new task
   mutation occurs on replay. An incomplete claim cannot be evicted by retention.
5. Publish the corresponding source-scoped receipt and archive only the claimed
   snapshot. Failure leaves the claim recoverable. Invalid JSON, permanent errors
   and exhausted retries must also retain ownership until their receipt/archive
   completion succeeds; the current ignored error-receipt failure is not enough.
6. Persist terminal completion before journal acknowledgement and spool cleanup.
   Recovery of terminal state repairs receipt/archive only, never re-applies the
   operation even if its journal row has subsequently become retention-eligible.
   Bind retained terminal receipt bytes to the same claim and do not trust an
   agent-editable receipt as proof of completion. Sync receipt/archive data before
   recording durable completion; do not infer durability from rename alone.
7. Release the slot only after completion. A replacement already in `tasks/`
   remains untouched and is admitted on a subsequent sweep. There is no requirement
   for the agent to wait for archival or preserve the old receipt before retrying.

The exact crash protocol must be demonstrated with tests before review. If the
implementation cannot meet these invariants, stop and revise this contract;
do not weaken ownership or silently broaden accepted replay semantics.

### Bounds and operational compatibility

- Keep existing directory/dispatch limits and journal retention. Add bounded
  claim admission/recovery; never delete an unresolved claim to free capacity.
  Process outstanding slots before new admissions, with per-sweep work limits.
- Account for claim snapshots and terminal records in storage bounds. Preserve
  errors and recoverable state when full; do not turn resource refusal into a
  false success or deletion of input.
- Existing inbox requests and journal records require no caller migration.
  Existing task receipt shape, filename and error redaction remain compatible.
- Old binaries cannot discover this spool. Drain claims before downgrade or keep
  task IPC disabled and preserve spool evidence; no blind rollback dispatch.
- Existing backups do not cover full IPC recovery. Document the spool explicitly
  in that limitation; no new claim of matched IPC restore or CP-007 completion.
  A future complete restore must capture spool and SQLite together.
- No deployment, real schedules, Telegram messages or provider calls are needed
  for local qualification; deployment remains separately authorized.

## Required deterministic qualification

| Case | Required result |
|---|---|
| Original receipt observed, removed, identical same-name replacement submitted before original completion | Replacement remains pending until processed; receipt returns; exactly one task |
| Same interleaving with changed payload | Conflict receipt, original task unchanged; no old success falsely attributed to changed bytes |
| Replacement before claim / during read / during receipt / during archival | Only the atomically claimed bytes are processed; a later inbox entry is never removed by old completion |
| Restart after claim, snapshot, task commit, receipt, archive, terminal marker, acknowledgement | Eventual truthful receipt without repeated mutation; unresolved evidence survives |
| Receipt/archive denied or full; acknowledgement failure | Recoverable hold, no source replacement consumption, no lost error/conflict response |
| Claim/source/results/archive symlink or nonregular file; namespace epoch rotation | No cross-namespace access; stale claims held; authorization and redaction preserved |
| Pending backlog/retention pressure | Bounded work; no unresolved eviction or replay after terminal journal pruning |
| Existing task workflow and security suites | Same public filenames/receipts, owner/group authorization and schedule IDs |

Convert the diagnostic into a regression asserting successful recovery, with an
explicit deterministic synchronization seam if the old resolver seam no longer
matches the new protocol. Do not merely increase fixture timeouts or wait for
source disappearance. Run full build/vet/tests, affected package races, independent
review and required CI including Docker isolation. Inspect Sonar as advisory.

## Implementation entry points after acceptance

- `internal/ipc/tasks.go`: separate public request identity from claimed content;
  completion and failure paths must retain/recover the exact claim.
- `internal/ipc/watcher.go`, `namespace.go`: bounded claim recovery/admission and
  descriptor-based ownership, with no change to message archival semantics.
- `internal/ipc/task_workflow_test.go` and dedicated claim tests: deterministic
  replacement, crash/restart, security and retention coverage.
- `internal/store/task_requests.go`, `internal/scheduler/ipc_requests.go`: preserve
  journal atomicity/retention; change only if required by the proven claim protocol.
- Scheduling documentation, backup/rollback notes, HANDOFF and BACKLOG: record the
  accepted ADR, implementation status, evidence and remaining deployment boundary.

## Qualification blocker discovered during implementation

The accepted unchanged-consumer promise cannot provide correct receipt attribution
after ambiguous publication. See the [proposed amendment](2026-11-05-task-ipc-receipt-identity.md)
and [implementation evidence](../reviews/2026-11-05-task-ipc-claims.md). The owner accepted that amendment as D068 on 2026-11-05.
D068 supersedes the unchanged-consumer guarantee; all other D067 invariants remain.
