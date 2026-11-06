# D071 placeholder deletion outcomes

Base `e8c9757`, branch `feat/durable-delete-outcomes`.

## Checkpoint 1: bounded contract and implementation

The sole production DeleteMessage call is the synchronous voice-only placeholder
cleanup. It now acquires the existing message gate and topic transport lease,
resolves durable registered-private provenance and commits a deletion attempt
before HTTP. A separate table avoids rebuilding D070 CHECK constraints/triggers.
The admission transaction rechecks exact owner/scope/source, pending raw-target
operations and outbox ownership. A creation receipt never permits deletion of a
published result.

Only true success or a definite refusal/proven-unsent outcome settles the attempt.
False success, timeout, 5xx and failed DB settlement retain uncertainty. The outer
voice path returns NoReplay before Finished fallback or TTS. Exact receipts can
retry SQLite only; process restart loses that optional evidence and retains holds.
ACK removes only the exactly captured control row (not a text-delivery ACK), and
retains a raw-target tombstone fencing later edit/repair/outbox/redelete even after
control rebind or scope rotation. Pending counts toward idle and topic quiescence;
ACK tombstones do not. Both pending and ACK evidence resist cleanup/deletion.

Groups/unregistered compatibility remains unqualified. No foreground, managed,
worker or topic-deletion orchestration is activated. Parent owns global documents,
integration and deployment. Source is wired; focused prior D070/D069 regressions
pass. Dedicated deletion tests and full validation follow.

## Checkpoint 2: deterministic coverage and review correction

Deletion-specific Store/Telegram tests pass, covering pending restart and scope
rotation, ACK tombstone retention, exact control versus successor retirement,
malformed admission, published results, both outbox admission orderings,
zero attempt consumption and sibling progress. More than 100 retained tombstones
exclude successor control repairs before LIMIT. Populated prior schema reopens
without changing old delivery hashes. Telegram covers false/malformed/500 success,
actual voice fallback with fake TTS enabled, cancellation after dispatch, precommit
failure, historical no-provenance rejection and database-only ACK/refusal receipt
repair. Restart/receipt-cap loss keeps pending. Unregister plus fresh Bot cannot
bypass pending or ACK barriers through compatibility edit/delete paths.

Independent review found the new receipt sweep returned early on settlement error,
which could starve unrelated control repairs. It now logs and continues; the exact
pending target remains excluded by the ledger. A dedicated failure-trigger test
checks unrelated control repair still emits one edit and the failed receipt stays
held. Exact-topic quiescence still returns its own matching receipt error.

## Checkpoint 3: validation and review refinements

Full `go build ./...`, `go vet ./...`, `go test ./...` pass (Store 56.220s,
Telegram 80.892s). Full affected race passes: Store 70.109s, Telegram 100.011s,
backup 32.968s, control 32.034s. Focused deletion/guard race passes (Store 7.482s,
Telegram 7.397s). A negative overlay restoring the original raw DeleteMessage call
fails the enabled-TTS false/nil regression with an unwanted generation.

Reviewer strengthened the fairness fixture: held messages 1000–1100 now precede
eligible 2000, proving exclusion before LIMIT. The outbox plain-fallback test also
covers the exact deletion hold error with zero HTTP and refunded attempt budget.
The published-target test now first proves creation-receipt provenance, then rejects
deletion of the acknowledged result. This last refinement passes its targeted test.
Production source has not changed after the receipt-sweep correction. Final full
revalidation of these test-only refinements and exact-head independent review are
pending at this checkpoint; parent owns CI/PR/integration. No runtime activation or
live service calls occurred.
