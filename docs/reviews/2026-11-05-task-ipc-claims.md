# D067/D068 task IPC claim and receipt identity implementation

PR [#177](https://github.com/Coriol-is/gopod/pull/177) merged as
`cbdd9e30373219a17dfb58468573381ef8cb1507`. Reviewed final head:
`fdc5ac7a767ef067495994c62c31302296b371bb`; base `0d18156`.
Merged production source and tests match the reviewed head. No deployment or
live-client migration occurred.

## Implemented behavior

- Task-only host-private spool under `DataDir/task-claims/<folder-hash>`; original
  filename, filesystem root identity, Store scope/epoch/revision, JID and privilege
  are bound. Production supplies Store SnapshotScope; scoped mutation and ACK
  validate inside SQLite transactions. Standalone callers without that callback
  have only filesystem/JID binding and are not equivalent production evidence.
- Atomic same-filesystem claim before read; no EXDEV copy/delete fallback. Bounded
  immutable byte snapshot is synced before dispatch. Mutable source inode is
  unlinked after snapshot persistence. Transient read failures hold original bytes;
  nonregular/symlink/oversized requests get durable rejection outcomes.
- Existing journal lookups pin identical and conflicting requests until terminal
  outcome ACK, preventing retention from enabling a second mutation. Typed
  conflicts preserve the original journal hash for cleanup acknowledgement.
- Durable outcomes, archive/receipt completion, terminal ACK and cleanup. Neither
  success, error nor panic archives the inbox pathname of a replacement.
- Recovery rotation and eligibility filtering before admission budget; 16 recovered
  and 16 newly admitted requests per folder/sweep. Errors and panics do not block
  independent namespaces. Parent directory entries are synced before completion.
- D068 receipts carry SHA-256 of the exact complete request snapshot. Invalid
  bounded JSON gets a hash-bound error; unreadable/oversized data cannot get a
  misleading prefix hash or success. The executable seeded scheduling example
  matches filename AND requestHash before accepting success or error.

RequestHash identifies historical submitted bytes, not current policy/epoch or
filesystem receipt authenticity. Identical replay intentionally accepts its prior
result. Query fresh state with a new operation, not a reused retained filename.

## Review and validation

Independent review **PASS**, including final test-only production-scope workflow
fixture delta. Early review found and corrected missing Store epoch binding,
recovery/admission starvation, panic escape, missing parent fsyncs, replay journal
retention eligibility and transient read errors becoming terminal data loss.
The receipt-attribution finding led to owner-approved D068, without weakening D067
silently. No source blocker remains under the amended matching-consumer contract.

| Check | Evidence |
|---|---|
| Full build/vet/all tests | PASS |
| Full affected races, final D068 source | IPC 70.370 s, cmd/gopod 32.825 s, chattmpl 1.198 s; unchanged Store/scheduler reused |
| Prior complete Store/scheduler races | Store 103.053 s, scheduler 19.707 s |
| Independent focused races | IPC 16.982 s, Store 7.237 s, chattmpl 1.361 s; actual seeded Node consumer ran without skip |
| Final production-scope workflow fixture | Real scheduler/SQLite scoped-path races PASS, 16.962 s |
| Required final-head CI | [42048402467](https://github.com/Coriol-is/gopod/actions/runs/42048402467): test and Docker isolation PASS, no rerun |
| Advisory Sonar | Exact `fdc5ac7`, analysis 2026-11-05 14:47:25 UTC, gate OK; 8 code smells, 0 bugs/vulnerabilities |

All eight Sonar findings were read through CLI API (one page, total 8): cognitive
complexity and a repeated literal, including test functions. The regular issue
command hit an active-organization mismatch; read-only CLI API returned the
complete project/PR inventory without switching account settings. No suppression.

Deterministic regressions cover identical/changed receipt-time replacement,
restart at prepared/claimed/snapshot/dispatched/outcome/receipt/terminal/ACK stages
with SQLite close/reopen, writes through an old source descriptor, receipt/archive
failure, stale filesystem/JID/owner/Store epoch, private spool refusal, pending
capacity, rotated recovery, held-replacement admission, panic containment, journal
pinning and terminal recovery after journal pruning.

D068 tests execute the actual JavaScript consumer extracted from seeded
SCHEDULING.md via Node, not a Go imitation. After the receipt-before-terminal
crash, changed replay ignores recovered old success and accepts only its own
conflict; identical replay accepts the original bound result. Exact whitespace/
newline bytes, malformed/unbound/foreign receipts, timeout and oversized rejection
are covered.

## Historical diagnostic and remaining client boundary

The [retained diagnostic](fixtures/2026-11-05-task-claim-receipt-ambiguity.go.txt)
reports `old success republished while changed replacement is pending; exactly one task`.
It passed in 0.982 s before D068, demonstrating the legacy status-only consumer's
attribution defect. It remains a useful demonstration of why old consumers are
unqualified: the current matching-consumer regression rejects that stale result.
No temporary diagnostic source remains in the tree.

Existing workspace guides are preserved by Seed. Operators must migrate their
old guide/scripts to filename+hash matching before claiming live acceptance;
host deployment alone does not establish that migration. See [acceptance](../ACCEPTANCE.md#d067d068-task-ipc-consumer-migration--2026-11-05).
No Telegram/provider or real-schedule acceptance was performed.

## Storage, backup and downgrade limits

At most min(max(Retention,1),200) unresolved claims per folder, each host JSON
record <=4 MiB and request snapshot <=64 KiB plus rejection sentinel byte.
These limits do NOT cap a pre-snapshot agent inode: an old descriptor may grow it
while crash/I/O failure prevents snapshotting. Preserve that evidence rather than
silently evicting it; normal agent/filesystem quotas remain separate.

The new spool is uncovered operational state under existing incomplete IPC backup
coverage. Complete IPC restore is not qualified. Drain claims before downgrade,
or hold task IPC and preserve spool plus SQLite evidence. Old binaries cannot
recover these claims. This does not complete CP-007 or authorize deployment.
