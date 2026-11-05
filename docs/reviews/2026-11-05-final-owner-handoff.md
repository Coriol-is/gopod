# CP-002 final ownership handoff

Base `5e1be7c`; branch `fix/final-reply-handoff`.

## Selected production gap

The stream checks Stop after consuming RunResult, then sends the final text/code/
voice while the turn remains registered as stoppable. Stop can report acceptance
and cancel the completed native context after final delivery has already started.
The buffered and drain fallback paths have the same handoff gap.

The existing outbox contract requires stopping/deregistering streaming edits before
final publication. Stream edits already run synchronously, wait draft refreshers
are joined, and queue waiters are joined at run admission. This slice adds the
missing atomic final-owner boundary after preview completion and before terminal
sends. It does not enable the durable foreground producer or redefine pending/
sending outbox cancellation.

## Implementation checkpoint

Per-generation mutex serializes Stop, final-owner acquisition and retirement.
An accepted Stop closes its channel once and cancels native work; a final owner
rejects subsequent stops. Retirement also rejects callbacks holding old pointers.
Finalizers capture their generation and cannot acquire a replacement's ownership.
A consumed RunResult is passed to stopped settlement through an already completed
stream, preserving NoReplay evidence and avoiding a second blocking read.

Existing stream/Stop/final/sync tests and dedicated race regressions pass.
Independent review found two generation-recapture windows (delegation and
registration); both are corrected by returning the exact registration pointer
and carrying it through the context. New registrations overwrite inherited
bindings; delegations preserve them. Final review and full checks are running. No production mutation, live Telegram test or billing.
Persistent frozen cancellation and reply-action/producer integration remain open.

## Scope limits

This is successful-final ownership in the existing foreground sender. It does
not make all preview/control metadata generation-aware: existing preview helpers
still use turn IDs. Terminal-error presentation, persistent frozen-result
cancellation, reply-action preparation and the durable producer are not changed.
No new ADR is needed: the boundary implements the existing preview/final contract.

## Regression coverage

- Stop and final acquisition race with exactly one winner (1,000 iterations).
- Late Stop via command/button/draft update cannot cancel active final HTTP;
  draft/edit, drain fallback, synchronous and voice-only TTS paths are covered.
- Stop before the preview join finishes wins even after RunResult is consumed;
  an unclosed Result channel cannot cause a second blocking read.
- Retired generations cannot claim replacements through sync-to-stream or
  stream-to-fallback delegation; a new registration replaces inherited bindings.
- Consumed native NoReplay evidence remains available to stopped settlement.
