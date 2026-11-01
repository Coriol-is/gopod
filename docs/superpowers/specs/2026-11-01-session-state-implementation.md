# CP-037 implementation contract A: sessions, controls and persistence

Status: implementation specification for review, 2026-11-01. Writing specs is
authorized; implementation/activation is not. Baseline `cee5cac`.
[Architecture](2026-11-01-parallel-sessions.md) ·
[Runtime contract](2026-11-01-session-runtime-implementation.md) ·
[Restart contract](2026-11-01-session-restart-implementation.md).
Normative MUST/MUST NOT requirements below govern the proposed implementation.

## A1. Identity and additive storage

Use existing ScopeRef for namespace/epoch/registration authority; folder/thread
alone is routing, never sufficient permission. Add store-owned records:

| Record | Required fields and constraints |
|---|---|
| Conversation binding | namespace_id, epoch, thread_id, active_session_id, revision, lifecycle; unique namespace/epoch/thread |
| Execution session | UUID, source scope, kind conversation/task_attempt, context_epoch >=1, workspace_id, lifecycle, timestamps |
| Native context | session_id, context_epoch, provider, native_id, format/profile revision; unique session/epoch/provider |
| Session operation | UUID, kind clear/new/compact, expected binding revision/epoch, cutoff sequence, prepared workspace, phase, result |
| Attempt binding | existing turn/task_attempt identity, session_id, context_epoch, provider/model/profile snapshot, runtime generation |
| Attachment | session_id, approved project ID, access mode, revision/snapshot reference |

Reuse task_runs/task_attempts, existing turns and outbox; do not create competing
execution ledgers. Every foreign relationship validates owner scope. Use existing
store migration conventions and constraints; no destructive migration or cascade
of evidence. Old `sessions` mappings remain authoritative in legacy mode; a
migration marker atomically selects the new mapping adapter, never two writers.
Maintain a unique active execution claim per session and exclusive write claims
per project. `execution_unknown` is a held claim, not terminal availability.

Conceptual consumer APIs: ResolveConversation, PrepareSessionOperation,
CommitSessionOperation(expectedRevision), BindAttempt, AcquireClaims,
SettleAttempt, ReconcileSessionOperation. Exact Go names may follow local style;
transaction boundaries and errors are mandatory. Expose stale/conflict/blocked/
retired/not-qualified outcomes separately, never silently choose another session.

## A2. Ordering and reset transaction

Persist input with monotonic per-conversation ingress sequence and dedup key.
Only compatible interactive items of the same session/epoch may coalesce; task
attempts are singleton batches. Durable attempt bindings outlive retries.

`/clear`: invalidate all native provider contexts, neutral handoff and summary;
advance context epoch, preserve files, settings and D023 long-term memory.
`/new`: archive old session, allocate a fresh workspace/session and atomically
switch the conversation pointer; inherit settings/attachment definitions, reacquire
write claims. Telegram topic remains unchanged. Neither is data deletion or
blank-memory mode. `/compact` builds a summary from a captured generation and
commits only if that generation is still current; failure preserves old context.

Confirmation binds actor, ScopeRef, operation ID, expected binding revision and
context epoch. Confirmation commit chooses an ingress cutoff under the same
conversation transaction; the prompt explicitly says all still-pending old input
will be cancelled. Input after that cutoff enters a durable transition inbox.
Freeze admission, settle/explicitly stop the active attempt, prepare filesystem
manifest, then commit the new pointer/epoch and cancelled old queue states.
After commit bind transition input to the new generation. On failed transition,
retain new input and expose retry/cancel; do not run it in an implicit context.

Filesystem preparation is idempotent and precedes DB pointer commit; finalize
and mark old state archived afterwards. Startup reconciles prepared/committed
operations. Orphan preparation is quarantined, not deleted automatically. Unknown
process exit blocks transition. Duplicate confirmations return the saved result;
stale confirmations cannot reset newer work. Other topics continue unaffected.

## A3. Late events and task integration

Attach session/epoch/attempt/runtime generation to stream events, callbacks,
capabilities, extraction, summaries and outbox provenance. Validate at mutation
and publication. Late context writes are discarded with diagnostic evidence;
old task results remain retrievable and may be delivered as old-session results,
never silently compiled into current context. Already sent messages are not undone.

CP-004 remains authoritative: isolated tasks use ephemeral native context per
attempt; persistent working files/checkpoints/results are separate. Do not add
native recurring-task resume in this slice. Keep legacy tasks on the chat-serial
compatibility lane and preserve existing delivery_mode; stdout never enables a
second reply. Literal reminders allocate no container. Task source and result
destination are immutable authenticated scope, including thread. Explicit
history snapshots require the existing grant and bound. Task mounts/credentials
remain restricted by its execution role, not owner-topic authority.

Old task attempts have their own writable workspaces; no live RW mount into an
archived conversation. Recurring definitions survive new/clear and cancellation
of an occurrence. New occurrences resolve authorized inputs/settings at their
specified boundary; they cannot implicitly resume an archived foreground session.

## A4. Compatibility, migration and acceptance

Retain serial default; source registration and group behavior do not broaden.
Quiesce each chat for provider-home extraction. Preserve native IDs, old homes,
legacy adoption markers and a matched backup. Qualify complete native-file format
migration per provider and pinned CLI version, with stable internal workspace
paths. Unsupported migration leaves the chat on the legacy adapter. CP-007 full
workspace/native-home backup and restore drill is required before activation.
Rollback after new writes needs explicit reconciliation; never point old code at
stale mappings. No task definition execution_version rewrites in this slice.

A-tests: A01 unique ownership/epoch constraints; A02 two-topic FIFO/no cross-batch;
A03 clear all providers with files/memory retained; A04 new archives/fresh workspace;
A05 clear/new/compact races; A06 crash at every filesystem/DB transition boundary;
A07 duplicate/stale confirmation; A08 late extraction/IPC/outbox; A09 task result
after new and retired topic; A10 legacy migration/resume and restore; A11 scope
revocation/re-registration; A12 task stdout/delivery-mode preservation. Use
barriers and fault injection, not sleeps. Real native resume is a separate gate.

## A5. Archive access, retention and explicit deletion

Refinement requested by owner on 2026-11-01. `/new` archives a session without
silently deleting it. Provide owner-authorized archive listing and detail with
source topic, creation/archive times, storage usage and running/uncertain task
references. Listing is scoped by authenticated conversation/owner authority;
archive IDs alone grant no access. Viewing/exporting must not reactivate a session
or resume its native process. Resuming an archive remains a separate future action.

Export explicitly selected workspace files and conversation data through existing
artifact/delivery authorization. Exclude credentials, native authentication homes,
capabilities, raw database contents and attached shared projects by default.
Native context backups follow the protected backup contract, not ordinary export.

Deletion requires confirmation bound to exact archive ID, revision and displayed
scope/size. Refuse active sessions and archives with running/unknown execution,
write claims or unresolved artifact/result references. Cancelled external actions
and their evidence must remain independently addressable. Delete only owned session
assets, never shared projects, chat memory, schedules or other sessions. Preserve
minimal audit tombstone and explicitly state that Telegram copies and retained
backups are not erased by local archive deletion.

Use a durable deletion manifest and resumable phases: tombstone/deny new access,
quarantine owned paths, reconcile references, then remove eligible assets. Never
leave an active pointer targeting removed content. Path validation must prevent
symlink escapes. Partial failure is visible and retryable; do not report success
until scoped deletion is verified. Coordinate retention with CP-007 backups and
CP-003 artifact leases; do not introduce a competing purge mechanism.

Default retention is no automatic archive deletion. Show usage and configured
storage limits; reaching the limit rejects a new allocation before resetting the
current session. Archive listing/export/deletion is a bounded follow-up slice;
its detailed command/API design must precede implementation. Until then `/new`
activation requires an operator-visible storage inventory and documented safe
cleanup procedure, not an invisible unbounded archive store.

Additional acceptance: A13 archive access/exports do not leak credentials or
shared-project data; A14 delete versus task/result/backup lease races; A15 crash
at each purge phase resumes safely; A16 full disk rejects `/new` without changing
the active session; A17 retained backups/Telegram copies are described accurately.

## A6. Routing operations and event progress

Extend session-operation kinds with selection/fallback and revision-bound state
from [contract D](2026-11-01-session-routing-implementation.md). Store opaque
credential-budget identity, pending/effective selection, handoff envelope IDs and
per-native-context consumed event sequence. Keep credentials outside metadata.
Failed-turn continuation precedes queued successor messages; reset and retirement
invalidate old operations. Conversation epoch fences do not replace registration
ScopeRef checks. SQLite commits and Docker dispatch require intent/reconciliation,
not claims of cross-system atomicity.

## A7. Durable Telegram control surfaces

[C8](2026-11-01-session-restart-implementation.md) requires per-attempt message
bindings, desired/acknowledged UI revisions and repair intent. Extend existing
turn/outbox storage rather than using the volatile inflight registry as truth.
Session reset invalidates old control actions; execution status and Telegram edit
status remain independent. Persist only bounded display-safe partial checkpoints.
