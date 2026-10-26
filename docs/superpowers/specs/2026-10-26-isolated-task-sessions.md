# Task-scoped native sessions and working sets

Date: 2026-10-26. CP-004. Status: **proposed design**, not implemented or enabled.
Planning wave: 1; size estimate: S–M.

[Proposal](../../proposals/CP-004-isolated-task-sessions.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

Scheduled execution currently risks sharing the native foreground session and polluting
interactive context.

## Dependencies and boundaries

Existing queue/admission; CP-018 resolution contract; CP-020 before group enablement.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Separate immutable task identity from Telegram thread identity: task:<id>/run:<id> is not a
  guessed thread integer. Each occurrence defaults to a fresh native session with bounded
  task-specific input and separately stored execution metadata.
- Resolve source authority/destination at occurrence admission; resolve provider/model at actual
  execution admission under the chat slot, preserve that attempt binding,
  and never change provider midway because a foreground setting changed. Missing
  grants/provider availability follow existing failure or confirmation policies; there is no
  owner/personal fallback.
- Task execution inherits the source conversation's authority, not the authority of the private
  chat that approved a login. Scope output to the original conversation/thread and CP-002
  visibility.
- Allow explicit session:main only when requested and safe under the foreground lock. Tasks
  needing history may receive an explicit bounded authorized snapshot without sharing the main
  native transcript. Task results remain inspectable; a concise delivered result may enter
  chat history without importing the task tool transcript.
- Keep current concurrency/admission limits; do not require a new worker engine or container per
  occurrence. General role expansion requires a separate approved capability definition.

## Acceptance

- Existing IPC-delivered tasks retain their delivery mode; moving to a fresh
  native session does not enable an extra controller stdout reply.
- Repeated cron runs leave the foreground native transcript free of task prompts/tool traces.
- Two conversations/tasks cannot resume each other's sessions; invalid task/thread aliases fail
  closed.
- Provider changes while queued resolve by a documented admission boundary and never redirect an
  already admitted run.
- Explicit main-session opt-in serializes correctly; restart and retirement preserve output
  destination.

## Migration, retention and rollback

New schedules use the isolated default. Existing tasks receive an operator-visible migration
inventory; do not silently import main transcripts into task namespaces. Retain original history
and run logs.

## Risks and decisions before implementation

History-dependent tasks need explicit input selection; session isolation alone does not isolate
filesystem or credentials.
Defaults/limits above are proposed product policy, not existing configuration.
The implementation contract below and linked executable plan define the initial schema,
APIs, defaults and slices. Deployment, native-account/resource acceptance and unresolved
privacy/effect evidence remain rollout gates; unknown execution fails closed.

## Out of scope

A general worker pool, task-specific long-term memory or forced provider switching.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/agent.js; src/service.js; docs/conversations.md`. Source/tests indicate implementation intent and mechanics;
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

### Identity and compatibility fields

Add `scheduled_tasks.execution_version` (`0` legacy, `1` managed),
`definition_revision` (starts at 1), `session_mode` (`legacy_main`, `isolated`),
`delivery_mode` (`legacy_ipc`, `controller_result`) and stored ScopeRef columns.
Existing `chat_folder`/`chat_jid` remain for compatibility and diagnostics; they
are not reusable authority. Every definition update increments the revision.
Existing rows keep `execution_version=0`, `session_mode=legacy_main`,
`delivery_mode=legacy_ipc`, thread 0 until an approved migration selects them.
New managed agent jobs default to `isolated` and `legacy_ipc`; controller-result
delivery is an explicit request, never inferred from stdout. Literal reminders
use their separate CP-022 kind and do not allocate a native session.

One `task_runs.run_id` UUID names an occurrence; one `task_attempts.attempt_id`
UUID names an execution attempt. Logical context key is
`task:<task_id>/run:<run_id>/attempt:<attempt_id>`, never a negative/large Telegram
thread integer. Add `TaskRunID`, `TaskAttemptID` and `SessionKind` to the durable
queue/store adapters rather than overloading `ThreadID` or `SourceID` parsing.
The source conversation/thread remains the output/context-read destination.
New Telegram-created definitions derive that thread from authenticated Caller.
Legacy folder-only IPC remains thread 0; a new versioned IPC request may target a
nonzero thread only with a host-minted source-conversation capability. A JSON
`threadId` alone is never permission to select another conversation.

### Native execution and provider snapshot

Provide `Runner.RunTaskStream(ctx, TaskRunRequest)` and provider
`RunTaskCmd(prompt, systemPrompt)` builders. Reuse existing global/per-chat agent
admission and streaming machinery, but do not route task sessions through
`resolveSession`, `adoptLegacySession`, `handoffPrompt`, conversation completion,
auto-compact or automatic memory extraction. This is a scheduled-task execution
path, not the current `RunFresh` maintenance API: that path has a one-minute
budget and maintenance cancellation semantics inappropriate for ordinary jobs.

The initial isolated strategy is **ephemeral per attempt**, using the pinned
providers' existing no-persistence mechanics (`--no-session-persistence` and
`--ephemeral`) with explicit task policy and model arguments. It must never use
`--continue`, `resume --last`, a conversation native session or an implicit legacy
adoption. Store an announced native UUID only as diagnostics in the attempt;
it is not resumability authority. Ephemeral execution also prevents the first
foreground legacy-adoption pass from adopting a task's newest transcript.
A requested continuation is a new explicitly authorized attempt with bounded
checkpoint input, not a hidden native-session resume. No automatic continuation
command is part of this first slice.

Freeze ScopeRef, task definition revision, delivery owner and intended provider
resolution policy when admitting the occurrence. Once its chat slot is held,
resolve the effective provider/model for that stored conversation using CP-018's
resolver contract (current chat-default adapter is sufficient before CP-018 UI
ships). Persist the concrete provider, model and effective profile fingerprint
before any native exec request. Started attempts never read a later mutable
chat provider again. A pre-start queued occurrence may use a subsequently changed
conversation setting; a confirmed fallback would be a new attempt, not mutation
of the original. No paid API/account fallback is added.

Scheduled work remains no-more-privileged than the current registered tier by
default: a private owner source does not silently grant `TierOwner` project/raw-DB
mounts. Task role, source profile and mount tier are distinct. Group managed work
is denied until CP-020 qualification; isolated native sessions alone do not isolate
shared files, account credentials or arbitrary native code.

### Queue and result boundary

Managed task runs are singleton batches. Update `drainPending` batching identity:
interactive items may coalesce only with compatible interactive conversation
items; different runs, task plus Telegram input, or task plus another task never
share a handler invocation. Keep per-chat order and existing global concurrency;
this is not a parallel worker-pool change. A task's execution timeout defaults to
5 minutes, with explicit operator-approved bounds up to 60 minutes; maintenance
limits remain unchanged. Recheck role/scope, expiry and cancellation at dispatch.

Default working set is the task prompt plus trusted run instructions and bounded
checkpoint data, with no implicit foreground tail or compiled personal memory.
An explicit history-read grant may provide an authorized snapshot (default cap
8 KiB) without sharing the main native transcript. Existing service memory tokens
must carry the task role/run binding where applicable; a folder/thread token is
not evidence of permission for task-specific writes. OS access remains a separate
CP-020 boundary.

Freeze output into CP-002's immutable `turn_results.result_id` before releasing
execution. This is private preparation, not Telegram delivery. Do **not** call
foreground `CompleteConversation`, mutate its session mapping, append the task
prompt/tool transcript or set `turns.reply_msg_id` merely because task stdout
exists. Only a controller-result run may publish through its configured outbox
path. A legacy-IPC run keeps its explicit IPC delivery; there is no stdout
fallback or second final send. A confirmed delivered bot result may enter the
source chat's visible history once; that is distinct from importing native task
execution context.

### Migration and concrete acceptance

Generate a source/definition/session/delivery preview before selecting old tasks
for managed execution. Backfill ScopeRef only from an unambiguous current host
registration; missing/reused/retired bindings are held for reconciliation. Do not
rewrite the owner's live digest prompt or create a replacement schedule. Preserve
legacy foreground/native session rows and adoption markers. CP-021's started-run
recovery safety gate must accompany activation; do not switch sessions while
leaving queue retries/recovery able to blindly rerun started managed jobs.

Fixtures must cover: cron before first foreground turn; a failed/expired native
session; Claude -> Codex -> Claude across tasks and topics; provider change while
queued versus after attempt snapshot; singleton batching; explicit main opt-in;
legacy IPC plus nonempty stdout; and cancellation before/after native start.
Task prompts must not appear in foreground native calls or auto-memory/compact
hooks. Main and another topic remain intact after isolated task completion.

### Trusted execution authority and fallback

A stored source actor/service principal and host profile/policy fingerprint are required
independently of ScopeRef. Folder `owner` or a reusable personal container must not
promote scheduled work to owner mounts, credentials or tools. Preserve current
`IsOwner=false`; require compatible container authority before execution. Quota/provider
errors do not prove no effects: CP-010 confirmation plus checkpoint/effect reconciliation
precedes any explicit new attempt, never automatic provider replay.
