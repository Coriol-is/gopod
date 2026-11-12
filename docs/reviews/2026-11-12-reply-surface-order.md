# Mixed legacy/outbox final reply ordering

Baseline: da529a8. Implementation worktree: feat/delivery-completion.
This is a CP-002 prerequisite, not foreground activation or parent completion.

## Contract

A new metadata-only `telegram_reply_surfaces` ledger allocates immutable order at
legacy final handoff or durable result publication, before final HTTP. Exact
completion only fills the reserved message ID. It cannot allocate a later order.
Pending/unknown surfaces do not retire a previously acknowledged row. A receipt
settled after a newer final cannot promote the older final. Telegram message IDs
and clocks are not chronology evidence.

The production legacy handoff reserves only registered-private ordinary batches.
Legacy result recording validates current durable target provenance and a terminal
control's acknowledged revision before exposing that reservation. No historical
backfill is attempted; missing reservation/provenance remains unknown. Existing
legacy group behavior is unchanged. Outbox ACK completion binds the reservation
only when the whole interactive delivery is sent and a text/edit target exists.

Both action paths consult the same current-registration scope. Durable readthrough
can ignore a conflicting process-local legacy cache entry only with exact evidence
that its surface is older. An unindexed cache entry still holds. A later durable
surface also revokes an older legacy action row without relying on process memory.

## Verification

Focused tests cover both legacy/durable order directions, delayed receipt settlement,
idempotent receipts, restart, no historical reconstruction, unacknowledged terminal
controls, explicit unknown-cache holds and proven older-cache acceptance. Full
build/vet/tests and affected race results are reported in the integration handoff.
The new table is included in backup row counts. Old binaries do not enforce the
shared order; preserve the table and do not blindly downgrade after activation.

## Remaining gates

This slice revokes stale actions but does not eagerly remove their visible keyboards.
That cleanup needs exact retired-row authority and durable edit attempts, not a raw
reply-markup call. Group/unregistered/nontext coverage and historical unknown-effect
reconciliation remain independent gates. Foreground freeze/publication, preview
retirement, voice-only placeholder outcomes and integrated Stop/callback/native
qualification are still required before enabling the foreground producer.
