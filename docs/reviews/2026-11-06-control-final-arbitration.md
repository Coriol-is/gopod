# CP-002 control repair and final edit arbitration

Base `8b53386`, branch `fix/control-final-arbitration`.

## Checkpoint 1: bounded design

A stale repair previously performed HTTP after releasing the global writer lock;
its revision CAS protected only SQLite and could not undo a late Telegram edit.
Published final edits and durable More/Back also bypassed this writer lifecycle.

Use one cancellable gate per exact chat/message across legacy edits, repair,
durable final edit transport and callback keyboard edits. Repair reloads revision
and live status under the gate. Existing immutable outbox parts protect a final
target from publication onward, including pending/uncertain/failed delivery and
bare keyboards. An actual part ACK retires the exact control revision; it does
not re-label old partial text as the delivered final. No schema/ADR or foreground
activation. Root owns canonical HANDOFF/BACKLOG/ROADMAP updates.

## Checkpoint 2: implementation in progress

Store ownership read/ACK retirement and shared message gates are wired. Legacy
HTML/plain/bare fallback remains inside its original outer gate without recursive
locking. Outbox takes its gate inside the transport lease. Repair no longer holds
a whole-bot mutex during HTTP; only identical targets serialize. Callback errors
before dispatch retain known-unsent classification. Deterministic concurrency,
restart and retention regressions are being added before independent review.

## Checkpoint 3: regression coverage and lifecycle boundary

Both HTTP orderings are exercised with blocked requests: an issued repair finishes
before final transport, and an issued final edit blocks a stale repair before DB
ACK. Waiting cancellation releases the bounded gate registry; unrelated message
IDs remain independent. Store reopen cases cover pending/sending/uncertain/failed/
cancelled/expired/sent/compacted targets; scope mismatch and revision CAS prevent
cross-generation retirement. Late legacy text/HTML/keyboard writers are suppressed
once publication owns the target.

More/Back now uses the same message gate. Durable callbacks checkpoint retirement
of the old control revision from existing final-part ACK before editing the action
row. Legacy bound callbacks require an already acknowledged terminal text revision;
partial or failed final text cannot gain keyboard-only authority. This does not
persist a desired More/Back keyboard for retry: failures remain failed actions,
with no automatic resend/rebuild after ambiguous outcomes. Existing immutable
final markup stays unchanged. Eager old-row cleanup, mixed legacy/durable ordering,
and integrated preview/Stop/publication qualification still gate foreground.

Focused regressions pass. Full build/vet/tests and affected race suites are running;
independent review is in progress before commit/PR.

## Checkpoint 4: review corrections and remote-uncertainty limit

Review found batch starvation when 100 held/live controls stayed at retry_at=0.
Both skip paths now CAS a 30-second deferral without claiming remote settlement;
a 100-held-plus-one-unrelated regression verifies progress on the next sweep.
An obsolete Stop no longer requeues an already acknowledged terminal revision:
its final text/keyboard ACK already proves that Stop surface was retired. This
preserves successful legacy More/Back through obsolete callbacks as well as the
durable path. Unacknowledged terminal revisions remain repairable.

The message gate orders local HTTP completion, **not unknown remote settlement**.
A timed-out repair could still take effect remotely after a later final edit.
Persisted repair-attempt/unknown-outcome arbitration is NOT implemented here and
remains an explicit foreground activation blocker; retry_at and in-memory topic
uncertainty are not presented as durable proof. Existing direct repair retry
semantics are not broadened by this change. Published targets suppress subsequent
repairs across restart, but do not retroactively cancel already issued requests.

Independent review: provisional PASS after corrections; focused race suite passed
(Store 7.181s, Telegram 7.718s). Negative controls using a temporary Go overlay
restoring prior skip/requeue behavior fail both regressions as expected: `repair
overwrote acknowledged action` and `unrelated row starved`. The real source stays
unchanged during this falsification check. Final combined-head validation follows
a merge of the parallel CP-016 source; root records final evidence in global docs.
