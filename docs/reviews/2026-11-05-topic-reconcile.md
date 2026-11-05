# CP-016 positive receipt reconciliation

## Scope checkpoint

Implement DB-only repair of a positively acknowledged legacy main/control send
whose ownership receipt write failed. Retain the exact precommitted attempt,
registration scope, source/target and returned message ID in a bounded process
registry. The existing production control-recovery loop, exact message lookup and
scoped quiescence retry the acknowledgement without sending another HTTP request.

A successful receipt repair never clears the generic per-topic/unknown-edit
uncertainty latch. Receipt-backed failures are classified separately at the time
of return, so a concurrent repair cannot be followed by a stale generic latch.
Unknown HTTP outcomes have no positive receipt; pre-crash pending attempts cannot
be reconstructed after restart. Both continue to block quiescence. Registry-full
failures keep their durable pending evidence and generic uncertainty.

No schema change is needed. CP-002 agent owns its separate additive outbox table;
this slice owns ownership Store helpers, Telegram receipt/transport/lookup and
control-recovery/quiescence hooks. Root owns global documentation. No deletion
command, operator absence assertion, TTL clearing, live auth/delete or deployment.

Implementation and regression validation in progress.

## Implementation checkpoint

The registry retains at most 128 exact receipts, with no eviction. Excess receipts
remain durable unresolved attempts and retain generic uncertainty. Failed repair
has a 30-second cooldown; concurrent callers skip claimed entries. Every sweep,
lookup or quiescence repair shares a maximum five-second budget (or the caller's
shorter deadline); cancellation releases all claimed entries. No entry contains
message text, credentials or raw provider output.

Store repair validates the current registration and then the immutable original
attempt/scope/chat/source/target before acknowledgement. A pending deletion intent
does not erase positive send evidence: the receipt can settle while admission
stays closed. Changed registration cannot relabel the response. Repeated receipt
commit is idempotent; changing its message ID fails.

Production callers are `RepairControls` (existing startup/30-second loop), exact
message-owner lookup and quiescence after transport joining. An individual failed
receipt does not prevent unrelated control repair. There is no new HTTP client,
operator command, restart reconstruction or automatic remote absence inference.
The old restart test helper now explicitly discards the new process-only registry;
separate tests retain it only when reconnecting Store within the same Bot process.

Focused tests/races pass for receipt-write failure -> repair -> idle readiness,
lookup/control-loop integration, restart loss, two independent attempts, generic
same-topic and unknown-edit holds, concurrent repair before the original return,
exact identity/stale registration, bounds/backoff and deadline claim release.
Full build/vet/test and affected store/Telegram races are running. Additional
production-path capacity overflow and unknown-network tests verify one remote
send total and retained durable/generic blockers.

## Remaining gates

Only positive host-returned receipts still held by this process are repairable.
Unknown network effects, lost receipts after restart, overflow receipts without
cached evidence and historical unowned controls still require an explicit
reconciliation contract. Generic ambiguity is never cleared by this slice.
Retention, other noncontrol output provenance, ordinary native-login exit proof,
managed queue settlement and confirmed delete/outcome/local retirement remain
open. `/thread delete` stays disabled; no live actions were performed.

## Validation checkpoint

Implementation commit: `cab98d7`. `go build ./...`, `go vet ./...` and full
`go test ./...` passed (Telegram 43.841s). Full
`go test -race ./internal/store ./internal/telegram` passed (32.631s / 63.869s).
The final two test-only production-path overflow/unknown-network cases were then
run separately under race and passed (6.870s); production code did not change.
Independent review provisionally passed the same production source and repeated
focused receipt tests under race. Exact commit confirmation/required CI remain
root integration gates; no deployment or live acceptance is claimed.

## Integration checkpoint

PR [#171](https://github.com/Coriol-is/gopod/pull/171), tip `b26b4a9` (source
`cab98d7`), passed independent review, full build/vet/test and full Store/Telegram
race. Both required jobs in [CI 41950007596](https://github.com/Coriol-is/gopod/actions/runs/41950007596)
and Sonar gate passed before squash merge `930586a`. No deployment or live
Telegram/login/deletion action occurred; unknown-outcome gates remain open.
