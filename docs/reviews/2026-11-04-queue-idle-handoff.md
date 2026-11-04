# Queue idle handoff regression — 2026-11-04

Base: `c1fc6f4`; branch: `fix/queue-idle-handoff`.

## Cause and impact

`worker` called `beginBatch`, which read no runnable items under `q.mu`, then
called `deactivate` after releasing that lock. An admission between these calls
appended to `pending` while `active` was still true and did not start a successor.
Retirement then set `active=false`, leaving the item without a worker until later
traffic or recovery. This is a shared scheduling defect affecting ordinary and
managed work, not a fake-ledger ID collision or merely a short test timeout.
Persisted turns remain recoverable; no production loss has been demonstrated.

## Change

`beginBatch` now retires the worker under the same lock as its empty/blocked read.
The worker returns without a later `deactivate`, so it cannot clear a successor's
active state or cancel its running batch. The departing worker still releases
its own semaphore slot and WaitGroup count. Existing drain, closed-queue, held
capacity and conversation-fence admission checks remain in place.

## Evidence

- New deterministic `TestIdleWorkerHandoffAcceptsNewWork` fails on baseline for
  both ordinary and managed admissions (3-second timeout each). It models the
  idle-read/admission boundary and retains the old worker's semaphore slot until
  admission, so a successor must wait for capacity normally.
- Fixed deterministic test plus original `TestConcurrentDispatchAndEnqueueRace`:
  100 repeats PASS, also 100 repeats under `-race` PASS.
- Entire queue race suite PASS.
- `go build ./... && go vet ./... && go test ./...`: PASS.
- Independent review and CI: pending.

No production deployment, task activation, Telegram traffic or provider calls
are part of this fix. CP-007 recovery is next after this regression is integrated.
