# CP-037 implementation contract C: drain and forced restart

Status: implementation specification for review, 2026-11-01. Owner requires no
unexpected interruption during planned restart and explicitly requests
`/restart force` to terminate background work. No code/deployment authorized.
[Runtime contract](2026-11-01-session-runtime-implementation.md).

## C1. Current code audit (baseline cee5cac)

| Path | Observed behavior | Gap |
|---|---|---|
| runner/lifecycle.go idleWatcherSweep | Age + container lock, no execution claim check | Long foreground/legacy task can be removed mid-run |
| runner/task_run.go | Managed task refreshes activity every minute | Heartbeat is not exclusion; small TTL/stalls remain unsafe |
| cmd/gopod/restart.go request | Calls root stop after notice/state commit | Cancels active work rather than waiting for natural completion |
| cmd/gopod/shutdown.go + queue.Close | Cancel app/workers, then wait for exit | Graceful goroutine exit is not completion of LLM/task work |
| runner/lifecycle.go CleanupLeftovers | Stops dev/old-version/policy-stale running containers | Startup version change is not evidence of idle/dead work |
| runner/continuity.go provider switch | Acquires agent slot before stop/remove | Useful serialization, but existing cleanup uncertainty still matters |
| runner/exec_guard.go | Requires slot and resolved managed holds before recycle | Preserve this; unify all other mutation paths with coordinator |

Stream cancellation kills the agent in runner.go; keeping a same-version Docker
container alive at startup does not prove stream reattachment or uninterrupted
response delivery. Current docs/comments suggesting that are insufficient evidence.
This audit is source-based; no live job was interrupted for verification.

## C2. Ordinary /restart: drain before cancelling application context

Owner-private Router command; no args means graceful drain, not force. Persist a
restart operation UUID, mode, scope, generation cutoff and phase. Supported phases:
requested -> draining -> ready -> restarting -> verifying -> completed, with
blocked/failed/cancelled outcomes. Duplicate requests return operation status.
Unknown arguments fail before mutations. Existing group/authority restart denials
remain until a separately qualified group restart procedure exists.

1. Validate supervisor/instance ownership and affected runtime inventory.
2. Atomically close execution admission for the controller's supported scope,
   then persist draining intent before any stop. Continue durable ingress with a
   visible restart-wait status; do not start new task occurrences or agent work.
3. Keep execution contexts, streaming readers, required IPC/MCP/memory services,
   result capture and outbox preparation alive. Control commands/Stop remain usable.
4. Wait for active foreground, legacy/managed tasks and maintenance operations to
   settle naturally. Require no unknown execution or unregistered child writers.
5. Persist terminal state/result references and recovery checkpoint. Ready means
   no active claims; then stop exact idle runtime IDs, cancel application services
   in dependency order and let the existing supervisor restart the controller.
6. Startup acquires instance lock, reconciles operation/containers/claims and opens
   admission only after verification. Resume queued requests once, preserving IDs.

A drain deadline only reports blocked/postponed and leaves execution alive; it
MUST NOT escalate to force. Provide status and cancellation of a pending drain
(before ready), restoring admission atomically. New ingress remains durable during
waiting. Snapshot inventory after admission closes; reconcile late startup claims
and newly observed runtimes before declaring ready. No snapshot-to-stop race.

Planned deploy runbooks must use prepare-drain/readiness before external Compose
replacement or SIGTERM. An external docker restart/kill, host failure or an
orchestrator's hard deadline cannot preserve in-flight inference. First SIGTERM
may attempt bounded drain, but no promise survives external SIGKILL. Do not call
that a completed graceful restart. Hot controller stream reattachment is not
implemented by this contract; planned restart waits for streams to finish first.

## C3. /restart force

Owner-private only, explicit danger confirmation bound to actor, scope, operation
ID and inventory revision. Explain: active interactive replies also terminate;
all pending/active background work in the supported controller scope is cancelled;
recurring definitions, files and long-term memory remain. New work after the force
barrier is queued for post-restart. A changed inventory requires renewed confirmation
or inclusion under the explicitly shown 'all work admitted before confirmation'
cutoff; use the latter as the initial implementation contract.

Under admission barrier, persist force intent and cancellation of every queued
background occurrence/attempt before signals. Include managed, legacy scheduled,
maintenance and parser runs; do not include arbitrary unrelated host processes.
Active foreground becomes stopped-by-forced-restart; pending foreground messages
remain queued with their original session/generation. Running background jobs
receive cancellation intent, then bounded TERM -> KILL escalation; use dedicated
container termination when the process tree cannot be proven dead. Old shared
containers require cancelling all affected attempts before stopping them.

Confirm death before releasing execution/project claims. Proven cancelled managed
attempts use existing cancelled state/reason; ambiguous exits use execution_unknown
with cancellation intent retained, not fabricated success/cancel settlement.
If Docker is unavailable or evidence unresolved, force operation becomes blocked:
do not restart-and-replay or release claims. Repeated force is idempotent and
continues reconciliation. Force is permission to interrupt known work, not to
ignore unknown execution authority or existing group boundaries.

Persist cancellation tombstones/cutoff so recovery, retries and catch-up do not
recreate cancelled occurrences. Preserve recurring definitions and their ordinary
future schedule. Occurrences due during the force barrier are recorded suppressed
through the resume watermark (using the existing occurrence ledger); future
occurrences after reopening follow configured policy. Legacy scheduling needs an
explicit adapter for this suppression before force can claim complete coverage.
Any delivery of cancelled results is marked partial/interrupted; no success result
is invented. Already committed external effects cannot be rolled back by force.

## C4. Idle eviction, upgrades and startup cleanup

Idle TTL and desired image/provider changes request a drain of the exact runtime,
never invoke a competing direct stop path. Serialize activity acquisition with
drain acquisition; compare generation before stop/remove. Busy runtime retains its
old image/profile until settled; new authorized runtime cannot reuse its writable
assets while it runs. After cleanup failure, keep claims and report blocked.

Replace startup blanket version/dev cleanup with inventory reconciliation.
Known active/unknown leftovers are quarantined from new admission; version labels
alone cannot authorize termination. Planned upgrades arrive already drained.
Crash recovery records interrupted/unknown work and never silently resumes a
partly executed action as a fresh turn. Durable spool/re-attach is a future feature;
absence of it is surfaced, not hidden by preserving a container.

## C5. Acceptance (required before rollout)

C01 silent foreground longer than TTL survives, then idle eviction succeeds.
C02 managed and legacy task exceed TTL without eviction, including short configured TTL.
C03 drain race with Exec start admits either before barrier or after restart, never during.
C04 ordinary restart during streaming/tools/background finishes existing work before stop.
C05 drain timeout does not send TERM/KILL; status/cancel remains functional.
C06 new ingress during drain persists and resumes once after restart.
C07 force cancels queued/running tasks and active foreground; pending foreground survives.
C08 force preserves schedules/files/mappings and suppresses cancelled catch-up occurrences.
C09 daemon unavailable/detached child/failed kill retains unknown claims and blocks restart.
C10 stale/foreign/replayed confirmation and simultaneous restart requests are safe.
C11 crash after each intent/signal/settlement boundary resumes operation idempotently.
C12 old-version/dev startup does not delete a busy leftover.
C13 result capture and outbox checkpoint complete before ready; delivery uncertainty retained.
C14 provider/image update and deployment use the same drain barrier.

Use deterministic barrier tests plus local disposable Docker processes with a
sentinel file and long silent intervals. Test both streaming and buffered runner,
legacy tasks, managed tasks and maintenance. Native-provider and Telegram smoke
checks require an explicitly scoped later acceptance run. Never use live schedules
as kill fixtures. Update /restart help and CP-031 restart documentation; record a superseding restart ADR when
implemented. Force is the only planned restart path allowed to cancel active work,
apart from a separate explicit Stop or existing visible execution deadline.

## C6. Operator-visible failure, cancellation and external effects

Refinement requested by owner on 2026-11-01 after the second review.

- If force has cancelled work but container/controller restart fails, report both
  facts separately: "Work was stopped; restart did not complete." Where death is
  unproven, say "Cancellation requested; execution status is unknown; restart is
  blocked" instead. Never report all work stopped solely from sent signals.
  Persist the operation ID, per-attempt outcomes and failed phase. A retry resumes
  that operation idempotently; it does not resubmit cancelled tasks or silently
  broaden the original cancellation cutoff. Newly admitted work requires a new
  explicitly scoped force operation.
- Show a Cancel restart button while an ordinary restart is draining. Bind it to
  actor/scope/operation/revision and CAS draining -> cancelled. If cancellation
  wins, restore admission and keep running work intact; if ready/restarting wins,
  reject the stale button with current status. Cancellation is unavailable after
  force cancellation has begun and cannot resurrect terminated work. Status and
  cancellation remain reachable outside worker admission.
- Distinguish local process termination from external operation completion. A
  submitted deployment, email or remote job may continue after the container dies.
  Retain connector operation IDs, receipts and external settlement state separately
  from local execution state. A dead local process may release its execution claim;
  unresolved external effects keep their conflict/retry guard. Restart may complete
  with these external guards retained, and the result must show the unresolved
  operations. Reconcile through provider receipts/read-back or explicit owner
  settlement; never blindly replay because the local attempt was cancelled.

Additional acceptance: C15 force partly succeeds then Docker fails: truthful status
and idempotent retry; C16 cancel-drain versus ready race has one winner and does
not drop queued input; C17 remote operation survives local termination, conflict
guard survives controller restart and no duplicate external action is dispatched.

## C7. Provider waits and transitions during drain

Apply [D6](2026-11-01-session-routing-implementation.md): quota waits and pending
fallback are durable queue state, not running operations to wait on indefinitely.
Drain forbids their dispatch; force cancels active-turn fallback lineage. Account
cooldowns survive restart. A preparation operation is safe to defer only after
proving it has not started execution or external effects. C04/C06/C07 must include
these cases in addition to ordinary active native processes.

## C8. Reconcile Telegram status controls after emergency interruption

Owner requirement, 2026-11-01: messages carrying Stop controls must reflect the
actual attempt state after an emergency interruption, including controller crash,
worker/container failure, OOM, force restart or lost execution connection.
No live Telegram message is changed by this specification.

### Durable binding and desired presentation

Persist control-message bindings to ScopeRef/chat/thread, logical turn, attempt,
session/context epoch, runtime generation and presentation revision. Record exact
message IDs as soon as Telegram acknowledges creation, including replaced/split
control surfaces; existing turns.placeholder_msg_id is a migration source, not a
complete per-attempt binding. The in-memory inflight registry is not recovery truth.
Track desired versus last-acknowledged presentation and retry state durably through
the existing delivery/edit infrastructure; do not create a competing send queue.
Never assume a draft ID is a persistent message ID.

| Proven execution state | Desired user presentation | Controls |
|---|---|---|
| Known queued/not started, retained after restart | Waiting to resume after restart | Exact-turn cancel may remain only if durably implemented |
| Reconciliation in progress / execution unknown | Connection lost; checking whether work is still running | Remove ordinary in-memory Stop; show safe status/reconciliation action if implemented |
| Proven unexpected termination | Response interrupted by an error/restart; result is incomplete | Remove Stop; continuation offered only through effect-safe explicit retry contract |
| Proven force cancellation | Stopped by forced restart | Remove Stop; no implicit retry |
| Proven completed, final delivery pending | Work completed; delivering result | Remove Stop; reconcile delivery separately |
| Normal running after verified attach | Working | Stop bound to that exact live attempt only |

No current stream reattach is promised: keeping a container alive does not justify
restoring 'Working' or registering a fake stop handle. Uncertain state must not be
rendered as stopped/completed. A stale Stop press answers promptly with current
status and schedules repair; it never selects 'whatever runs in this topic now'.
Authorization uses the actual callback message and actor plus durable scope, not
callback data alone. New attempts need newly bound controls; old controls cannot
cancel a retry or a new session.

### Recovery and editing protocol

1. Commit execution/termination evidence and desired UI revision independently of
   Telegram availability. Startup scans unreconciled control bindings, including
   terminal turns whose last edit failed, not only unfinished turns.
2. Reconcile process state before choosing terminal wording. Stop stale local
   refreshers; one serialized writer owns each persistent Telegram message across
   streaming, final delivery and recovery. Before dispatch recheck binding and
   revision; an older progress update must not overwrite a terminal status.
3. Edit the original control message in the original chat/thread, preserve the
   recoverable partial text and mark it incomplete. Remove Stop and obsolete
   fallback/confirmation controls with explicit reply-markup clearing, not merely
   omission of a keyboard argument. Preserve unrelated valid controls only when
   their own revision remains current.
4. Persist acknowledged revision. On timeout keep update pending/unknown and retry
   convergent edits of the known message ID with bounded backoff; respect retry-after.
   A late ambiguous old edit can still arrive externally, so reapply the newest
   desired revision after uncertain writes settle. Client-side generations alone
   cannot fence Telegram edits already sent over the network.
5. Terminal state must dominate any late chunks, progress or refresh events. Do
   not edit an archived attempt message into the output of a newer attempt. An
   old-scope registration change prevents editing or notifying a new owner.

If only a partial checkpoint is available, do not invent missing text. Prefer
removing markup from the known message and sending a separately tracked status
notice to preserve text already displayed. If original text can be safely rebuilt,
append a short status within Telegram formatting/length limits. For split outputs,
clear controls on every tracked control-bearing message, not every historical
content message. Recovering user-visible partials requires bounded persisted
checkpoints or existing durable result data; raw provider thoughts are excluded.

If Telegram rejects editing permanently (deleted message, permissions, obsolete
scope), stop edit retries and record repair outcome. A single correlated fallback
notice is allowed only in the same still-authorized conversation through existing
outbox semantics. Never redirect a deleted topic's notice into main automatically.
An ambiguous fallback send remains uncertain and is not blindly resent. If a send
succeeded but its message ID was lost before persistence, do not claim the old
button was removed; record this repair limit and rely on stale-callback rejection.

Drafts are transient and may already have expired. Stop refreshing them after
terminal/unknown state, use a supported terminal update only when qualified, and
prefer the persistent control message for recovery. Do not create a new draft on
startup just to recreate the old Stop button. Missing edit rights or Telegram outage
means eventual UI repair, not an immediate-cleanup guarantee.

### Integration and acceptance

Include repair intents in restart recovery and graceful finalization; Telegram
outage does not hold execution/project locks or block safety shutdown indefinitely.
Persist outstanding UI work before restart. Normal restart preserves live Stop
while draining; force changes wording to stopping until death is proven, then
terminal. Task controls, if exposed, bind task/run/attempt rather than a topic's
interactive Stop. Rate-limit recovery scans so a crash affecting many sessions
does not flood Telegram or starve ordinary replies. Expose pending/permanent UI
repair failures in diagnostics without leaking content or credentials.

C18 crash with a persisted Stop placeholder repairs status and clears markup.
C19 unknown exec never reports 'stopped'; stale Stop cannot affect a new attempt.
C20 late stream/final/recovery edits converge to latest terminal presentation.
C21 edit timeout/429/outage survives repeated restart and does not duplicate notices.
C22 deleted message/topic or revoked scope does not leak status into another chat.
C23 split placeholders, empty/partial response and transient drafts preserve valid
content and clear all known obsolete controls.
C24 completed execution with failed final delivery is not mislabeled interrupted.
C25 create-ack-before-binding crash is reported as a repair limitation; stale
callback remains safe. C26 force/clear/new/provider-fallback races cannot revive
old Stop controls. Unit/fake-transport tests require a later real-client acceptance
run; success of a queue-state test alone does not prove Telegram presentation.

## C9. Canonical recovery presentation and action matrix

This matrix consolidates C8 and routing contract D. Execution evidence selects the
row; Telegram delivery/edit status is a separate axis. All offered actions require
host authorization, exact attempt/generation binding and revision validation.

| Durable execution evidence | Display meaning | Allowed actions | On controller recovery |
|---|---|---|---|
| Accepted, provably not dispatched | Queued; not started | Cancel queued request; Start only if explicitly held | Ordinary queued work resumes once through admission; held work stays held |
| Live process verified, control channel usable | Running / waiting on tool | Stop exact attempt | Continue only with qualified attachment; do not pretend a stream was reattached |
| Started or dispatch ambiguous, death/effects unproven | Checking execution; status unknown | Status/reconciliation; no Start/Retry/Continue | Retain claims; reconcile before allowing replacement work |
| Unexpected exit proven, effects reconciled | Interrupted; partial result | Continue with state verification, or close turn | Never blindly replay original tool actions; continuation is a new bound attempt |
| Explicit Stop/force cancellation proven | Stopped by user / forced restart | View partial result; explicit new continuation request if safe | Do not auto-replay; cancelled background occurrences remain cancelled |
| Execution completed, result durably captured, delivery pending/unknown | Completed; delivery pending/unknown | View delivery status; existing settlement flow | Repair delivery/status; do not invoke the model again |
| Completed and delivered | Completed | Existing valid result actions only | Remove obsolete Stop; no execution retry |

A generic Retry button MUST NOT cover both not-started and partially executed
work. Start refers only to a provably not-started held request and atomically
claims it so concurrent recovery cannot also start it. Continue with state
verification uses a new attempt and bounded checkpoint, verifies files/external
receipts before acting and preserves result lineage. If effect reconciliation is
incomplete, stay in the unknown row rather than offering misleading continuation.
The visible meaning may be localized; state distinctions must remain intact.

When an obsolete Stop button is pressed, acknowledge promptly with the durable
current status (for example already completed or execution status being checked),
then enqueue repair of that exact control message. Do not wait for a successful
Telegram edit before answering the callback; do not route it to a newer attempt.
If durable state cannot be read, report status temporarily unavailable and perform
no destructive action. Callback acknowledgement is best effort during Telegram
outage and is not proof that the message keyboard has been repaired.

C27 recovery distinguishes queued/not-dispatched from started/unknown; only the
former can resume without started-work reconciliation. C28 Start versus recovery
and Continue versus duplicate callback admit one attempt with preserved lineage.
C29 stale Stop returns current status even when edit repair fails, and cannot
cancel a newer attempt. These supplement C18-C26, not evidence of implemented UI.
