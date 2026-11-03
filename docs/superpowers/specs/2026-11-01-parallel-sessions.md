# Parallel private conversations and background sessions

Date: 2026-11-01. CP-037. **Draft design; no implementation or activation authorized.**
Baseline: main `cee5cac`. Owner selected private chats plus background tasks,
per-topic persistent folders plus explicitly attached shared projects, and
explicitly required splitting chats by threads. Architecture below is proposed,
not an accepted replacement for existing ADRs.

## Implementation specifications

The owner requested implementation specs and a second review, still without code.
The detailed contracts A/B/C/D below refine this architecture and take precedence where
its exploratory wording differs (notably ephemeral task contexts and restart).

- [A: session state, clear/new and persistence](2026-11-01-session-state-implementation.md)
- [B: runtime ownership and scheduling](2026-11-01-session-runtime-implementation.md)
- [C: graceful restart and /restart force](2026-11-01-session-restart-implementation.md)
- [D: mixed providers, model changes and failover](2026-11-01-session-routing-implementation.md)
- [Delivery slices](../plans/2026-11-01-parallel-sessions.md)
- [Second review and current restart audit](../../reviews/2026-11-01-parallel-sessions-spec-review.md)

## 1. Scope and recommendation

Introduce an optional single-host session scheduler. A conversation is
`(registered chat folder, Telegram thread_id)`; thread 0 is the main conversation,
never a wildcard. Each conversation has its own ordered queue, native context,
workspace and provider/model preferences. Different topics of the same chat may
run concurrently. More private chats use the same mechanism only if admitted by
existing registration/authorization rules; this design does not broaden access.
Groups retain existing execution and authority policies.

Use a dedicated disposable container for each active execution session, with
sticky reuse while idle and a bounded idle TTL. Persist session assets on the
host independently of container lifetime. Background task runs get independent
execution sessions and an explicit origin/delivery conversation. Scale active
agent processes according to resource and account limits, not topic count.

These are CLI containers calling remote providers, not local model servers.
Several containers may use the same model. Runtime compatibility is determined
by provider, image version, authority and mount profile; model is an attempt
setting. More containers do not create more account quota. Local model serving
and multi-host orchestration are separate future designs.

| Option | Benefit | Cost / reason |
|---|---|---|
| Parallel CLI processes in one chat container | Small lifecycle change | Shared homes, tools and cancellation scope remain hard to isolate; not recommended |
| Permanent container for every topic | Simple affinity | Unbounded idle runtime inventory; topic lifetime should not determine runtime lifetime |
| Dedicated active-session container with idle eviction | Clear process boundary; durable context; bounded inventory | Requires durable session routing and safe native-home migration; recommended |
| Arbitrary request balancing over model containers | Familiar stateless routing | Native sessions/files are stateful; requires qualified remount/recreation, not request-by-request balancing |

## 2. Verified baseline and required changes

- `internal/queue/queue.go`: queue state keyed by folder; ThreadID separates
  batches but not execution lanes. Replace personal execution lanes with
  conversation/session keys; retain chat authorization separately.
- `internal/runner/session.go`: native sessions already keyed by
  folder/thread/provider and resumed by explicit ID. Preserve all mappings.
- `internal/runner/mounts.go`: native Claude/Codex homes and workspace are
  chat-shared. Adding containers without changing writable mounts is insufficient.
- `internal/runner/agentkill.go`: agent slots and cleanup are per chat. A bounded
  kill attempt is not evidence that every tool process has exited.
- `internal/runner/gate.go`: global process limit, interactive-first admission,
  memory threshold; missing telemetry is allowed and low memory on an idle
  system eventually admits anyway. Proposed auto mode must not inherit these
  fail-open cases for additional concurrency.
- D022/D026 define sequential chat execution and admission; D023 defines memory
  visibility. CP-018 designs per-conversation provider settings but remains
  unimplemented. CP-004 task execution and CP-007 full backup/restore have open
  qualification gates. This document does not bypass those gates.

## 3. Identity, ownership and durable state

Keep these identities distinct:

| Entity | Stable key / responsibility |
|---|---|
| Chat | Registered folder; authority root |
| Conversation | Chat + thread, including main 0; queue and delivery scope |
| Execution session | Host-generated ID; conversation or task-run owner; durable files |
| Native context | Execution session + provider + context generation; native CLI ID |
| Attempt | Durable turn/run + attempt ID; frozen provider/model, profile revision |
| Runtime | Disposable container ID + generation; never an authorization input |
| Shared project | Host-approved project ID and explicit access mode |

Store metadata in the existing single SQLite database through `internal/store`.
Conceptual additions: session ownership/state, workspace references, runtime
bindings/generations, admission reservations, project access claims and attempt
snapshots. No separate scheduler database. Host derives paths and IPC/capability
scope from registered ownership, never model-supplied folder or session IDs.

A topic rename leaves its session unchanged. Closed/retired topics stop accepting
new work according to CP-016; late task results must not resurrect a retired
conversation or silently redirect into main. Retain results for owner retrieval.
No cross-topic batching, implicit --last resume, or shared mutable provider field.

## 4. Context and filesystem contract

Illustrative layout, not a committed storage schema:

```text
sessions/<chat>/<session-id>/
  workspace/                   persistent topic or task-run files
  providers/<provider>/        private native context/config, versioned profile
  checkpoints/                 manifests and continuity evidence
projects/<project-id>/          explicitly attached shared project
```

Use stable container paths across resume (for example `/workspace/session`) so
native project/path identity does not change. Persist native transcripts, indexes
and required provider state, not only the SQLite native ID. Container writable
layers are for disposable scratch. Warm containers are bound to one session;
reassignment requires recreation with a validated mount/profile manifest.

Credential provisioning is separate from native context. Do not mount a shared
writable CLI home into concurrent containers or naively copy refresh credentials.
Qualification must establish supported login/refresh behavior, locking and
revocation for each provider. Secrets remain host-provisioned under existing
policy, never committed or placed in scheduler metadata. CP-004's unqualified
task credential adapter remains a hard activation gate.

Persistence promises: stopping an idle container retains committed files and
native context; Stop retains history and already written files. It cannot retain
in-flight tokens, shell processes, open transactions or unflushed native state,
and it cannot undo tool side effects. A missing/corrupt native checkpoint is an
explicit recovery condition; never claim an exact resume from a summary alone.
Bounded transcript handoff can be offered as a visibly degraded recovery.

Existing memory semantics remain D023: topic-local extracted facts plus shared
manual/main memory; main can aggregate topic facts. Folder separation is not a
new confidentiality guarantee between owner topics. Task runs receive an explicit
allowed memory scope and an input snapshot; they never inherit whichever topic
was most recently active. Task extraction must not overwrite a topic summary.

## 5. Shared projects and concurrency

Attach projects explicitly per session. Default attachment is read-only. For
writes, choose either an exclusive project write claim or a separate per-session
Git worktree. Distinct worktrees allow parallel editing; publishing/merging is
serialized and remains governed by existing approval rules. Git metadata and
external build caches also need concurrency-safe access.

The host arbitrates write claims before mounting writable content. All project
claims are acquired atomically in canonical order; a session waiting for a
project must not consume an agent slot. Current broad owner writable mounts
must be inventoried: parallel mode cannot promise collision protection while
an alternate writable alias bypasses the project policy. Migrate those aliases
into explicit project attachments before enabling protected parallel writes.

Locks coordinate gopod writers, not arbitrary host applications. Read-only mounts
prevent writes, but do not provide a stable snapshot of files changed elsewhere;
reproducible tasks use snapshots or pinned Git revisions. Unknown execution keeps
its write claims until death is proven; a lease expiry alone cannot release them.

## 6. Scheduler and bounded autoscaling

Proposed modes: `serial` (default, existing chat serialization), `parallel`
(explicit bounded cap), `auto` (adaptive within an operator cap). These are design
names, not available commands/config. Admission covers all agent starts, including
maintenance and task parsing. Group workloads count toward host capacity even
though their routing does not change.

1. Persist the request and its original conversation/task delivery metadata.
2. Select a runnable session: FIFO within a conversation; fair rotation across
   conversations. Only one attempt may write a session's native state at once.
3. Resolve and freeze provider/model, authority and required project claims.
4. Atomically reserve session, project and host/provider capacity without holding
   scarce agent capacity while blocked on another resource.
5. Reuse that session's compatible warm runtime or create its dedicated runtime.
6. Start the attempt; record runtime generation; deliver events to the original
   conversation. Release claims only after confirmed execution termination.

Interactive work has priority. Prevent indefinite task starvation with bounded
weighted service (initial proposal: offer one eligible background dispatch after
four interactive dispatches). This is a dispatch fairness target, not a completion
SLA. A cap of one cannot guarantee immediate user response during a long task.
With capacity >=2, background work may use at most cap-1 slots while interactive
capacity is reserved. Do not preempt arbitrary task side effects. Maintenance may
cooperatively yield only where its existing contract makes cancellation safe.

Auto admission requires fresh memory/pressure/disk telemetry, enforced container
limits, and a qualified per-profile resource envelope including child tools.
Reserve a measured host/controller margin and budget for admitted-but-not-started
processes. Do not double-count resident memory already reflected in MemAvailable:
subtract startup reservations and remaining growth headroom of active envelopes.
CPU and sustained swap/IO pressure can veto scale-up; swap is not extra RAM.
Account concurrency/rate limits are independent bounds, with shared cooldown on
provider rejection and no automatic paid/provider fallback.

Grow one slot at a time while there is eligible demand and sustained headroom;
shrink the target under pressure, then drain running attempts. Idle TTL eviction
removes runtime containers only, never persistent session files. Use separate
limits for running agents, warm runtimes, queued requests and retained disk data.
Hysteresis/cooldowns and numeric budgets must be calibrated on the actual host;
no production values or reboot are authorized here. Historical Pi cgroup evidence
is not a current probe. Auto mode above serial capacity stays unavailable until
resource enforcement and failure behavior are qualified.

## 7. Background tasks and controls

A background run owns a session separate from its originating conversation.
Persist origin `(chat, thread)`, destination, input snapshot, project attachments,
provider/model policy and result reference. Fresh context per run is the default;
continuing a recurring-task context requires an explicit policy, versioned
checkpoint and no overlapping writes. Task output reaches the origin thread via
the existing durable delivery design; it does not inject into an unrelated active
native conversation. If context transfer is needed, enqueue a result reference
for that conversation's next turn.

Legacy scheduled tasks currently using main remain on the legacy lane until
CP-004 migration/credential qualification is complete. Do not silently classify
them as isolated because they run on a timer. Misfire, overlap and retry policy
must follow CP-022 rather than launching unlimited catch-up containers.

Per-topic provider/model UI follows CP-018; chat defaults remain explicit. Snapshot
settings at attempt start, and serialize changes/clear/compaction with that
session. A chat-default change affects future inherited attempts, not active ones.
Stop button and `/stop` affect only the active interactive attempt of the current
conversation. New messages queue, never implicitly interrupt. Background task
cancellation uses its own task/run identity; stopping a topic does not kill a task
or sibling topic. Stale buttons cannot cancel a newer generation.

Proposed control UI through `internal/control`: a mode picker, bounded concurrency
setting, and status showing active/queued sessions, resource/project/quota wait
reasons and warm runtimes. Keep existing Router authorization. Disabling parallel
mode prevents new concurrent starts and drains existing work; no mass termination.

## 8. Stop, failure and recovery

Runtime lifecycle: cold -> starting -> running -> idle -> draining -> cold.
Attempt terminal states are separate: succeeded, failed, stopped, or uncertain.
On cancellation, terminate the agent process tree and, if required, its dedicated
container. Confirm Docker/runtime death before releasing session/project claims.
If the daemon is unreachable, mark uncertain, retain claims and block conflicting
work. Generation fencing rejects stale IPC/results but cannot stop an old process
writing a bind mount; actual termination or storage isolation is still required.

After controller restart, reconcile recorded attempts against labeled runtimes
before admitting replacements. No automatic replay of uncertain side-effecting
tools. OOM/resource failures are recorded distinctly from provider quota failures.
Native context validation and delivery deduplication precede continuation; no
exactly-once external side-effect promise is made.

## 9. Migration and backup

Keep serial default and preserve existing IDs/history. Quiesce a chat before
splitting native homes; save a matched backup, inventory all provider files and
qualify extraction into separate per-topic homes with stable paths. Do not infer
that copying a transcript alone migrates a provider. Keep original homes and a
mapping manifest for rollback. Rollback after new writes requires reconciliation,
not pointing old code at stale homes. An unsupported provider stays serial.

CP-007 currently does not establish full native-home/workspace coverage. Parallel
activation requires a matched snapshot of SQLite mappings, session homes, topic
files and project revisions, plus a restore drill. Credentials need their existing
protected recovery policy. Backup/retention must distinguish container eviction,
context reset and explicit data deletion. No automatic deletion of dormant topics.

## 10. Future delivery gates — not a development plan approval

1. Agree the architecture and append superseding ADRs for D022/D026 where needed;
   reconcile CP-018 and CP-004 contracts without changing their current status.
2. Qualify provider-home migration, credential concurrency and resource limits in
   disposable environments with explicit scope for live provider probes.
3. Implement durable identities and serial-compatible routing; then per-session
   containers and a fixed cap; only then adaptive capacity and idle eviction.
4. Before activation, test two topics in one chat on the same model, two different
   providers, topic 0, independent task results and exact Stop scope. Test shared
   project contention, stale callbacks, allocation failure, cancellation during
   startup, daemon outage, detached child tools, OOM, restart reconciliation,
   quota cooldown, starvation and disable/drain. Assert no duplicate writers.
5. Demonstrate native resume plus a file checksum after runtime eviction and
   restart for each supported provider; demonstrate matched restore and rollback.
   Real Telegram and native CLI acceptance are separate from Go/race tests.

Open design choices: idle TTL and measured capacity profiles; exact task context
continuation policy; provider-specific credential distribution; allowed writable
project aliases. Recommended defaults are fresh task context, RO projects with
explicit write/worktree attachment, serial until qualified, and no live migration.

## 11. Design review: missing contracts and proposed resolutions

Review date: 2026-11-01. The following are proposed refinements, not approved
implementation. They include the subsequent discussion of `/clear` and `/new`.

### Conversation versus session generations

A conversation needs a durable active-session pointer; a session needs a context
generation. Topic lifetime, workspace lifetime and native context lifetime are
not identical. `/clear` advances context generation in the same session, retaining
files/settings and long-term memory, and invalidates all providers' old contexts,
neutral handoff and conversation summary. `/new` archives the old session and
atomically selects a fresh session with a new workspace in the same Telegram
topic. Project attachments/settings are inherited, but write claims are reacquired.
Neither command deletes Telegram history or long-term memory. Topic storage is
a stable root containing session workspaces, not one mutable directory reused by
all generations. Archive access/resume UI remains to be designed; no deletion is
implied by archiving.

An archive is sealed only after all writers terminate. Background tasks use their
own directories or snapshots, not a live writable reference into an archived
conversation workspace. Long-term memory remains conversation-scoped under D023:
`/new` is not a blank-memory or confidentiality boundary. A future isolated-memory
mode would require a separate explicit contract.

### Reset barrier and ingress ordering

Serialize `/clear`, `/new`, `/compact`, retirement and context-changing controls
against attempt admission, with a durable operation ID and compare-and-swap on
active session/context generation. Confirmation binds actor, conversation,
expected generation and the pending-message cutoff; stale confirmations fail.
On confirmation, freeze old-session admission, stop/settle its active attempt,
and commit the transition only after termination is confirmed. Old queued input
is explicitly cancelled with notice, never silently carried into the new context.
Messages arriving after the confirmed barrier wait in a transition inbox; after
commit they bind to the new generation. If the transition fails, retain them and
report the failure instead of silently running them in a different context.

SQLite and filesystem operations cannot form one transaction. Prepare workspace
and migration manifests first, commit the pointer/state transaction, then finalize
idempotently. Recovery reconciles prepared/committed operations. Do not announce
success before the durable pointer commit. Telegram update deduplication and queue
records must include the resolved session/generation; retries preserve them.

### Late results, capabilities and publication

Carry conversation, session, context generation and attempt identity through
stream events, IPC, memory extraction, summaries, callbacks and outbox records.
Fence stale writes after clear/new; delivery to a Telegram topic alone does not
prove a result belongs to its active session. Old task results may be delivered
with an archived-session label but never automatically enter new context. Handle
already-queued outbox items explicitly: preserve terminal status/results with
provenance, cancel obsolete transient drafts, and keep uncertain sends for
settlement. Reset cannot recall an already sent external action.

External mutations need more than filesystem project locks. Two sessions can
send duplicate email, edit the same calendar event or publish the same change.
Define connector-specific operation IDs, conflict keys and retry rules. Bind
existing approval receipts to exact action, attempt and session generation; no
cross-session reuse. This extends existing side-effect policies rather than
adding blanket approval to every private-chat action. Execution claims and any
external side-effect settlement claims have distinct release conditions.

### Environment continuity

Persisting files does not preserve packages installed in the container layer,
background servers, shell environment, MCP processes or browser state. Classify
state as durable workspace, reproducible environment manifest, or transient
process state. Pin image/CLI/skill/plugin revisions per attempt; rebuild from a
manifest when possible. Never promise that an idle-evicted shell resumes alive.
Qualification must cover native format upgrades and rollback compatibility.
Session-installed dependencies need a persistent environment or reproducible
recipe; running ad-hoc tools do not exempt a runtime from resource accounting.

### Scheduling, pressure and control availability

A strict reserved interactive slot conflicts with maximum background throughput.
For the first version prefer explicit reservation and expose the idle capacity;
borrowing/preemption is deferred. Fair dispatch cannot bound waiting time behind
unbounded jobs. Define background wall-time/heartbeat policies and surface stuck
attempts without treating silence as proof that an external action failed.

All spawn paths must consume the same host budget, including CLI-spawned agent
children and tool subprocesses through container resource limits. Bound pending
input, task fan-out, artifacts, logs and archived data; report overload without
silently dropping accepted requests. Define admission behavior at disk exhaustion
and retain enough space for terminal state/diagnostics. Owner cleanup is separate
from runtime eviction. Controller/Stop/reconciliation need reserved resources and
must remain outside the worker admission queue. Detect duplicate controller
ownership before any scheduling; recovery must not run two schedulers concurrently.

Before choosing adaptive thresholds, record queue time by reason, runtime startup
latency, peak RSS/process-tree usage, OOMs, provider throttling, stuck claims and
session resume failures. Status must distinguish running, awaiting permission,
waiting for a project, waiting for resources and uncertain execution. No payloads
or credentials in telemetry. Fixed-cap execution should qualify before auto mode.

### Additional acceptance cases

Race clear/new with message ingress, Stop, provider change, extraction, task
completion and delayed outbox delivery. Crash between workspace preparation and
pointer commit; retry the same confirmation; send a stale approval; exhaust disk
while recording a terminal result. Verify archived workspaces stay immutable,
new context does not receive old summaries, old results remain retrievable, and
an evicted environment is either reproducible or honestly reported as changed.
Exercise concurrent external mutations with connector-specific conflict rules,
and prove Stop remains available when every worker slot is occupied.

## Sources

Repository evidence above is the baseline authority. Docker documents that
persistent mounts outlive runtime containers, and that resource limits require
explicit configuration and host support. These facts support the storage/admission
boundaries; they do not qualify gopod's native CLI or credential behavior.

- [Docker storage](https://docs.docker.com/engine/storage/)
- [Docker resource constraints](https://docs.docker.com/engine/containers/resource_constraints/)
- [CP-018](../../proposals/CP-018-per-thread-providers.md)
- [Decisions D022, D023, D026](../../DECISIONS.md)
