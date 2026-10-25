# Sandbox runtime extensibility

**Date:** 2026-10-25  
**Status:** Design saved; implementation deferred  
**Proposal:** CP-011  
**Related:** D013, D022, D026, D030, proposed D032

## Goal and scope
Separate **which agent runs** (`AgentProvider`) from **where it runs**
(`SandboxRuntime`). Start with a behaviour-preserving Docker adapter. This
spec authorizes no new backend, service account, infrastructure, credentials,
host configuration change or production runtime migration.

## Verified baseline
- `Runner` owns `*Docker`; `New` requires it (`internal/runner/runner.go`).
- `agentExecer` exposes Exec/ExecStream only; it is a test seam, not a complete
  runtime contract (`internal/runner/agentkill.go`).
- Image/host args, mounts, interactive login, version probing, per-chat idle
  cleanup, process-group cancellation and recovery are tied to Docker.
- Provider changes wait for the agent slot and lifecycle lock, replace the
  container, preserve session mappings and pass bounded per-thread history.
- `GOPOD_NO_CONTAINER=1` disables the runner in `cmd/gopod/main.go`; a working
  subprocess runtime has not been implemented.

## Boundaries
A provider builds/parses agent CLI operations and declares its capabilities.
A runtime allocates an isolated execution domain, places approved workspace
and session storage there, executes operations, and controls their lifetime.
Telegram, Router and queue never branch on a backend name.

The execution domain remains **per chat** initially; conversations and sessions
remain **per `(chat folder, thread)`**. Runtime choice is per chat, with one
configured default. Per-thread sandboxes require a separate design because
workspace concurrency, RAM admission and provider assumptions change.

## Proposed contract
The following is a design outline, not a current package API:

```go
type SandboxRuntime interface {
    Name() string
    Capabilities() RuntimeCapabilities
    Ensure(context.Context, SandboxSpec) (Sandbox, error)
    List(context.Context, Namespace) ([]SandboxInfo, error)
    Close() error
}

type Sandbox interface {
    Identity() SandboxIdentity
    Exec(context.Context, ExecSpec) (ExecResult, error)
    ExecStream(context.Context, ExecSpec) (StreamHandle, error)
    ExecInteractive(context.Context, ExecSpec) (InteractiveHandle, error)
    CancelRun(context.Context, RunID) error
    Stop(context.Context, time.Duration) error
    Remove(context.Context) error
}
```

Concrete types must keep validated chat identity, durable execution-domain
identity, ephemeral allocation/container identity, run identity, process env, working directory, stream ownership and exit status
explicit. argv is structured; user/model content must never form a shell
command or choose a mount, credential capability or execution namespace.
A provider may require capabilities such as interactive login or process-tree
cancellation. Absence is an actionable configuration error, not a silent downgrade.

## Storage and isolation contract
- Host workspace paths and backend execution paths are distinct fields. Do not
  pass a host absolute path to a remote runtime and assume the same filesystem.
- Docker preserves today's shared chat workspace, per-provider session mounts,
  read-only skills and D013 mount allowlist. No extra host access is introduced.
- Additional adapters must define workspace transport, durability, size limits,
  file ownership and cleanup explicitly before they are enabled.
- Native session handles include a **durable execution-domain id**: the
  registered runtime backend plus a trusted persistent chat/session storage
  namespace and relevant storage/config identity. It is provisioned/persisted
  by the host, never supplied by model output. It is not a Docker container id.
  The domain stays stable across idle kill/recreate, host-process restart and
  provider roundtrip when the same persistent session storage remains attached.
- Ephemeral allocation/container ids and run ids are separate lifecycle handles,
  never part of a durable session lookup key. Docker container C1 → C2 with the
  same domain/storage may resume the same session. Moving to another runtime,
  storage namespace or incompatible configuration creates a different domain;
  native resume is refused until a separately verified migration is accepted.
  Matching provider names alone never establishes domain compatibility.
- Runtime migration is explicit. Bounded conversation history may seed a fresh
  native session; it does not migrate native tool/hidden state or imply that
  files and credentials were copied. User-visible continuity claims must say so.
- Authorization derives from the validated registered chat namespace. Data from
  the model, IPC payloads or callback arguments cannot override it.

## Lifecycle, cancellation and resources
The runner retains queue slot → per-chat agent slot → lifecycle lock → global
agent admission ordering as applicable. Do not hold the lifecycle lock across
an agent run or acquire a chat slot while holding a global gate slot.
Ensure/login/probing and provider replacement must not race sandbox creation.

Stop targets exactly one run and completes its declared cancellation operation
before releasing the agent slot. Declare process-group versus full process-tree
semantics explicitly. The current Docker process-group kill has a known escape
gap for detached tools (D021/HANDOFF); extracting an adapter must not advertise
a stronger guarantee. Closing that gap is a separate tested change. Stream EOF,
cancellation, process exit and kill completion have
explicit owners; all error paths release slots and capabilities once. A backend without bounded run cancellation cannot be selected for a stoppable
chat turn; a provider requiring full-tree termination cannot use a runtime that
only declares process-group cancellation.

Declare whether memory/CPU/pid limits are actually enforced. Docker configuration
alone is not proof: the Pi's disabled memory cgroup is an existing operational
constraint. Preserve D026 global admission and responsive-host priorities.
Idle cleanup and startup recovery filter by application namespace and runtime
identity; they must never remove another application's containers/resources.

## Credentials and connectivity
Keep secret sources in environment/protected local configuration, never provider
metadata, chat JSON or committed files. Maintain per-run memory capabilities,
revocation and chat/thread scoping. An additional backend must demonstrate how
it reaches the memory API/IPC without exposing them publicly or inheriting host
Docker access. Reject unsupported networking/mount combinations explicitly.
Interactive auth must name the selected provider and runtime; auth migration,
credential export and new paid services require separately authorized work.

## Configuration and rollout
Initially the only accepted runtime is Docker; old configuration stays valid.
Unknown backends or unsupported capabilities fail startup/selection clearly.
`GOPOD_NO_CONTAINER` keeps its current disabled-runner meaning until a separately
approved change; never reinterpret it as unisolated production execution.

1. Extract a Docker adapter with no UX/config changes and pass contract tests.
2. Route runner lifecycle/exec/auth through it; remove concrete Docker assumptions
   only after parity, cancellation and recovery are proven.
3. Introduce runtime descriptors/capability validation and identity persistence.
4. Select one additional backend in a separate proposal, with its own threat
   model, operational costs, rollback and live acceptance. No vendor chosen here.

## Acceptance
Contract tests cover isolation, allowlist rejection, workspace/session durability,
exec argv/env, concurrent stdout/stderr, interactive login, exit classification,
Stop while waiting/running, child-process cleanup, idle kill, startup leftovers,
crash recovery, scoped memories and failed create/stop/remove operations.
Integration acceptance must run on each actual backend; fake tests and successful
image builds do not establish backend isolation or real resource enforcement.
Docker-only deployment must remain compatible throughout the refactor.
