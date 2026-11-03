# Private-chat interruption audit

Date: 2026-10-31. Source: `f0d094c`. Scope: inspection and verification only;
no runtime changes, production inference, Telegram messages, or fixes.

## Behavior

- An ordinary incoming message is persisted and queued, not injected into or used
  to cancel the active turn (`telegram/handler.go:enqueueTurn`, `queue/queue.go:worker`).
  Leading pending messages in the same thread and source class can be batched.
- Native draft Stop matches the active chat and draft id. The edit fallback uses
  an inline Stop button bound to the turn and chat. Duplicate and stale stops
  are ignored (`telegram/stop.go`). The run is registered before slot admission,
  so Stop also works while waiting for an agent slot (`agentwait.go`).
- Stop cancels the run context, preserves the visible partial reply (or
  `(stopped)`), and marks the batch terminal `stopped`, with no automatic retry
  or restart replay. Pending messages remain queued. The next run waits for the
  previous run's cleanup attempt to finish.
- The runner uses a per-run PID file and a second Docker exec to signal the
  process group: TERM, up to 3 seconds grace, then KILL. The cleanup context is
  independent of the cancelled turn and bounded to 15 seconds. Cancelled turns
  skip memory ingestion, turn counting and automatic compaction.
- Foreground work can cancel same-chat `RunFresh` maintenance. A scheduled task
  in the normal queue is not preempted by a user message. Controller shutdown is
  different: unfinished turns become `interrupted` and may resume at startup.

## Findings and limitations

1. **Sync fallback loses the Stop cancellation context.**
   `telegram/streaming.go:runAgentStreaming` registers `cancelRun`, but after a
   streaming setup error invokes `runAgentSync(ctx, item)` using the parent
   context. Stop cannot cancel that fallback run through `cancelRun`. This is
   distinct from the edit-only UI fallback, which retains the streaming runner
   and supports Stop. Verified by source inspection; no new reproduction test.
2. **Cleanup is best effort, not verified process death.**
   `runner/agentkill.go:killAgent` logs exec failures and returns no success
   result; the shell script exits zero even if signals fail. The stream closes
   its attachment and releases the slot after the attempt regardless. During a
   Docker/kill failure the next run can therefore start without proof that the
   previous process exited. `TestStartStreamCancelEndsEvenIfKillFails` explicitly
   preserves termination of the runner in this case. Detached process groups
   and already performed external side effects are not undone (D033).
3. **Stopped reply delivery errors can be swallowed.**
   `finishStopped` ignores `sendFinal`'s error and still returns `ErrStopped`;
   the edit path also ignores ordinary edit errors after checking missing topics.
   A stopped turn can therefore remain terminal without its partial reply being
   successfully delivered. This prevents replay but weakens user feedback.

## Verification

- PASS: `go test -race ./internal/queue ./internal/runner ./internal/telegram`.
- PASS: targeted Telegram Stop/wait tests with `-race -count=1`.
- PASS: local Docker `TestIntegrationStreamCancelKillsAgent`, using the existing
  `redis:7-alpine` image. Result arrived 116 ms after cancellation; the test checks
  the process before and after cancellation. This is a shell-process probe, not
  native Claude/Codex session-resumption acceptance.
- SKIP: `TestIntegrationFreshPreemptionKillsProcess`: required local
  `gopod-agent:latest` image absent. No image was built or pulled.
- Read-only Pi check: checkout `e3b5401`; no source differences from local HEAD
  in `internal/runner`, `internal/telegram`, or `internal/queue`. In the last
  24 hours of compose logs, one `cancelled agent stopped` event appeared at
  `2026-10-31T19:24:13.606Z` for owner, with no matching `turn stopped by user`
  event in the filtered output. This does not establish a user Stop action or
  independently prove process exit.

Real-client A08/A10 acceptance remains open: Stop while thinking/tool-running,
next-turn native session resumption, and Stop while waiting. No production turn
was deliberately interrupted for this audit.

## CP-036 implementation follow-up

The owner rejected automatic interruption and authorized explicit inline Stop plus
`/stop`. [CP-036](../proposals/CP-036-private-chat-stop.md) and
[spec](../superpowers/specs/2026-10-31-private-chat-stop.md) supersede the abandoned
steering draft. The number avoids the existing CP-035 CLI-hooks worktree.

Implemented: persistent owner-private placeholder before agent startup, reusing
existing edit/Stop machinery; owner-only private command through Router, scoped
to chat/topic; in-flight registration for buffered execution and the same child
context through streaming setup fallback. This fixes finding 1 above. Findings
2 and 3 remain outside this UI/command change. Session files and mappings are not
cleared; first-turn termination before native persistence remains a limitation.

Validation on the implementation branch:
- PASS: `go build ./...`, `go vet ./...`, `go test ./...`.
- PASS: affected-package `-race` checks (`control`, `queue`, `runner`, `telegram`,
  `cmd/gopod`); new targeted Stop tests also ran uncached with `-race`.
- PASS: `TestIntegrationStreamCancelKillsAgent` with `redis:7-alpine`; 114 ms to
  cancellation result. No inference or live Telegram sends.
- PASS: `git diff --check` and relative documentation-link existence checks.

Source self-review traced Router permissions and dispatch outside the agent queue,
chat/topic matching, stale-button behavior, setup/wait/stream and buffered cleanup,
and the existing queue/session continuation tests. Regression coverage includes
private button callback, private-only menu, idle/repeated/wrong-topic commands,
buffered cancellation and a fallback that returns late output after cancellation,
and queued input surviving explicit Stop. Independent review and live acceptance
are separate gates; neither is established by this self-review.

### Pre-merge re-review

Owner requested checks/merge and then deployment. Source re-review found that
routing a buffered voice-only reply through the streaming renderer would publish
its full text. The fix retires the Stop placeholder and preserves the existing
voice-only delivery path; a regression test checks no answer text is published.
No other new blocking source finding was identified. This is implementer
re-review plus separately executed Sonar analysis, not an independent human
GitHub approval. Existing cleanup/delivery limitations remain recorded above.

Release: PR #124 merged as `8a3ce75` after final-head green CI and the above fix.
The owner's later deployment authorization was completed; see
[deployment evidence](../deployments/2026-10-31-private-stop.md). The earlier
"no production changes" statements describe the audit/implementation checkpoint.
