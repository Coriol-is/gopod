# D067 task IPC claim implementation checkpoint

Base `0d18156`; branch `feat/task-ipc-claims`. Owner accepted D067. Implementation
is incomplete and MUST NOT merge/deploy: receipt attribution requires the proposed
[consumer identity amendment](../plans/2026-11-05-task-ipc-receipt-identity.md).

## Implemented, pending final qualification

- Task-only host-private spool under `DataDir/task-claims/<folder-hash>`; original
  filename, filesystem root identity, Store scope/epoch/revision, JID and privilege
  are bound. Production supplies Store SnapshotScope; scoped mutation and ACK
  validate inside SQLite transactions. Standalone callers without that callback
  have only filesystem/JID binding and are not equivalent production evidence.
- Atomic same-filesystem claim before read; no EXDEV copy/delete fallback. Bounded
  immutable byte snapshot is synced before dispatch. Mutable source inode is
  unlinked after snapshot persistence. Transient read failures hold original bytes;
  nonregular/symlink/oversized requests get durable rejection outcomes.
- Existing journal lookups pin both identical and conflicting requests as pending
  until terminal outcome ACK, preventing retention from enabling a second mutation.
  Typed conflicts preserve the original journal hash for cleanup acknowledgement.
- Durable outcomes, archive/receipt completion, terminal ACK and cleanup. Neither
  success, error nor panic archives the inbox pathname of a replacement.
- Recovery rotation plus eligibility filtering before admission budget; 16 recovered
  and 16 newly admitted requests per folder/sweep. Source-bound errors and panics
  do not block independent namespaces. Required parent directory syncs are present.

Bounds: at most min(max(Retention,1),200) unresolved claims per folder, each host
JSON record <=4 MiB and request snapshot <=64 KiB + rejection sentinel byte.
These bounds do NOT cap a pre-snapshot agent inode: old open descriptors may grow
it while a crash/I/O failure prevents snapshotting. Such evidence is preserved,
not silently evicted; normal agent/filesystem quotas remain separate. No claimed
complete IPC backup/restore support; spool is new uncovered operational state.

## Independent review and corrections

First review found missing Store epoch binding, fixed-prefix recovery starvation,
panic escape, missing parent fsyncs and replay journal retention eligibility.
All received source fixes and focused regression coverage. Second review found
admission budget starvation and transient reads becoming terminal data loss;
those are also fixed. The receipt-publication ambiguity below remains a blocker.
No overall review PASS is claimed.

## Tests and evidence

Deterministic regressions cover identical/changed receipt-time replacement,
restart at prepared/claimed/snapshot/dispatched/outcome/receipt/terminal/ACK stages
with SQLite close/reopen, writes through an old source descriptor, receipt/archive
failure, stale filesystem/JID/owner/Store epoch, private spool refusal, pending
capacity, rotated recovery, held-replacement admission, panic containment, active
journal pinning and terminal recovery after journal pruning. Existing production
scheduler workflow tests remain unchanged.

Focused race checkpoint: IPC 14.381 s, Store 6.664 s; scheduler regex selected no
tests (reported explicitly, not scheduler acceptance). Final checkpoint `go build ./...`, `go vet ./...` and `go test ./...` pass.
Full race suites pass: IPC 89.474 s, Store 103.053 s, scheduler 19.707 s,
cmd/gopod 35.571 s. Independent final focused races: IPC 32.781 s, Store
15.816 s; remaining attribution blocker explicitly prevents overall PASS.
Required CI and advisory Sonar will be recorded separately; local checks do
not waive the unresolved contract/review gate.

## Remaining deterministic blocker

The retained diagnostic reports:
`old success republished while changed replacement is pending; exactly one task`.
It passed in 0.982 s, which proves the defect, not correctness. The temporary
`zz_claim_ambiguity_test.go` copy was removed unchanged. To reproduce, copy the
[fixture](fixtures/2026-11-05-task-claim-receipt-ambiguity.go.txt) into
`internal/ipc/zz_claim_ambiguity_test.go`, run
`go test ./internal/ipc -run '^TestDiagnosticTaskClaimReceiptPublicationAmbiguity$' -count=1 -v`,
then remove only the identical temporary copy.

The legacy caller treats status + filename as authority. It can observe the old
success before the later correct conflict. Neither passing regression suites nor
required CI can waive this independent review blocker. RequestHash plus matching
consumer behavior is proposed but not implemented or accepted yet.

## Published checkpoint

Draft PR [#177](https://github.com/Coriol-is/gopod/pull/177), production/test
checkpoint `6d0c891dfa6ecaaca46cdbc292fe3fcfe84758d5`. Required
[CI 42042595921](https://github.com/Coriol-is/gopod/actions/runs/42042595921)
was in progress at handoff, not claimed green. Sonar CLI for PR #177 returned
`NOT_COMPUTED`; no fresh analysis or findings inventory was available yet.
No suppressions, merge or deployment. Retain the feature branch/worktree for
accepted amendment implementation; do not clean up unresolved work.
