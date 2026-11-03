# CP-002 PR 6b — atomic batch attempt recording

This prerequisite slice records all ordinary queue batch members in one store
transaction and passes the committed attempt numbers to the handler. Previously,
per-row start errors were logged and ignored, and handler items retained stale
attempt counters. Managed tasks keep their separate reservation/execution ledger.

`StartTurnBatch` validates identities, conversation/source batching, duplicate
IDs and expected previous counters; it rejects managed ledger members. An error
rolls back all updates. The queue retains the same batch and retries only the
pre-execution recording with capped backoff, preserving order. Cancellation
leaves rows for the existing startup/forced-restart reconciliation. It does not
call completion hooks for work that has not started or advance newer messages
past an uncommitted batch. Restart snapshots own a separate immutable item slice.

## Review-driven scope boundary

The first draft combined this prerequisite with frozen-result and topic-denial
preflight. Independent review found that its failure handling parked rows while
allowing newer messages to overtake them and incorrectly notified completion
hooks. It also found shared-slice mutation racing forced-restart snapshots.
Those paths were replaced, and eligibility preflight was removed from this slice
rather than inventing a permanent-rejection/reconciliation lifecycle here.

This does **not** activate runner freeze, foreground outbox ownership, publication
recovery, topic deletion or automatic replay of unknown execution. The existing
startup adapter still refuses automatic execution after a recorded attempt.
Before cutover, add explicit frozen/publication recovery and topic/output fences;
checks must preserve existing results and notify completion only for rows really
settled. `StartTurnBatch` is an attempt stamp, not that eligibility decision.

## Verification checkpoint

Tests cover atomic rollback on a bad second member and injected write failure,
authoritative counters across retries, duplicate/stale/mixed snapshots,
concurrent use of one snapshot, pre-execution failure without completion,
arrival ordering through persistence recovery, and immutable restart snapshots.
Full build/vet/test and race suites for queue/store/cmd passed. Independent
re-review passed after the narrower scope and concurrency fixes. Added a
production-adapter test that freezes one coalesced result from the committed
member counters, plus explicit managed-ledger refusal coverage; their targeted
race run passed. PR #160 merged as `f5ba97a` after green test/isolation/Sonar.
No deployment was performed.
