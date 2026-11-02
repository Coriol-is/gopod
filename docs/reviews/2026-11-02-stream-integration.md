# Reliability, topic retirement and memory-query integration

2026-11-02. Synthetic integration qualification only; no deployment or live
Telegram/provider acceptance.

## Inputs

Base: `2d24266`.

- `876d8d0`: CP-016 scoped topic-deletion journal and retirement/write fences.
- `e650de2`: explicit runner result freeze and non-replayable freeze failure.
- `22c13b7`: escaped embedded quotes in full-text memory queries.
- `e963b34`: literal operator-query assertions for that memory fix.
- `ef5662d`: preserve cancellation/deadline errors during registration admission.

The memory test adjustment requires its predecessor `22c13b7`; applying only
`e963b34` to the base produces a modify/delete conflict because the test file
was introduced by that predecessor. The complete sequence applies cleanly.

## Intersection verified

`internal/runner/result_freeze_retirement_test.go` runs the real runner completion
path against the real Store with the synthetic executor, for buffered and streamed
runs. At the final result-freeze boundary it introduces each topic state:

- `deleting` or `uncertain`: freeze fails with the deletion gate.
- `retired`: freeze fails with `ErrConversationRetired` after quiesced synthetic
  turn settlement and atomic cleanup.
- `refused`: the preserved conversation can complete and freeze normally.

Blocked freezes retain `ResultFreezeError.NoReplay()` and leave no result for
any coalesced member. Another topic completes and freezes through the same
runner/store afterwards. The test covers eight lifecycle/mode combinations.
Existing store tests additionally establish continuity rollback; existing queue
tests establish that NoReplay errors cannot become automatic retries on shutdown.

## Results and limits

Passed on the integrated tree:

- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `go test -race ./internal/store ./internal/runner ./internal/queue`
- `git diff --check`

The full command sequence was repeated after adding `ef5662d`; it passed,
including the new registration-admission cancellation tests and the eight
runner/retirement cases. Unchanged packages used Go's test cache on this repeat.

No source integration defect was found. These changes remain foundations:
production does not yet wire the result-freeze seam, and topic deletion has no
command or complete runtime/output/recovery orchestration. The test explicitly
supplies the trusted controller binding and models quiesced topic settlement;
it does not establish those missing production adapters. Docker isolation remains
a separate CI gate. Real Telegram deletion and native-provider acceptance were
not performed.
