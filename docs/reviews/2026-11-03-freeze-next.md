# CP-002 ordinary result recovery — implementation checkpoint

This slice fences result-bearing/terminal turns before ordinary native dispatch,
classifies immutable result ownership at boot, and preserves Telegram NoReplay
errors. Existing published plans remain exclusively owned by the outbox; pending,
failed and uncertain are not re-labelled delivered. Unpublished frozen results
remain unfinished and hold their folder, visible in queue pending/hold diagnostics
and database backup idle checks. Other folders continue.

No foreground freeze activation or guessed text-only publication is introduced.
Before enabling it, persist the final reply mode and immutable combined plan,
replace code document process-local IDs/direct uploads, wire the voice plan's
preparation ledger, stop preview ownership before final publication and qualify
placeholder/edit/attachment parity. A frozen unpublished result has no safe generic
plan today; a future parity-qualified publisher must publish that exact result,
then a controlled restart reclassifies it. Never erase the result or retry native
execution to resolve the hold. Managed task ledger behavior stays separate.

## Verification checkpoint

Full local build/vet/test passed. Targeted new regressions passed: all publication
outcomes preserve outbox ownership, unpublished results survive repeated recovery,
coalesced mixed/terminal/result batches roll back atomically, held folders retain
pending input and let unrelated work proceed, and managed reservation release
cannot clear an ordinary result hold. Telegram sync/shutdown, streamed final,
placeholder failure and overflow fallback retain NoReplay and expose no Retry.
Affected full race suites passed (queue, store, Telegram and controller). Root owns global handoff/roadmap.

The backup idle `turns_pending` counter now includes interrupted rows, so a held
interrupted freeze cannot appear idle. This also conservatively exposes other
interrupted input; normal boot reconciles unknown started work. A stopped matched
backup still records these counters and remains available; an online idle check
must not claim such unfinished work is absent.

## Cancellation boundary

A consumed or already-ready NoReplay completion takes precedence over Stop or
shutdown's generic status. The cancellation probe does not wait for native exit.
If completion arrives only after the handler returned, the background drain logs
the non-replayable failure but cannot retroactively correct queue status. Existing
attempt-based boot reconciliation still forbids native replay; explicit late-result
settlement remains an activation gate, not a claim that all shutdown statuses are
now accurate. Forced-restart failure remains terminal and also never retries.

Final local validation: build/vet/full tests passed after the cancellation changes;
new cancellation and interrupted-zero eligibility cases passed race checks.
Independent review passed for this bounded inactive-freeze scope. Source PR/CI,
integration and deployment remain the root agent's next gates.
