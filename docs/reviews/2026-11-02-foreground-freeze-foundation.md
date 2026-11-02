# CP-002 PR 6a: runner result-freeze foundation

This bounded first slice of [PR 6](../superpowers/plans/2026-10-26-reliability-delivery.md#pr-6--foreground-freeze-streaming-ownership-and-queue-recovery)
adds a controller-only seam. It does **not** enable foreground outbox delivery.

`runner.WithResultFreeze` captures an immutable copy of the trusted interactive
`ExecutionRef` and an explicit `ResultFreezer`. Sync and streamed runs validate
binding shape, folder, thread and membership before starting native execution.
The store still verifies namespace ownership and recorded turn attempts at commit.
Provider and model are taken from the actual selected runtime/command; later
model changes cannot relabel the result. Both successful paths call the freezer
while retaining the runtime slot and before exposing the final result. The
freezer owns the atomic continuity write, replacing the ordinary completion write.
Without a binding, existing behavior is unchanged. Isolated managed tasks are
not admitted by this interactive seam.

A failed result commit returns `ResultFreezeError` with a `NoReplay` marker.
The queue terminal-fails such a returned handler error instead of retrying it,
including when shutdown races the error. Group error sanitization preserves the
marker without preserving private failure details. No successful durable result
is claimed when the commit fails.

## Verification

Synthetic runner tests cover both sync and streaming, two coalesced members,
caller mutation of membership, exactly one assistant continuity append, runtime
slot ownership during commit, model changes during execution, failed commits,
and invalid/missing bindings before native execution. Queue tests verify one
invocation, terminal failure and zero recovered items, with and without shutdown.
No real provider, Telegram send, deployment or activation is part of these tests.

## Required next slice; do not enable this seam alone

- Supply trusted batch attempts and gate **before** native invocation against
  existing frozen results/publications. Current queue attempt numbering and
  recovery batching must be reconciled; never manufacture attempt membership.
- Wire sync, stream and managed task completion deliberately. Telegram currently
  consumes some runner errors and can replace an error with `ctx.Err()`; preserve
  `NoReplay` all the way to the queue when adding the production binding.
- Resume preparation/publication of frozen results without model execution;
  stop previews before a final outbox-owned immutable plan. Keep voice/code parity.
- Reconcile execution without a committed result after process interruption.
  This slice's queue marker is an **in-process** classification, not a crash
  fence: a crash before `MarkFinished`, or a failed `MarkFinished` write, can
  leave a turn unfinished. Existing queue recovery does not resolve that case.
  Durable execution-unknown reconciliation is required before enabling cutover.
- Integrate CP-016 topic lifecycle fences before permitting late completion of
  retired topics. This slice does not change store retirement policy.

No feature flag or production caller was added. No ADR is needed: these are the
existing CP-002 completion and no-automatic-replay contracts, with the remaining
activation gates stated explicitly.
