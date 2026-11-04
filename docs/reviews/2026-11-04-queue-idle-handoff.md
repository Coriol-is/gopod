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
- Independent read-only review of `af266ad`: PASS, no blocking findings. Reviewer
  independently ran `go test -race ./internal/queue -count=3`: PASS. The deterministic
  boundary is modeled directly rather than pausing an actual worker; the existing
  concurrent stress test supplies complementary end-to-end scheduling coverage.
- PR [#168](https://github.com/Coriol-is/gopod/pull/168), source `af266ad`:
  CI `test` PASS; first `isolation-spike` failed in the unchanged runner test
  `TestIntegrationLifecycleBusyTTLAndForceDrain` with `ErrExecutionUnproven`.
  Runner has no queue import; this failure is outside the changed scheduling path.
  Local real-Docker rerun of that test, `-count=3`, PASS. Failed CI job rerun PASS on the same `af266ad` head; [run attempt 2](https://github.com/Coriol-is/gopod/actions/runs/41921413585/attempts/2)
  has both required jobs green. This does not establish a root cause or erase
  the original runner failure; retain it as a separate reliability observation.
- Sonar CLI PR gate: OK. Main gate ERROR belongs to stale analysis `4e3031a`
  dated 2026-10-26, not this fix.

No production deployment, task activation, Telegram traffic or provider calls
are part of this fix. CP-007 recovery is next after this regression is integrated.

## Integration checkpoint

Independent review and exact-head required CI passed before squash merge of
PR #168 as `01cc7e0`. Sonar PR gate is OK. No deployment was performed; the
recorded Pi application remains `a25f22a`. Main CI is triggered separately by
the merge and is not claimed as completed in this report.
