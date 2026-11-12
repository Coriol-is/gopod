# Explicit state machines: refactor spec for audit items 1-6

Date: 2026-11-08. Source: [implicit state machines audit](../../reviews/2026-11-08-implicit-state-machines.md), risk ranking rows 1-6. Owner approved running the six items as parallel agent tasks, one worktree each, and asked to pause before dispatch (quota window). Status: **specified, not started**. Feature worktrees and branches exist at base `da529a8` with no commits (see HANDOFF).

Each part below is a self-contained implementer brief. Report paths are assigned by the controller at dispatch time. Next free ADR number at the time of writing: D072; each part that lands an ADR takes the next number at merge time (expect renumbering across the six PRs).

## Common rules for every brief

Read `CLAUDE.md`, `docs/HANDOFF.md` (top 60 lines), `docs/DECISIONS.md` headings and `docs/DEVELOPMENT-CHECKS.md` in your worktree first. Hard constraints apply: Go only, no CGO, DB access only via `internal/store`, `log/slog`, errors wrapped with `%w`, admin commands only through `internal/control` with `Perm`, no secrets in files.

Workflow:
- Work ONLY inside your worktree directory (given in the brief). Never touch `~/_code/coriolis/gopod` (main checkout) or other worktrees. Never run `git reset --hard`, `git checkout` of other branches, `git stash`, or force pushes.
- Test-driven: write the failing test first, make it pass, refactor. Keep the change focused on the brief; if you find out-of-scope issues, list them in your report under "Noted", do not fix them.
- Commit small, `<area>: <imperative summary>` with a body that explains why. The repository pre-commit hook (`.githooks`, already configured) runs a deadcode analysis on Go commits; a new unreachable function fails the commit. If it fails, fix the cause (delete the dead function or wire it), never edit the baseline.
- Before claiming done run in the worktree: `go build ./... && go vet ./... && go test ./...` (full suite, telegram package takes ~75 s) and `go test -race` for the packages you changed. Paste the decisive output lines in the report.
- Docs: if you make an architectural choice, append an ADR `D{NNN}: <title>` to `docs/DECISIONS.md` (next free number). Update `docs/BACKLOG.md` only if the brief says so. Do not edit `docs/HANDOFF.md` (the controller does).
- When green: `git push -u origin <branch>` and open a PR with `gh pr create --base main --title "<area>: <imperative summary>" --body-file <file>` where the body has sections: Summary, Design, Tests (commands + results), Risks, Noted (out of scope). Do not merge. Do not touch Sonar settings.
- Never dispatch subagents yourself. Report back only: status (DONE / DONE_WITH_CONCERNS / NEEDS_CONTEXT / BLOCKED), branch, commits, PR URL, one-line test summary, concerns. Write the full report to the report path given in the brief.

## Brief 1: guarded turn transitions (`turns.status`)

Worktree: `/Users/spaceinvaderz/_code/coriolis/gopod-turn-transitions`, branch `store/turn-transitions`, base `da529a8`.

### Context (verified findings)
The durable turn row (`internal/store/turns.go:13-22`, statuses pending/running/done/failed/interrupted/stopped) has 12 writers and no guarded transition:
- `MarkTurnRunning` (`turns.go:130-134`) and `MarkTurnFinished` (`turns.go:139-145`) are `WHERE id = ?` only.
- `SetTurnReply` (`turns.go:156-159`) comment says "first write wins" but has no `reply_msg_id = 0` guard; the outbox path has a real CAS in `internal/store/results.go:313`.
- `SettleForcedTurns` (`internal/store/restart.go:264-268`) writes `status='stopped'` for every `ForceTurns` id with no status guard, overwriting `failed` (written earlier by `RecordForcedTurns` `restart.go:240` and by `internal/queue/queue.go:511-515`) and in a narrow window `done`.
- cmd `settleForced` (`cmd/gopod/restart.go:594`) calls `MarkTurnFinished(..., TurnStopped, ...)` with no guard for managed turns.
- `execution_unknown` is a sub-state carried in free text: written at `queue.go:512,585`, `internal/queue/managed.go:367,637`, `internal/store/ordinary_recovery.go:69,100`, `internal/store/restart.go:240`; matched by `strings.Contains(t.Error, "execution_unknown")` at `internal/telegram/control_recovery.go:79,201`.
- Shutdown race: `internal/queue/queue.go:453-461` and `:485-489` finish a freshly drained *pending* batch as `StatusStopped` with `ErrForcedRestart` whenever `runCtx.Err() != nil`, even when no force happened (plain `Close()` cancels the same ctx). The message is lost (terminal, never replayed).
- Dead replay path: production `LoadUnfinished` (`cmd/gopod/main.go:1226-1251`) runs `RecoverOrdinaryTurn` which settles every ordinary row with `attempts > 0` (`ordinary_recovery.go:98-106`), so only `attempts == 0` rows reach `Queue.Recover`. Therefore `Item.Resumed`, the crash-loop branch `queue.go:375-381` (`ErrCrashLoop`, `MaxAttempts`), `crashLoopNotice`/`resumePrompt`/`pickBatchItem` resumed-handling in `internal/telegram/resume.go:17-28,114-126` are unreachable with the real store. The spec `docs/superpowers/specs/2026-10-03-durable-turns-design.md:46-53` still describes the old replay.

### Tasks (in order, one commit or more each)

#### T1. Guarded transition primitive
- Add to `internal/store` (new file `turn_transitions.go`): `ErrTurnTransition` and
  `func (s *Store) TransitionTurn(ctx, id int64, from []string, to string, errText string) error` executing one `UPDATE turns SET status=?, error=?, finished_at=? WHERE id=? AND status IN (...)`; when `RowsAffected == 0` return `ErrTurnTransition` wrapped with the current status (read it in the same tx for the message). `finished_at` is set only when `to` is terminal (done/failed/stopped) or interrupted, matching today's `MarkTurnFinished`.
- Reimplement `MarkTurnFinished` as `TransitionTurn(ctx, id, []string{TurnPending, TurnRunning, TurnInterrupted}, status, errText)`. Keep the exported name so callers compile; existing callers that previously overwrote terminal rows will now get `ErrTurnTransition`: audit each (`internal/queue/queue.go finish()`, `internal/queue/managed.go closeManaged`, `cmd/gopod/restart.go:594`, `internal/store/ordinary_recovery.go:69,108`) and decide: log at Warn and continue (queue `finish`, which already logs mark failures), or propagate. Do not silently swallow.
- `MarkTurnRunning` gets the guard `status IN ('pending','running','interrupted')`; the managed caller `internal/queue/managed.go:600` must handle `ErrTurnTransition` (log, treat the item as already closed).
- `SetTurnReply`: add `AND reply_msg_id = 0`; return `ErrTurnTransition`-style sentinel when 0 rows; caller `internal/telegram/resume.go:74-77` logs at Debug (a later attempt must not overwrite the first memo). Keep `results.go:313` as is.
- `SettleForcedTurns` (`internal/store/restart.go:265`): add `AND status IN ('pending','running','interrupted')` so `failed`/`done` rows are never rewritten to `stopped`.
- Tests in `internal/store/turns_test.go` (+ `restart_test.go`): terminal row cannot be re-finished; running->done after done->stopped attempt keeps done; `SetTurnReply` first write wins; `SettleForcedTurns` leaves `failed` and `done` untouched and still stops `running`.

#### T2. `execution_unknown` as a column
- Migration in `internal/store/schema.go` migrations list: `ALTER TABLE turns ADD COLUMN execution_unknown INTEGER NOT NULL DEFAULT 0` followed by backfill `UPDATE turns SET execution_unknown=1 WHERE error LIKE 'execution_unknown%'`. Follow the existing idempotent migration pattern in that file (check how prior `ALTER TABLE ... ADD COLUMN` lines are guarded against re-run).
- `Turn` struct gets `ExecutionUnknown bool`; `ListUnfinishedTurns`/`GetTurn` read it.
- Add `TransitionTurnUnknown(ctx, id, from, to, errText)` (or a boolean option on `TransitionTurn`) that also sets `execution_unknown=1`. Convert the seven writers listed above to set the column (keep the error text for humans). `RecordForcedTurns`' CASE expression sets the column for the `attempts>0` branch; `ordinary_recovery.go` sets it for its two unknown branches.
- `internal/queue` `TurnStore` interface: extend `MarkFinished` with a way to mark unknown (e.g. new method `MarkFinishedUnknown`) and update the production adapter in `cmd/gopod/main.go` plus the fake stores in `internal/queue/*_test.go`.
- `internal/telegram/control_recovery.go:79,201`: use `t.ExecutionUnknown` instead of `strings.Contains`. Keep the `force` substring check unless it also maps cleanly to a flag (do not add a second column for it).
- Tests: migration backfill on a pre-existing row with the legacy text; UI classification via the flag with error text that does not contain the substring.

#### T3. Shutdown race in the queue worker
- In `internal/queue/queue.go:453-461` and `:485-489`: when `runCtx.Err() != nil`, finish as `StatusStopped`/`ErrForcedRestart` only if `q.batchForced(folder)` is true. Otherwise (plain `Close()`), items that have not started must stay recoverable: re-park them to the front of `pending` (reuse `settleCancelledBatch`-style re-parking without the cancellation error) and return from the worker without writing a terminal status. Rows remain `pending`/`attempts=0` and are dispatched at next boot (`ordinary_recovery.go:104-106` already allows that).
- Test in `internal/queue/queue_test.go`: Close() racing a freshly drained batch leaves the row non-terminal (use the fake store and a handler gate to hit the window deterministically, e.g. cancel inside `StartBatch`).

#### T4. Remove the dead replay path and update the spec
- Delete `Item.Resumed`, the crash-loop branch in `Queue.Recover` (`queue.go:375-381`, `MaxAttempts`, `ErrCrashLoop` if unused elsewhere), and the resumed-handling in `internal/telegram/resume.go` (`crashLoopNotice`, `resumePrompt`, resumed branch in `pickBatchItem`) together with their fake-store tests (`TestRecoverReenqueuesUnfinishedAsResumed`, `TestRecoverCrashLoopIsFailedWithoutRunning`, and any telegram test asserting the resume prompt). Keep `Queue.Recover` scheduling `attempts == 0` rows and `RecoveryHold` items unchanged. Grep for every remaining reference before deleting; the deadcode hook will catch leftovers.
- Update `docs/superpowers/specs/2026-10-03-durable-turns-design.md` recovery section to describe the real behaviour (boot recovery settles started rows as failed/`execution_unknown` or done; never-started rows are dispatched; frozen results hold) and append ADR `D{next}: Turn transitions are guarded; started turns are never replayed` to `docs/DECISIONS.md`.

### Shared-file caution
`internal/store/restart.go` and `cmd/gopod/restart.go` are also edited by two sibling branches (typed restart phases; managed forced-settle fix). Keep your edits in those two files to the exact lines named above (the `SettleForcedTurns` UPDATE and the `MarkTurnFinished` call at `cmd/gopod/restart.go:594`) to minimise merge conflicts.

## Brief 2: managed task ledger — forced-settle bug, CAS, operator reconcile

Worktree: `/Users/spaceinvaderz/_code/coriolis/gopod-managed-reconcile`, branch `tasks/managed-reconcile`, base `da529a8`.

### Context (verified findings)
The managed task ledger (`task_runs`, `task_attempts`; `internal/store/task_runs.go`, `task_dispatch.go`, `schema_tasks.go`) is gated by `GOPOD_MANAGED_TASKS` and reached in production only through `cmd/gopod/managed_turns.go`, `cmd/gopod/restart.go` and `internal/store/task_report.go`.
1. Bug in `cmd/gopod/restart.go:564-599` (`settleForced`): it reads attempt `a` (`:580`), calls `SettleTaskAttempt(... SettleStopped, Evidence: "local_exit:forced_restart_container_stopped" ...)` (`:584-585`), which inside `transitionAttemptQ` already flips `reservation_state` `held` -> `released` (`internal/store/task_runs.go:344-347`). It then tests the stale `a.Reservation == "held"` (`:589`) and calls `r.queue.ReleaseHold` -> `managedLedger.Release` -> `ReleaseAttemptReservation`, whose UPDATE has `WHERE reservation_state='held'` (`task_runs.go:379-382`) -> 0 rows -> `ErrReservationNotReleasable` -> `settleForced` returns the error before `MarkTurnFinished` and `SettleForcedTurns`. `queue.ReleaseHold` (`internal/queue/managed.go:477-501`) calls the store first and never clears the in-memory hold on store error, so the folder stays held and the semaphore slot is never returned. No test covers `settleForced` (grep `settleForced|ForceTurns|forced_restart_container_stopped` in `cmd/gopod/*_test.go` has no hits).
2. `requestRunStopQ` (`task_runs.go:486`) writes `execution_state=?` with `WHERE run_id=?` only (state read earlier in the same tx at `:440`).
3. Dead ends with no operator exit: `execution_unknown` runs/attempts (with `reservation_state='held'`, only exit is forced restart; with `reservation_state='none'`, the frozen-result-on-queued case from `task_dispatch.go:390-396`, no exit at all: invisible to `HeldTaskReservations` `task_dispatch.go:434-436`). `effects_state` goes `none -> unresolved` on unknown (`task_runs.go:279-281`) and only `ReconcileRunEffects` (`task_runs.go:408`) moves it to `reconciled`, but nothing in `internal/control` or `cmd/gopod` calls `ReleaseAttemptReservation`, `ReconcileRunEffects`, `TransitionRun` or `TransitionAttempt`. Ordinary restart is refused while any hold exists (`cmd/gopod/restart.go:307-310, 377-380`).

### Tasks

#### T1. Fix `settleForced` (test first)
- Write a test in `cmd/gopod/restart_test.go` (look at `managed_turns_test.go` and `restart_test.go` for the real-store harness) that creates a managed attempt in `running/held` with a turn in `ForceTurns` and asserts `settleForced` succeeds, the attempt ends `cancelled/released`, the turn is `stopped`, and `queue.ReleaseHold`/in-memory hold is cleared (check `Queue.FolderHeld` or the equivalent accessor used in `managed_test.go`). Also a case where the attempt is already `execution_unknown/released`.
- Fix: after `SettleTaskAttempt`, re-read the attempt; only call `ReleaseHold` when `Reservation == "held"`. For the case where the ledger already released but the queue still has an in-memory hold, add `func (q *Queue) ForgetHold(attemptID string)` in `internal/queue/managed.go` that removes the hold, decrements `cs.held`, returns the sem slot if `h.slot`, and kicks the folder (mirror the second half of `ReleaseHold`), and call it from `settleForced`. Alternatively make `ReleaseHold` tolerate `ErrReservationNotReleasable` when the attempt is already released: pick one, justify in the report.

#### T2. CAS on `requestRunStopQ`
- `internal/store/task_runs.go:486`: add `AND execution_state=?` with the state read at `:440`; on 0 rows return `ErrTransitionConflict` (existing sentinel, check the name in `task_runs.go`). Update `TestStopRunIsSeparateFromScheduleCancel` or add a concurrent test similar to `TestConcurrentTransitionsExactlyOneWins`.

#### T3. Operator reconcile command through `internal/control`
- Add store method `ReconcileAttempt(ctx, attemptID string, detail string) (ReconcileResult, error)` in `internal/store` that, in one tx: if attempt `execution_state='execution_unknown'`: with `reservation_state='held'` call the existing release logic with `ReleaseProof` `operator_reconciled` (check the proof constants in `task_runs.go`); with `reservation_state='none'` transition attempt and run `execution_unknown -> cancelled` with Evidence `operator_reconciled:<detail>` through the existing typed `transitionAttemptQ`/`transitionRunQ` (keep run and attempt in lockstep, see `task_dispatch.go:263`); then `ReconcileRunEffects` for the run. Terminal attempts with `held` reservation: release only. Anything else: `ErrInvalidTransition`. Detail length bounds like `ReleaseAttemptReservation` (1..350).
- Register `/tasks reconcile <attempt_id|run_id> <detail>` in `internal/control/tasks.go` as `PermOwnerOnly` with the confirmation-button flow used by other destructive commands (see `docs/CONTROL.md §6.1`, `docs/DECISIONS.md D028`, and how `/restart force` or `/outbox settle` in `internal/control/outbox_cmds.go` ask for confirmation). Free-form parameters stay text. Resolve a run id to its attempts. When called without arguments, list reconcilable attempts (state `execution_unknown`, or terminal with `held`) with ids. The handler must also clear the queue's in-memory hold: expose the queue hook through the existing control dependencies the way `/restart` reaches the queue (find how `internal/control` gets a queue/runtime reference; if it is only through `cmd/gopod` wiring, add the hook there, not a new import cycle).
- Document the command in `docs/CONTROL.md` (command table) and add ADR `D{next}: Operator reconciliation of unknown managed executions`.
- Tests: store-level for each branch; control-level for Perm and confirmation (follow `internal/control/outbox_cmds_test.go` or `tasks_panel_test.go` patterns).

### Out of scope (list under Noted, do not implement)
Deleting `publication_state`, `task_runs.result_id`, wiring `OnFrozen`, managed admission API.

### Shared-file caution
`cmd/gopod/restart.go` is also edited by two sibling branches. Confine your edits there to `settleForced` (`:564-599`).

## Brief 3: typed restart phases and recoverable `blocked`

Worktree: `/Users/spaceinvaderz/_code/coriolis/gopod-restart-phases`, branch `restart/typed-phases`, base `da529a8`.

### Context (verified findings)
Restart state lives in `router_state["controller:restart"]` as JSON `RestartRecord{Phase, Mode, MessageID, Containers, ForceTurns, SuppressedThrough}` (`internal/store/restart.go:12-36`). Phases are free strings: requested, draining, forcing, blocked, restarting, cancelled, aborted, notice_unknown, notice_claimed, ready, degraded; active set at `:145-151`. `SetRestartPhase` (`:96-114`) CASes on `from` but does not validate `to`; only `AbortRestart` (`:119-143`) validates terminal names. Driver: `cmd/gopod/restart.go` (`requestMode :64-85`, `completeDrain :139-227`, `cancel :254-276`, `recovered :433-462`, `abortUnstarted :499-516`, `restoreDrain :520-537`, `retryForce :538-563`, `settleForced :564-599`).
Problems:
- `restoreDrain` (startup, `cmd/gopod/main.go:829-833`) moves any interrupted `requested|draining|forcing` record to `blocked` and re-arms `queue.BeginDrain()` + `runtime.BeginDrain()` with no owner notice; the bot silently stops answering until the owner sends `/restart cancel` (drain mode) or `/restart force` (force mode).
- `recovered()` (`:441`) accepts phase `requested` and reports "degraded" though nothing restarted.
- No tests reference `restoreDrain`, `retryForce`, `UpdateRestartTargets`, `RecordRestartSuppression`, `SettleForcedTurns`, or the `forcing -> aborted` path (`:109-126`).

### Tasks

#### T1. Typed phases with a transition table (store)
- In `internal/store/restart.go`: `type RestartPhase string` with constants for all eleven phases; `func (p RestartPhase) Active() bool`, `Terminal() bool`; `var restartTransitions = map[RestartPhase][]RestartPhase{...}` encoding exactly the transitions the driver performs today (derive them from `cmd/gopod/restart.go`; list them in the report): requested->{draining, forcing, aborted, notice_unknown, notice_claimed, blocked}, draining->{restarting, blocked, cancelled}, forcing->{restarting, blocked, aborted}, blocked->{cancelled, forcing}, restarting->{notice_claimed, notice_unknown}, notice_claimed->{ready, degraded, notice_unknown}. `SetRestartPhase(ctx, msgID, from, to)` returns a new `ErrRestartPhaseInvalid` when `to` is unknown or `from->to` is not in the table. `RestartRecord.Phase` becomes `RestartPhase`. Keep the JSON wire format identical (string values) so existing rows load.
- Update every caller in `cmd/gopod` and `internal/control` to the typed constants (grep for the string literals). Tests: table rejects an invalid pair, accepts each valid pair, and an existing JSON row with a string phase still decodes.

#### T2. `restoreDrain` notifies and does not strand drain-mode requests
- Mode `drain`: do not re-arm the drains. Set phase `cancelled` with reason "controller restarted during drain; request cancelled" and send the owner a notice through the same notifier `completeDrain` uses (look at how `blocked` notices are sent). The owner can issue a new `/restart`.
- Mode `force`: keep `blocked` and the re-armed drains (force intent must survive), but send the owner a notice naming the exact next step (`/restart force` to retry, `/restart cancel` is refused for force).
- `recovered()`: for phase `requested` report "restart was requested but never started; cancelled" and move it to `cancelled` (add `requested -> cancelled` to the table) instead of `notice_claimed/degraded`.
- Tests in `cmd/gopod/restart_test.go` using the existing harness: `restoreDrain` for each of requested/draining/forcing/blocked in both modes asserts phase, drain re-arm (or not) and the notice text; `retryForce` from `blocked` reaches `forcing` then `restarting`; `forcing -> aborted` when `ForceDrain` callback fails.

#### T3. Small hygiene
- `finishPhase` is called while `r.mu` is held in `completeDrain` (`:204-227`) and can wait up to 5 s on a stalled store, blocking `cancel`. Move the store write out of the locked section if it can be done without breaking the phase re-read check at `:206-209` (explain the ordering in the report); if not, leave it and say why.
- `docs/CONTROL.md` restart section: document the phases and the new recovery behaviour. ADR `D{next}: Restart phases are a typed transition table`.

### Shared-file caution
`internal/store/restart.go` and `cmd/gopod/restart.go` are also edited by two sibling branches: one changes only the `SettleForcedTurns` UPDATE (`store/restart.go:265`) and the `MarkTurnFinished` call at `cmd/gopod/restart.go:594`; the other changes only `settleForced` (`cmd/gopod/restart.go:564-599`). Do not refactor or move those functions; typed-constant replacements inside them are fine.

## Brief 4: one execution-evidence state per folder in the runner

Worktree: `/Users/spaceinvaderz/_code/coriolis/gopod-runner-evidence`, branch `runner/execution-evidence`, base `da529a8`.

### Context (verified findings)
`internal/runner` keeps per-folder execution evidence in `Runner.unknownExecutions map[string]string` (`runner.go:113-124`) with string-encoded kinds: a container id (exec outcome unknown), the literal `"unresolved"` (`drain.go:367-377`, inspect failed) and keys prefixed `"container:<id>"` (`drain.go:299-305`, blocks every folder). Writers: `markExecutionUnknown` (`drain.go:139-147`) from `agentkill.go:117`, `runner.go:742-744`, `fresh.go:35-37`, `task_run.go:301-303`, `drain.go:322-324`; `markContainerUnknown` from `startup.go:151`, `exec_management.go:40,47`. Clearing: `reconcileExited` (`drain.go:214-256`): the container-id kind clears **only** when `containerDead(id)`; the `unresolved` kind also clears when the container is provably `quiescent`. Consequence: when the exec exited but the per-chat container (PID1 sleep) lives, the folder is blocked forever (idle watcher refuses `tryIdleSlot` `drain.go:154`, `ForceDrain` stops only the quarantine map `drain.go:342-359`, `WaitDrained` returns `ErrExecutionUnproven` `drain.go:50-53`). Separately `Docker.quarantine` (`lifecycle.go:50-56`) is keyed by the `gopod.chat` label, which group native containers also carry (`internal/groupruntime/manager.go:340-341`), and blocks global admission (`agentkill.go:151`).
`startStream` reads `handle.Done` once (`runner.go:738`); `ExecStream` can send a demux error (`docker.go:405`) before the daemon `ExecError` (`docker.go:438`), so the reader marks the exec unknown although the exit was reported moments later.
Unit tests cannot reach `reconcileExited` (early return when `r.d == nil`, `drain.go:214`); `verifyQuiescent`, `stopQuarantined`, `inventoryLeftovers` have no unit tests (integration tag only).

### Tasks

#### T1. Typed evidence instead of string encoding
- Replace `unknownExecutions map[string]string` with `map[string]executionEvidence` where `type executionEvidence struct{ kind evidenceKind; containerID string; since time.Time; reason string }` and `evidenceKind` is an enum {`evidenceExecUnknown`, `evidenceInspectUnknown`, `evidenceContainerUnknown`}. Keep the global-block semantics for `evidenceContainerUnknown` (today the `"container:<id>"` map key blocks every folder; keep a separate set or key so behaviour is unchanged). Keep every public method signature (`ExecutionUncertain`, `WaitDrained`, etc.) unchanged. Pure refactor first: all existing tests must stay green with no behaviour change; commit.

#### T2. Clearing rules and the exec/exit ordering
- `reconcileExited`: `evidenceExecUnknown` clears when the container is dead **or** provably quiescent (`r.d.quiescent(ctx, id)` returns idle, i.e. no agent process inside; read `drain.go:315-330` for what quiescent proves and keep that proof). Keep `evidenceInspectUnknown` rules as they are. Make the function testable without Docker: introduce a small interface for the three calls it needs (`inspectByName`, `containerDead`, `quiescent`) and inject a fake in tests. Tests: exec-unknown with live idle container clears; with live busy container stays; with dead container clears; parked gate releases run after the last marker clears.
- `startStream` (`runner.go:730-750`): after the read loop ends with a non-`ExecError` error, give the daemon exit a bounded chance before marking unknown: if `handle` exposes exit state (`StreamHandle.exit`, `docker.go:293-327`, `withExitGrace`), use it; otherwise wait on `handle.Done` for the existing grace or the daemon `ExecError`. Reproduce the ordering in a unit test using the fake exec stream in `stream_cancel_test.go`.

#### T3. Quarantine scoped to personal containers
- `inventoryLeftovers` (`lifecycle.go:30-71`) must not quarantine a group native container: identify them by their own label (`internal/groupruntime/manager.go:330-345` shows the labels and `gopod-native-<lease>` naming) and leave them to `groupruntime` reconcile. Same filter for `killIdleChat` (`lifecycle.go:176-188`). Test with a fake container list.

#### T4. Dead code and docs
- Remove `leftoverStale` and `Docker.requirePolicyLabels`/`RequirePolicyLabels` (`lifecycle.go:78-84`, `docker.go:41`, caller `cmd/gopod/main.go:250`) if nothing else uses them (grep first), and fix the comment at `lifecycle.go:82-83`. Remove `errIdleWatcherShutdown` if unused (`lifecycle.go:205-207`).
- `docs/ARCHITECTURE.md` (runner section) or `docs/DEVELOPMENT-CHECKS.md`: one paragraph describing the evidence kinds and clearing rules. ADR `D{next}: Runner execution evidence is a typed per-folder state`.

### Out of scope (Noted)
Persisting evidence across restarts (relates to CP-037 `execution_claims`); unifying with `Elevated.blocked`.

## Brief 5: legacy scheduled task status — CHECK, pause reason, CAS

Worktree: `/Users/spaceinvaderz/_code/coriolis/gopod-legacy-task-status`, branch `tasks/legacy-status-check`, base `da529a8`.

### Context (verified findings)
Legacy tasks (`scheduled_tasks`, `execution_version=0`; `internal/store/tasks.go`) have `status TEXT NOT NULL DEFAULT 'active'` with no CHECK (`internal/store/schema.go:102`). Values by convention: active, paused, done. `next_run` doubles as a sub-state: 0 (user pause or done), -1 (`pauseMarker`, proactive global pause, `internal/proactive/pause.go:14`), >0 (scheduled; also left >0 by `RotateGroupEpoch` on paused rows). The single mutator `UpdateTaskStatus` (`tasks.go:143-157`) accepts any status/next_run with no compare-and-swap; correctness depends on every writer holding the scheduler `LockTasks` mutex (`internal/scheduler/task_lock.go`). Writers: `internal/scheduler/scheduler.go:147-152,158-164,219-220`, `internal/scheduler/restart.go:46-48`, `internal/scheduler/ipc_sink.go:87-90`, `internal/scheduler/ipc_manage.go:53-100`, `internal/control/tasks.go:357-391`, `internal/proactive/pause.go:43,84,101,107,116,137`, `internal/proactive/enable.go:145,167,221`, `internal/proactive/upgrade.go:54`, `internal/proactive/reschedule.go:137`, `internal/store/namespace_epochs.go:100` (`UPDATE scheduled_tasks SET status='paused' WHERE chat_folder=? AND status='active'`, no task lock, hits legacy and managed rows, keeps `next_run>0`). `task_run_logs.status` (running/success/error) has no CHECK and `FinishTaskRun` (`tasks.go:196-198`) has no status predicate; a `running` row left by a crash never finishes because `runTask` skips step 1 when the row exists (`scheduler.go:176`). `/tasks` list prints "—" for both 0 and -1 (`internal/control/tasks.go:115-117`); `/tasks resume` on a -1 row recomputes from schedule (`:362`) and silently drops the proactive marker so `ResumeAll` later reports it skipped (`pause.go:78-80`). `done` is terminal only by caller convention; store accepts `done -> active`.

### Tasks

#### T1. Explicit pause reason, no sentinel
- Migration (`internal/store/schema.go` migrations list, follow the existing guarded `ALTER TABLE` pattern): `ALTER TABLE scheduled_tasks ADD COLUMN pause_reason TEXT NOT NULL DEFAULT ''`; backfill `UPDATE scheduled_tasks SET pause_reason='proactive_global', next_run=0 WHERE next_run=-1 AND execution_version=0`.
- Constants in `internal/store/tasks.go`: `PauseReasonUser`, `PauseReasonProactiveGlobal`, `PauseReasonEpochRotation`, `PauseReasonUnsafePeriod` (scheduler non-owner period, `scheduler.go:147-152`). `TaskRecord` gets `PauseReason string`; every read scans it.
- `internal/proactive/pause.go`: replace the `-1` marker with `pause_reason='proactive_global'` and `next_run=0`; `ResumeAll` selects by reason; remove `pauseMarker`. `/tasks resume` (`control/tasks.go`) clears the reason; if the reason is `proactive_global`, keep current behaviour (resume) but the proactive restore set must then treat it as already resumed rather than "skipped": adjust `pause.go:78-80` to check the reason, not `next_run`.
- `RotateGroupEpoch` (`namespace_epochs.go:100`): restrict to `execution_version=0` (managed rows have their own revisioned API; leave them untouched and say so in the ADR), set `next_run=0, pause_reason='epoch_rotation'`.
- Tests: migration backfill; PauseAll/ResumeAll round trip with the new column (adapt `internal/proactive/*_test.go`); `/tasks resume` on a proactive-paused task; epoch rotation leaves managed rows alone.

#### T2. Status validity and terminal `done` at the DB
- Migration: triggers `scheduled_tasks_status_check` BEFORE INSERT and BEFORE UPDATE OF status raising when `NEW.status NOT IN ('active','paused','done')` and when `OLD.status='done' AND NEW.status<>'done'` for `execution_version=0` (managed rows already have their own triggers at `schema_tasks.go:73-104`; do not duplicate or conflict with them, and make sure the proactive "Enable after done" path still works: it deletes the done row and recreates, `internal/proactive/enable.go:171-174`). Same CHECK-style trigger for `task_run_logs.status IN ('running','success','error')`.
- Tests: invalid status rejected; done -> active rejected; delete+recreate still works.

#### T3. Compare-and-swap for status writes
- Replace `UpdateTaskStatus(ctx, id, status, nextRun)` with `UpdateTaskStatus(ctx, id, expectedStatus []string, status, nextRun, pauseReason)` (or add `UpdateTaskStatusFrom` and migrate all callers, then delete the old one): `WHERE id=? AND execution_version=0 AND status IN (...)`; 0 rows -> new `ErrTaskStatusConflict`. Every writer listed above passes the status it observed (the scheduler re-reads at `scheduler.go:137-141`; control reads at `:345-351`; IPC at `ipc_manage.go:53-100`). Callers that today tolerate a lost race (e.g. `SuppressForcedRestart`) log and continue; callers that reply to a user surface a "task changed, try again" message.
- `FinishTaskRun`: `WHERE task_id=? AND run_at=? AND status='running'`. For the crash-left `running` row: in `runTask`, when `GetTaskRun` finds an existing `running` row older than the slot (no turn finished it), finish it as `error "orphaned run log"` before logging the new run, or document why skipping is correct.
- Tests: concurrent pause vs scheduler advance yields exactly one winner (pattern `TestConcurrentTransitionsExactlyOneWins` in `task_runs_test.go`); `FinishTaskRun` does not overwrite a final row; scheduler non-owner pause and budget-delay writes asserted (`scheduler.go:149,161`, currently unasserted).

#### T4. Docs
- ADR `D{next}: Legacy task status is checked at the DB and written by compare-and-swap; pause reason is a column`. Update `docs/CONTROL.md` `/tasks` section if the list output changes (show the reason, e.g. "paused (proactive)").

## Brief 6: explicit login state and streaming view state

Worktree: `/Users/spaceinvaderz/_code/coriolis/gopod-telegram-view-state`, branch `telegram/login-stream-state`, base `da529a8`.

### Part A: login flow (`internal/telegram/login.go`, `elevated_login.go`, `login_handoff.go`)
Findings (verified):
- State is a flag combination: `loginSessions{starting map[int64]bool, sessions map[int64]*loginSession}` (`login.go:68-75`) plus `loginSession{awaitsCode, submitted, private, elevated, ctx, exec, thread, actorID}` (`:37-63`). Derived states: IDLE, STARTING, AWAIT_CODE, DEVICE_WAIT, SUBMITTED, VERIFYING-untracked. `private` is always true (`:285`, `elevated_login.go:39`), so every `!session.private` guard is dead.
- The Telegram handler goroutine blocks on `exec.ReadLine()` while STARTING (`:295-321`) and SUBMITTED (`:472-486`). `loginCtx` derives from `context.Background()` (`:267`) bounded only by `loginTimeout` = 10 min (`:21`). `bot.Run` waits for `b.inboundHandlers.Wait()` (`bot.go:367`, `Add` at `:423`), so shutdown can stall up to 10 min behind a login.
- `finishLogin` frees the slot (`cleanupLoginSession`, `:492`) before verification (`:494-500`): a new `/login` can start while `checkAuthAfterLogin` still reads the same container.
- Sessions are keyed by chat id only (`:129-138`); a code pasted in another private-chat topic is consumed and answered in `session.thread`.
- `finishLogin` checks only `context.DeadlineExceeded` (`:488`); a session cancelled by `cancelUnsafeLogin` (`:559-567`) still verifies and replies.
- No tests for the timeout notice ("Login session timed out"), URL extraction failure, duplicate code submission (`submitted`), `cancelUnsafeLogin`.

Tasks:
- A1. Introduce `type loginState uint8` {idle, starting, awaitCode, deviceWait, submitted, verifying} stored on `loginSession.state` and mutated only under `ls.mu` through one method `transition(from []loginState, to loginState) bool`; `starting` map goes away (a `loginSession` in state `starting` replaces it). Keep `awaitsCode`/`submitted` as derived accessors if that keeps the diff small. Remove the dead `private` field and guards. Key sessions by `(chatID, threadID)` so a code pasted in another topic is not consumed (`interceptLoginInput` and `handleLoginCode` look up by both). All existing login tests must pass; add table tests for the transition table.
- A2. Shutdown-bounded contexts: derive `loginCtx` from a Bot-level context that is cancelled when `Run` stops (find the context `Run` uses or add `b.lifetimeCtx` set in `New`/`Run`), still with the 10-minute deadline. `finishLogin` must return without verifying or replying when the ctx is cancelled for any reason (`ctx.Err() != nil`, not only `DeadlineExceeded`) and must send the timeout notice only on deadline. Test: cancelling the bot context while a fake exec blocks in `ReadLine` makes the handler return promptly and `Run` not stall.
- A3. Slot released after verification: move `cleanupLoginSession` after the verification result is known (state `verifying` keeps the slot; a second `/login` during `verifying` gets the "already" reply). Test it.
- A4. Tests for: timeout notice, URL-extraction failure, duplicate code while `submitted`, `cancelUnsafeLogin` prevents verification.

### Part B: streaming view state (`internal/telegram/streaming.go`, `agentwait.go`, `resume.go`)
Findings (verified):
- The reply view is encoded in locals of `streamReply` (`draftID`, `placeholder`, `accumulated`, `full`, `overflow`, `answering`, `silent`, `dirty`) plus `Bot.waitPlaceholders` (`agentwait.go:42-64`), `turns.placeholder_msg_id` (`internal/store/turns.go:149-152`), per-chat `draftsOff`/`draftBadReqs` (`draft.go`) and `goneThreads` (`threadgone.go`).
- Agent error in EDIT mode: `editMessage(ctx, chatID, placeholder, "")` (`streaming.go:636-641`) which `editMessageKBLocked` renders as a lone "❘" (`:116-118`); the error text is a separate message; the cursor placeholder is never cleaned up.
- Main-chat final edit failure is only logged; `recordReply`/`noteReply` still run with the placeholder id (`:689-700`; `finalEditLost` returns false for `ThreadID == 0`, `:761`) so a turn is `done` with no visible answer (e.g. placeholder deleted).
- `turns.placeholder_msg_id` is write-only: `reusePlaceholder` is hard-coded `return 0` (`resume.go:102-104`) and `PlaceholderMsgID` from `pickBatchItem` (`:121-122`) is unused.
- `streamReply` returns at `:299-301` (`finalGenerationRetired`) before `takeWaitPlaceholder` (`:346`), leaking the `waitPlaceholders` entry and leaving the wait message as sent.
- No tests for the ERROR path, `resumingBanner` opening, new-placeholder failure during overflow (`:447-450`), final edit failure in thread 0.

Tasks:
- B1. Extract a `replyView` struct in a new file `internal/telegram/reply_view.go`: fields `mode` (enum none/draft/edit), `draftID`, `placeholder`, `overflow`, `silent`; methods that are the only mutators: `openDraft(id)`, `openEdit(msgID)`, `fallbackToEdit()`, `overflowDraft(newID)`, `overflowEdit(newPlaceholder)`, `stop()`, `final()`, `fail()`. `streamReply` uses it instead of the bare locals; behaviour identical; all streaming tests green; commit as a pure refactor.
- B2. Error path: on `res.Err` with a placeholder, edit the placeholder to the error text (through `handleStreamError`'s formatting) instead of sending a separate message, or delete the placeholder then send the error; pick the option that keeps the existing error-message tests passing and leaves no bare cursor. Test: after an agent error no message contains only "❘".
- B3. Main-chat final edit failure: treat it like the topic case: when `editMessageHTMLKB` fails for `ThreadID == 0` with a not-found/can't-edit class, fall back to `sendFinal` and only then `recordReply`; if that fails too return `errReplyUndelivered`. Test it.
- B4. Fix the `waitPlaceholders` leak at `:299-301` (pop/finalize the wait placeholder before returning). Remove `reusePlaceholder` and the unused `PlaceholderMsgID` plumbing in `pickBatchItem` (keep `recordPlaceholder` and the DB column: control bindings depend on them). Test the leak fix.
- B5. ADR `D{next}: Login and reply view are explicit state machines in the Telegram layer`.

Run the full `internal/telegram` suite (about 75 s) and `-race` on it before pushing.
