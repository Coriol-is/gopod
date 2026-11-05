# D067/D068 task IPC claim and receipt identity implementation

Base `0d18156`; branch `feat/task-ipc-claims`, PR #177. Owner accepted D067
and the [D068 consumer identity amendment](../plans/2026-11-05-task-ipc-receipt-identity.md).
The claim implementation and exact-byte requestHash consumer are implemented;
independent source review PASS; final exact-head validation remains required before merge.
No deployment or live-client migration has occurred.

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
those are also fixed. The receipt-publication ambiguity below led to owner-approved
D068. Host hash binding and the matching consumer are now implemented; final independent source review now passes within D068's byte-bound contract.

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

## Historical diagnostic and D068 qualification

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
required CI could waive that independent review blocker under the old contract.
D068 now requires a matching consumer. New tests execute the actual JavaScript
consumer extracted from the seeded SCHEDULING.md via Node, not a Go imitation.
After the same deterministic crash, changed replay rejects recovered old success
and accepts only the current hash-bound conflict; identical replay accepts the
original matching result. Exact whitespace/newline bytes, malformed/unbound/
foreign receipts, timeout and oversized rejection are covered. Focused IPC tests
pass (10.142 s); template tests pass (0.384 s). Live consumers using only filename
and status remain unqualified and require migration before rollout acceptance.

## Published checkpoint

Draft PR [#177](https://github.com/Coriol-is/gopod/pull/177), production/test
checkpoint `6d0c891dfa6ecaaca46cdbc292fe3fcfe84758d5`. Required
[CI 42042595921](https://github.com/Coriol-is/gopod/actions/runs/42042595921)
was in progress at handoff, not claimed green. Sonar CLI for PR #177 returned
`NOT_COMPUTED`; no fresh analysis or findings inventory was available yet.
No suppressions, merge or deployment. Retain the feature branch/worktree for
accepted amendment implementation; do not clean up unresolved work.

## D068 implementation checkpoint

Host receipts hash only complete bounded immutable snapshots; malformed bounded
JSON gets its own hash-bound error, while unreadable/oversized data cannot get a
misleading prefix hash or success. The current guide provides an executable
producer/consumer preserving the submitted bytes. Existing operator-edited guides
are intentionally preserved by Seed; migration is documented, not claimed done.
Prior checkpoint `52e4826` required CI 42042725242 passed. Independent D067+D068 source review PASS. Focused independent races passed:
IPC 16.982 s, Store 7.237 s, template 1.361 s; actual seeded JS ran without skip.
RequestHash proves historical submitted bytes, not current policy/epoch or
filesystem receipt authenticity. Final validation and fresh advisory Sonar
will be recorded before integration.


## Final local D068 validation

Full build/vet/all tests PASS. Full affected races PASS: IPC 70.370 s,
cmd/gopod 32.825 s, chattmpl 1.198 s; unchanged Store/scheduler race results reused
from the prior complete run. The final test-only fixture delta wires production
NamespaceScope into real scheduler workflow tests; focused races pass 16.962 s.
No legacy status-only client acceptance or deployment is claimed.
