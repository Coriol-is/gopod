# Schedule overlap, misfire and model-free reminders

Date: 2026-10-26. CP-022. Status: **proposed design**, not implemented or enabled.
Planning wave: 1; size estimate: M.

[Proposal](../../proposals/CP-022-schedule-execution-policies.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

A recurring job that misses several runs or overlaps itself needs an explicit policy; a simple
literal reminder should not consume model quota.

## Dependencies and boundaries

CP-015 existing scheduling; CP-021 outcome authority; CP-002 reliable notices.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Persist an explicit schedule kind: literal reminder versus agent job. A reminder stores
  authorized literal text/destination and publishes through the outbox without invoking a
  provider. It does not promise zero Telegram/network cost, only no model call.
- Use an atomic occurrence key containing task identity, schedule revision and nominal
  occurrence instant. Admission and advancement commit together. This prevents duplicate local
  admission, not exactly-once external effects.
- Overlap policies: skip (proposed new-job default), coalesce (at most one latest queued
  continuation), bounded queue as explicit opt-in. Running/cancel_requested/unknown execution
  still counts as occupied until settlement or documented reconciliation.
- Misfire policies: skip (proposed recurring agent-job default), coalesce latest, bounded catch-
  up with explicit maximum and grace/expiry. A one-time reminder retains at most one pending
  occurrence until its specified expiry. Never infer that all missed old jobs should run after
  restart.
- Preserve explicit CRON_TZ or a trusted configured timezone; ambiguous user intent requires
  clarification rather than guessed UTC/host time. Test the chosen cron library's DST/clock
  behavior and document duplicate-wall-time treatment before release.
- Optional max_runs/expiry/objective stops a workflow truthfully: exhausted is not achieved.
  Goal completion needs exact owner intent or an appropriate host checker, not a worker's
  prose. The existing /tasks panel shows these policies and next run/last skipped reason
  without exposing private payloads.

## Acceptance

- Repeated scheduler ticks/restart admit one occurrence; concurrent controllers must not claim
  unsupported distributed safety.
- Long-running and cancel-requested tasks exercise skip/coalesce/bounded queue without unbounded
  backlog.
- Clock jumps, DST and multiple overdue times exercise each misfire policy and expiry.
- A literal reminder reaches fake Telegram with zero provider invocations; no early main-slot
  acknowledgement counts as completion.

## Migration, retention and rollback

Retain historical behavior for legacy schedules until policy is explicitly selected or approved
in a migration preview. Version changes invalidate stale occurrence plans without resurrecting
sent reminders. Do not recreate the owner's live FreeFeed task while implementing this generic
capability.

## Risks and decisions before implementation

Policy changes can surprise existing schedules; clock/DST interpretation and late reminder
expiry require explicit defaults.
Defaults/limits above are proposed product policy, not existing configuration.
The implementation contract below and linked executable plan define the initial schema,
APIs, defaults and slices. Deployment, native-account/resource acceptance and unresolved
privacy/effect evidence remain rollout gates; unknown execution fails closed.

## Out of scope

Distributed workflow engine, automatic cloud billing or bulk rewriting existing schedules.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/scheduler.js; test/scheduler.test.js; docs/workflow.md`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.

## Implementation contract — task reliability slice

This dated refinement is the concrete initial design, not evidence that code is
implemented. [Executable plan](../plans/2026-10-26-reliability-tasks.md).
It supersedes ambiguous early shorthand above. CP-002 owns the shared additive
`ScopeRef` foundation; this slice does not create a competing namespace registry.
`ScopeRef` carries `namespace_id`, `namespace_epoch`, `registration_revision`,
`chat_jid`, `thread_id`, `visibility` and `profile`, plus diagnostic folder data.
Use host `SnapshotScope`/`ValidateScope` at admission, dispatch and publication.
A shared owner DM or a model-supplied folder is not a replacement for that scope.

### Definition schema and API versioning

Extend `scheduled_tasks` additively with `definition_revision`, `execution_version`,
ScopeRef, `kind` (`agent`, `reminder`), `session_mode`, `delivery_mode`, nullable
`literal_text`, explicit `timezone`, `overlap_policy`, `misfire_policy`,
`misfire_grace_ms`, `queue_limit`, `catch_up_limit`, `dst_fold_policy`, nullable
`expires_at`, `max_runs` and optional descriptive `objective`/`done_condition`.
Do not put literal reminders into the provider prompt field merely to fit the
current `CreateTask` nonempty-prompt check; update typed store validation by kind.

Use strict IPC v2 task payloads for these new fields; preserve v1 parser/semantics
for old clients. The host binds ScopeRef and derives the source thread from the
authenticated caller/run capability, not a free JSON folder/thread choice.
Mutations require `expectedRevision`; stale buttons/IPC updates conflict instead
of overwriting newer policies. Every definition edit bumps the revision under
the existing task mutation lock and in its SQL transaction. Active admitted runs
retain their old revision and destination. Pause/cancel prevents future admission;
it does not claim to stop a running task. CP-021's separate `stop-run` action is
scope/run-bound and exposes requested versus settled cancellation.

New managed defaults are explicit: agent/isolation/legacy_ipc; overlap skip;
misfire skip with 120-second grace; queue and catch-up defaults 1, opt-in upper
bound 10. Reminders use controller_result and no session/provider, with literal
text <=3500 UTF-8 bytes; omitted expiry defaults to nominal due time +24 hours
and is shown at creation. These product limits are not Telegram API maxima.
An NLP create helper may consume a separately disclosed parsing model turn;
typed/manual reminder creation and every reminder dispatch require zero model
calls. No heuristic turns arbitrary task prose into a literal reminder.

### Atomic occurrence admission

Add `task_occurrences` with unique `(task_id, definition_revision, nominal_at)`;
store task ScopeRef, decision (`admitted`, `skipped`, `coalesced`, `expired`),
fixed reason, optional run/outbox FK and creation time. Nominal times are UTC
milliseconds; do not key admission by polling wall clock `now`. Runs/attempts are
CP-021 records. Add an optional wall-slot key for cron fold de-duplication below.

`Store.AdmitTaskOccurrence(ctx, definitionID, expectedRevision, expectedNextRun,
now)` owns one SQLite transaction that reloads the definition, validates active
scope/revision/expiry, inspects live/held runs, records bounded occurrence
choices, creates the run plus durable `turns` row (agent) or outbox intent
(reminder), and advances `next_run`/terminal reason. Keep `LockTasks` outside the
transaction for in-process coordination with commands; SQLite compare-and-swap
and unique keys, not that mutex alone, close crash/concurrent-poll windows.
The first slice still supports one controller process, not a distributed lease.

Expose a queue `SchedulePersisted`-style method that schedules an already committed
turn without inserting again. Never call current `Queue.Enqueue` from inside
this transaction: it writes through the bare Store and schedules before commit.
If process death occurs after commit but before in-memory scheduling, recovery
may schedule the committed **never-started** turn once. Started task recovery is
CP-021's non-replay path. A failed transaction advances nothing; permanent invalid
scope/definition is paused with a bounded operator-visible reason, not silently
set done as though a goal was completed.

For a literal reminder, transactionally bind its preauthorized exact text and
source destination to the CP-002 outbox ID; no task queue/Runner/CLI allocation
occurs. An uncertain outbox outcome stays one occurrence and does not create a
new reminder on the next tick. Late delivery shows the original nominal time and
timezone in a host-generated label rather than pretending the reminder is current.

### Policy semantics and bounds

- **Overlap skip:** record one skipped occurrence if that task has queued,
  running, cancel_requested or execution_unknown work; advance past it.
- **Coalesce:** retain at most one not-started continuation at the newest due
  nominal time; never replace started/frozen work. A replaced queued row is
  explicitly superseded/cancelled, so its durable turn cannot later recover.
- **Bounded queue:** at most `queue_limit` not-started occurrences per task;
  overflow is recorded skipped, not appended indefinitely. Unknown held execution
  cannot be used to start another overlapping native process.
- **Misfire skip:** due slots within grace are ordinary eligible work; older
  recurring agent slots are skipped and the cursor advances to a future slot.
- **Misfire coalesce:** choose only the newest eligible due instant, respecting
  expiry and overlap; no historical flood.
- **Bounded catch-up:** choose oldest eligible due instants up to catch_up_limit
  for one evaluation; record a summarized skipped remainder and jump forward.
  Never scan arbitrarily many missed seconds. Bound enumeration to 100 candidate
  instants per task/tick; persist a gap summary rather than fabricate thousands
  of occurrence rows. A fake clock and deterministic next-time functions are used.

Cron retains validated CRON_TZ/IANA semantics; new managed jobs without an explicit
zone require a trusted configured chat zone or clarification, not host guessing.
Interval schedules are anchored to their prior nominal instant, not delayed
polling now, for managed definitions. A once schedule requires RFC3339 offset.
For new cron jobs: nonexistent spring wall times are skipped; repeated fall wall
slots default to once, keyed by task/revision/local date/time/IANA zone without
UTC offset. A second fold instant records `dst_fold_duplicate`; explicit both
is available and uses distinct UTC instants. Pin the current cron parser and
verify candidate behavior with deterministic timezone fixtures. Moving the clock
back does not rewind a persisted cursor. Legacy rows retain legacy library fold
and interval-from-now semantics until explicit opt-in.

`max_runs` counts admitted logical runs/reminder publications, not skip decisions
or pre-spawn retries; an uncertain send still consumed its occurrence. Exhaustion
or expiry stores a terminal reason, never an achieved goal. An objective is
reference data until a specifically authorized owner/host completion check
closes it; model prose cannot cancel schedules or mark goal success.

### UI, migration and acceptance

Keep the paginated `/tasks` panel. Show kind, inherited/explicit policies,
source conversation, next nominal instant in its zone plus UTC, late/skip reason,
active run and separate goal/delivery facets without leaking payloads to another
scope. Callback state changes bind actor, task ID, expected revision and source
scope. Inspection of a run uses exact task/run identity; stopping a run is a
separate confirmation from pausing/canceling future scheduling.

Migration adds columns with explicit legacy values, preserving existing IDs,
next_run, prompt, timezone expression and IPC delivery. No schedule is recreated.
Opt-in preview shows changed isolation, default skip/grace/fold and reminder
expiry behavior before applying a revision. Legacy started work is reconciled
under CP-021 before recovery activation. Back up before schema changes and gate
rollback while managed/uncertain rows exist; an old binary must not reinterpret
those rows as ordinary tasks and replay them.

Tests cover atomic failure at each old multi-write boundary; concurrent tick versus
pause/update; duplicate nominal slot; queued coalescing/cancel-before-recovery;
long-running and unknown execution occupancy; delayed wake after 10,000 overdue
intervals; spring gap/fall fold in Europe/Belgrade; forward/back clock jumps;
reminder creation/dispatch with provider call counter zero; expired private route;
and legacy digest definition bytes unchanged in migration fixtures.
