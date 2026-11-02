# Task IPC replay receipt/archive race

2026-11-02. Open, pre-existing receipt-liveness defect. Investigated at `81f5f08`;
no production or test-fixture behavior changed in this checkpoint.

## Finding

[Main CI run 41497970842](https://github.com/Coriol-is/gopod/actions/runs/41497970842)
failed `TestTaskIPCRequestReplayUsesHostJournal` at `task_workflow_test.go:190`:
`no source-scoped IPC receipt for alice/same-update (possible deadlock)`.
The missing receipt is not evidence of a database deadlock.

The workflow fixture returns as soon as the receipt is visible
(`internal/ipc/task_workflow_test.go:74–101`), then removes that receipt and
atomically overwrites the same input filename for the replay. Meanwhile the
watcher commits the task journal, publishes the receipt, **then** archives by
pathname (`internal/ipc/tasks.go:128–147`, `namespace.go:101–146`). It can archive
the replacement file under the previous request's completion. No input remains
for the next sweep, and the removed receipt is never recreated.

The agent contract explicitly supports replaying identical bytes under the
same filename while the journal is retained (`chattmpl/SCHEDULING.md.tmpl:56–62`;
[spec](../superpowers/specs/2026-10-25-agent-scheduling.md#result-receipts-and-safe-replay)).
It does not require waiting for archival. This is therefore a production-capable
interleaving, not solely a test synchronization defect. A fixture-only sleep or
wait-for-source-disappearance would hide the supported replay boundary.

## Reproduction and evidence

The existing replay test passed 100 consecutive local runs:
`go test ./internal/ipc -run '^TestTaskIPCRequestReplayUsesHostJournal$' -count=100`
(9.016s). This does not disprove the CI failure or close the defect.

A [retained diagnostic](fixtures/2026-11-02-task-replay-race.go.txt) deterministically
pauses the watcher's existing `NamespacePath` resolver at the first archive
operation after receipt publication. It then deletes the receipt and atomically
replaces the input with identical bytes, releases archival, and checks:

- the replacement input has been consumed;
- the receipt remains absent after subsequent sweeps;
- exactly one task exists.

The diagnostic passed at `81f5f08`, reporting
`confirmed: replacement archived, receipt absent, exactly one task`.
Its PASS asserts the defect, not correctness. To repeat, copy the retained file
to `internal/ipc/zz_task_replay_repro_test.go`, run
`go test ./internal/ipc -run '^TestDiagnosticReplayReplacementArchivedWithoutReceipt$' -count=1 -v`,
then remove only that temporary copy. The diagnostic uses temporary SQLite/files
and fake message delivery; no Telegram, schedules on the Pi or provider calls.

The same changed-byte replacement can be consumed without its conflict receipt;
that case is source-derived, not separately exercised by this diagnostic.

## Risk and deployment classification

The host-owned SQLite journal still protects task mutations and binds payload
hash to source/filename (`internal/store/task_requests.go`). The demonstrated
race neither duplicates a task nor reapplies an update/delete. Typical identical
retries that preserve their existing receipt retain a valid previous result.
The failure is response liveness, and a conflicting replacement may lack the
promised rejection receipt. It can also make CI flaky under unlucky scheduling.

This code predates the resumed reliability/topic/memory wave; `81f5f08` does not
introduce the ordering or fixture. Deploying that wave is not acceptance or a
fix of this defect. Keep it explicitly open rather than broadening deployment
with an unqualified IPC redesign or rerunning a failed job until green.

## Fix constraints

A future fix needs a deterministic regression that rejects this interleaving
and preserves crash/restart recovery, source-scoped authorization, bounded
journal retention and truthful conflict responses. Do not simply:

- wait longer or wait for archival in the fixture while leaving the supported
  production replay boundary unchanged;
- move archival before receipt publication without a durable receipt-recovery
  mechanism for a crash between the two;
- compare inode/hash and then rename by pathname, leaving another replacement
  window between the check and rename;
- blindly republish the old success after consuming changed bytes.

Possible directions include claiming immutable request identity before processing,
or durable reconciliation of pending receipts with explicit archive ownership.
No approach is selected or implemented here. The next step is to design and test
that bounded protocol change against the retained deterministic interleaving.
