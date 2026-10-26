# Task reliability implementation plan: CP-004 / CP-021 / CP-022

Status: proposed, design only. Date: 2026-10-26. No runtime changes or deployment are
included in this planning change. Each PR below is independently reviewable; activation
stays disabled until its prerequisites and migration gate pass.

Canonical contracts:
[CP-004](../specs/2026-10-26-isolated-task-sessions.md),
[CP-021](../specs/2026-10-26-task-outcomes-checkpoints.md),
[CP-022](../specs/2026-10-26-schedule-execution-policies.md).
These dated implementation contracts supersede earlier broad alternatives in the proposals.

## Dependencies and fixed boundaries

CP-002 supplies one additive `ScopeRef` foundation in `registered_chats`,
`SnapshotScope` / `ValidateScope`, immutable `turn_results` / `turn_result_members`,
`FreezeTurnResult`, and the publication/outbox boundary. Do not create another namespace
registry or duplicate the model's final text. Task tables reference the same result ID.
`ExecutionRef.kind=isolated_task` must never call foreground `CompleteConversation`,
append chat history, change native conversation sessions or write `reply_msg_id`.

Persist source intent, authorized actor/service principal, destination `ScopeRef`,
definition revision and delivery mode at admission. Actor grants and container/profile
policy are separate host authority: a folder named `owner` must not promote a task to
owner mounts, credentials or tools. Require a compatible policy fingerprint and actual
container authority; reusing a privileged personal container is not a permission check.
Group managed tasks/reminders remain disabled until CP-020 qualifies their boundary.
Provider/model snapshot happens only after acquiring the actual chat execution slot,
before native spawn, compatible with CP-018. Its UI is not a prerequisite: adapt the
current host provider resolver without claiming per-thread selection already exists.

No general worker engine, Temporal adoption, automatic quota spending, unrestricted
retries, or new personal filesystem migration. CP-010 provider fallback confirmation
plus checkpoint/effect reconciliation is required before an explicitly new attempt after
potential execution; quota errors alone never authorize replay.

## Source baseline and defects addressed

Source baseline: `33e026a291fbe577efabf43e9d0b6e760fd19ece`.
`internal/scheduler/scheduler.go` currently logs, enqueues and advances schedules in
separate writes. `internal/queue/queue.go` batches by thread and retries/replays failed
items. `internal/runner/runner.go` uses foreground provider sessions; `fresh.go` is a
one-minute maintenance path. Task items are intentionally `ChatID=0`, `IsOwner=false`:
legacy IPC jobs have no automatic stdout delivery. Preserve that behavior.
`ErrStopped` currently reaches done hooks with nil error, so task success cannot be
inferred from that hook. Existing `task_run_logs` remain compatibility logs, not proof
of goal achievement, effect settlement or delivery.

## PR 1 — additive storage and versioned definitions

Files: `internal/store/schema.go`, `tasks.go`, `task_requests.go`; new
`task_runs.go`, `task_occurrences.go` and corresponding store tests.
Depends on CP-002 identity/result schema. Add fields and tables from the canonical
contracts: legacy version 0 vs managed version 1; definition revision; task kind,
session/delivery mode, scope, policy/default bounds; occurrence/run/attempt ledgers and
host verification records. Existing rows retain `legacy_main`, `legacy_ipc`, thread 0,
and existing scheduling semantics. No fabricated historical outcomes or ScopeRefs.

Implement strict kind validation (literal reminders do not need a fake prompt), expected
revision mutations, uniqueness on `(task_id, definition_revision, nominal_at)`, typed
execution transitions, and one transactional `AdmitTaskOccurrence` API. Its transaction
inserts the durable turn through a transaction-aware store helper; calling existing
`Queue.Enqueue` or a helper using the outer DB inside that transaction is forbidden.
The API returns committed turn/outbox IDs for later dispatch. Separate schedule
`cancel` from `stop-run` execution intent. Record skipped/coalesced/expired decisions.

Acceptance: temporary SQLite tests for concurrent admission/CAS, constraint violations,
revision edits, wrong scope, complete rollback at every inserted row, and legacy fixture
round-trip. Prove one admission after competing callers; no partial cursor advance.
Keep activation off. Migration is additive and restart-idempotent. Replace managed
`Store.DeleteTask` hard deletion with revisioned tombstones; use RESTRICT/no-cascade
foreign keys for managed ledger references. Legacy physical deletion is allowed only
with no managed references, checked transactionally. No automatic managed evidence
purge in this slice; later explicit maintenance requires settled dependencies, released
reservations and verified audit export. Test raw parent deletion rejection and refusal
to purge unresolved execution/effect/delivery evidence.

## PR 2 — managed dispatch and conservative recovery

Files: `internal/queue/queue.go`, `queue_test.go`, `internal/store/turns.go`,
`cmd/gopod/main.go` (turn-store adapter), task ledger helpers.
Depends on PR 1 and CP-002 frozen-result recovery contract.
Carry run/attempt/session-kind IDs in durable turn mappings. Add dispatch of an already
persisted turn after commit; a crash before in-memory dispatch is recovered from the
store. Managed task items are singleton batches and never coalesce with Telegram turns
or another run, even at thread 0. Preserve existing interactive behavior.

Persist attempt reservation and execution snapshot before spawn; failure to commit means
no spawn. Managed recovery distinguishes proven-never-started from may-have-started:
frozen result means publication only; potentially started means `execution_unknown`,
no model retry. Reconstruct unknown capacity reservations after restart; hold them until
local exit or operator reconciliation evidence. A finished attachment/timeout is not
proof of process exit. Known local exit can release capacity without authorizing replay
of uncertain remote effects. Keep this single-controller design explicit.

Acceptance: queue fixtures with task+Telegram same thread, two task runs same thread,
crash after SQL commit/before dispatch, after reservation/before spawn, and after spawn.
Assert no native invocation for frozen/unknown attempts; never map nil stopped-hook error
to task completion. Assert current interactive batching/retry tests remain unchanged.

## PR 3 — task execution and attempt-bound reporting

Files: `internal/runner/runner.go`, `provider.go`, `provider_claude.go`,
`provider_codex.go`, `exec_interactive.go`, `policy.go`, `lifecycle.go`,
`docker_args.go`, `mounts.go`, `agentenv.go`, lifecycle/session tests;
`internal/telegram/handler.go`; new task execution/report helpers as appropriate.
Depends on PR 2. This PR implements and qualifies the bounded personal task-role
policy below; it does not depend on the full CP-020 group migration.
Implement dedicated `RunTaskStream`, `RunTaskCmd(prompt, systemPrompt)` and ephemeral
native flags using the ordinary bounded task execution lifetime (default 5 minutes,
approved maximum 60), not `RunFresh` maintenance preemption. Snapshot provider/model
inside the actual execution slot. Compile task-scoped working set; never adopt/resolve
foreground sessions, handoff, compact or automatic memory. A task preceding the first
foreground request must leave no task transcript eligible for legacy adoption.

Implement a host-derived `EffectiveExecutionPolicy` for roles `foreground_personal`
and `scheduled_task_personal`. Its deterministic fingerprint covers role, immutable
ScopeRef/policy revision, resolved mount sources/targets/modes, env allowlist names,
provider credential binding, image identity and granted tools/IPC permissions; never
hash/log secret values. Task defaults derive registered-tier mounts/env/tool grants,
not owner privileges from folder; any extra required grant needs explicit host policy.
Store the expected fingerprint on the attempt and container labels. Existing `Ensure`
reuses folder/image and therefore does not establish this authority today.

Under the existing per-folder lock, compare requested policy against host-known creation
policy and Docker-inspected effective configuration before reuse. Missing/unknown labels,
unsupported inspection, unqualified credential sharing or incompatible config fail
closed. On a known mismatch, wait for proven exit of any active execution, then stop/
remove and recreate the folder container with requested policy; do not recycle while an
unknown attempt holds its reservation. Use the same guarded path when returning to
foreground: reconstruct the existing personal default policy and preserve persistent
foreground native sessions/credential files on host, never delete or import them during
container replacement. A task's ephemeral command must not change foreground session
selection. This is bounded runtime authority checking, not proof of filesystem or native
account isolation. If credential persistence cannot support the proposed task role
without exposing unauthorized state, keep that provider/task role disabled pending a
qualified adapter; do not weaken policy merely to pass a smoke test.

Use host-issued attempt capability for strict `task-results` v1 reporting, maximum
32 KiB envelope and the canonical bounded outcome/checkpoint fields. Parse into a
provisional agent report, never host verification or success authority. Revoke capability
on settlement/cancellation; reject spoofed scope/run/attempt/late messages. No automatic
host-path fetching or evidence execution. `IsOwner=false` and trusted actor/profile
checks remain in force independently of folder and image.

Acceptance: fake provider command/stream tests for both ephemeral commands, system
prompt and timeout, no foreground/session DB writes, provider switch while queued vs
started, wrong policy/container denial, report limits/schema/token revocation. Add
`lifecycle_test.go`, `docker_args_test.go`, `mounts_test.go` and `agentenv_test.go`
fixtures for owner foreground → task → foreground recycle, retained personal native
credentials/session IDs, no owner-only task mounts/env/tools, stale/missing labels,
inspected-config mismatch, scope revision change and unknown-execution blocking recycle.
Compatibility gate: both providers must retain unchanged foreground default behavior
after role transitions, with task execution denied wherever authority cannot be proved. Native
subscription smoke is a separate explicit acceptance gate, never a unit-test side effect.

## PR 4 — settlement, result publication and task visibility

Files: runner completion boundary; `internal/scheduler/scheduler.go`, store task ledger;
`internal/ipc/tasks.go`, `task_management.go`; `internal/control/tasks.go`,
`internal/telegram/handler.go`, task panel tests.
Depends on PR 3 and CP-002 durable results/outbox.
Freeze final text before result emission/slot release, using shared result storage.
Finalize task execution/report metadata and publication reference transactionally;
`legacy_ipc` produces no automatic output/outbox entry. `controller_result` publishes
through the shared outbox; failed delivery cannot rerun the model or relabel its goal.
Expose execution, agent goal report, host checks and delivery separately in `/tasks`.
Keep current pagination and add revision-bound controls and explicit `stop-run` action.

Cancellation first persists request/revokes reporting grant, then requests termination
and awaits actual exit; missing exit becomes unknown with held reservation. Final text
from a cancellation race is retained diagnostically but cannot fabricate completed status.
Use CP-002 publication identity so a legacy agent's own IPC send is not duplicated.
Definition deletion disables future admission and transactionally cancels proven-unstarted
turns; synchronize with dispatch so reserved/potentially started attempts follow actual
cancellation/unknown settlement instead. Tombstones preserve run/result/outbox evidence.
Pending delivery cancellation uses CP-002's state machine; deletion cannot relabel an
uncertain send as unsent. Add delete/dispatch races, queued/running/unknown attempts,
frozen results, unknown delivery, name reuse and backup/restore fixtures.

Acceptance: stop vs result races, nil stopped error, invalid reports with valid frozen
result, result freeze/publication crash failpoints, pending publication after restart,
legacy IPC no stdout duplication, and delivery failures with no model invocation.

## PR 5 — schedule policy and literal reminders

Files: `internal/scheduler/scheduler.go`, `ipc_manage.go`, new policy planner/tests;
`internal/store/tasks.go`, task request/occurrence helpers; `internal/ipc/tasks.go`,
`tasks_test.go`, `task_workflow_test.go`; `internal/control/tasks.go`, task panels;
`internal/runner/chattmpl/SCHEDULING.md.tmpl`.
Depends on PRs 1–4, CP-002 outbox, and personal scope authorization.
Implement strict opt-in IPC v2 and trusted thread grants, preserving v1 behavior.
Atomic admission owns cursor advance, overlap/misfire decision and durable turn or
literal-reminder outbox insertion. Schedule persisted turns only after commit. No agent
or runner invocation for typed literal reminder creation/dispatch. Natural-language
interpretation, if offered, is separate and discloses its model use.

Use canonical defaults: overlap skip; misfire skip with 120-second grace; bounded queue/
catch-up default 1 maximum 10; maximum 100 occurrence candidates/task/tick plus gap
summary; reminder text 3500 UTF-8 bytes, default expiry nominal+24h. Explicit trusted
IANA timezone; no guess from server local time. Managed interval anchors to nominal
cursor. DST fall-back once uses persisted local wall-slot deduplication; optional both
retains two UTC slots; spring gaps skip. Clock rollback never rewinds cursor. Legacy
library fold/now-based interval behavior remains until migration preview/approval.

Acceptance: fake-clock Belgrade spring/fall fixtures, UTC, restart/fold dedupe, forward/
backward clock jumps, >100 missed intervals, all overlap/misfire combinations, bounded
coalescing cancelling displaced durable turns, max-runs/expiry, unknown reminder sends,
and mock invocation counter proving zero model calls. Test invalid policies pause with
reason rather than claiming objective done. Show timezone, next UTC/local time, skips,
expiration and separate outcome/delivery state in paginated task UI.

## Activation, migration and rollback

1. Inventory definitions, queued/running legacy turns, foreground adoption candidates,
   bindings, provider/container policy and existing self-delivery prompts. Export a
   reviewed migration manifest; never recreate FreeFeed or mutate live tasks in tests.
2. Reconcile potentially started old tasks before enabling managed recovery. Pending
   legacy turns migrate only with unambiguous host scope and explicit operator choice.
   Existing tasks remain legacy by default; preview all mode/policy/timezone changes.
3. Enable one personal managed test task after unit/failpoint acceptance and independent
   review. Native process-tree termination and native-account isolation remain gates;
   remote-effect completion cannot be proven from local process exit alone.
4. Verify store state, run/attempt/result correlation and delivery independently. Task
   goal report, checker result, user acceptance and successful Telegram delivery are
   separate evidence. Group tasks stay blocked pending CP-020 qualification.
5. Roll back by disabling new admissions, draining/reconciling active attempts and
   retaining pending outbox/results/reservations. Do not downgrade to an older binary
   that blindly replays managed turns: quarantine/export managed unfinished rows first
   and refuse unsafe startup. Do not drop additive tables to erase unknown effects.

Run focused Go tests per PR, then the existing affected-package suite and repository
required checks for implementation PRs. This documentation-only change requires link,
source-reference, diff and independent review; no CI wait or workflow configuration
change. Runtime/resource/native-account/Telegram acceptance and production deployment
are deliberately not claimed by these specifications.
