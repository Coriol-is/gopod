# Elevated host session — architecture and implementation contract

**Proposal:** [CP-038](../../proposals/CP-038-elevated-host-session.md) ·
**Date:** 2026-11-01 · **Status:** design, no implementation authorised yet

An implementer's review of the first draft of this document found that several
of its claims about the existing code were wrong and that three of its
contracts could not be implemented as written. This revision is the corrected
version; §13 keeps the record of what changed and why, so the same mistakes are
not re-derived later.

## 1. Decisions the owner took

| Question | Answer |
|---|---|
| How the container gets host authority | `Privileged: true` plus host PID, network and IPC namespaces |
| How a session starts | Its own command and its own session, not a mode of the current topic |
| What bounds the lifetime | Explicit close only, plus controller exit |
| Required guarantees | Confirmation on start, a store journal of every exec, a visible chat marker, a separate `exec_guard` profile |
| Routing in a private chat | Modal: while a session is live every owner-private message goes to it, and the ordinary topic resumes on close |
| The missing `MessageID` on button presses | Populate it in `callbackCaller`, which also repairs `/restart`'s buttons |
| Journal granularity | Only what the host can actually attest: one row per `docker exec`, with no claim to record the agent's own host commands |

Two tensions are recorded rather than resolved. The original request said the
lifetime is limited and the chosen answer removes every timer, so the bound is
the controller process, not the clock; §6 keeps teardown in one function so a
TTL is a later caller rather than a redesign. And the owner asked for a journal
of every exec, which §7 can only honour at the granularity the host can see.

## 2. The authority this creates

With `Privileged: true`, host PID/network/IPC namespaces, the Docker socket and
the host root filesystem bound in, the container is equivalent to root on the
Pi. It can read and write any host file, including gopod's own SQLite store,
its backups and the provider credential directories; it can see and signal host
processes; it can drive the Docker daemon, so it can create, inspect, modify and
remove containers, the hardened chat containers included; and it carries the
host's network identity.

The consequence must not be softened anywhere in the implementation: this is
not a sandbox, and no property of it may be described as isolation. The three
controls that remain real are **who may start it**, **what input may reach
it**, and **what is recorded**. Sections 3, 4 and 7 are those three controls.

## 3. Who may start it, and what may reach it

### 3.1 Start conditions

Checked in this order, before anything is created:

1. `GOPOD_ELEVATED` is on and `GOPOD_OWNER_CHAT_ID` is set (§8). With either
   missing the command is never registered, so it does not exist rather than
   failing.
2. Router `PermOwnerOnly` (D011). Owner identity reaches the Router as
   `Caller.IsOwner`, which the Telegram frontend computes by comparing the chat
   ID to `b.ownerChatID` (`internal/telegram/cmdbuttons.go:187` for presses).
   The handler does not see the configured owner chat ID unless it is injected
   at registration, so the registration takes it as a parameter the way
   `RegisterWhoami` and `RegisterChatCommands` already do.
3. `Caller.Source == SourceTelegram`, `Caller.ChatKind == "private"` and
   `Caller.MessageID > 0`. This is `/restart`'s test
   (`internal/control/restart.go:12`, `:28`); `/stop` checks only source,
   kind and arity (`internal/control/stop.go:9`), so it is not a precedent for
   the message fence.
4. `Command.Confirmed`, i.e. the owner pressed the danger button the first
   invocation returned (D028, `control.Confirm`). The press re-enters the
   Router, so steps 2 and 3 run again on it.
5. No elevated session is already live (§4.2).

Step 3 and step 4 are incompatible in the code as it stands: `callbackCaller`
builds a caller with no `MessageID`, so it is zero on every button dispatch
(`internal/telegram/cmdbuttons.go:180-191`). The owner's decision is to
populate it, which lands as the prerequisite slice E-pre (§11) because the same
gap currently rejects `/restart force`, `/restart status` and `/restart cancel`
on a deployed build.

### 3.2 Input that must never reach the session

An elevated session accepts the owner's live private messages and nothing else.
Each of these needs its own test:

- group and supergroup messages, at any trigger mode;
- any other private chat, registered or not;
- IPC message, task and input files written by any container, the elevated one
  included;
- the scheduler and the proactive presets;
- outbox retries and provider-fallback replays;
- a button press whose token has expired or whose chat does not match.

The scheduler case needs care, because there is nothing today that could
"refuse a session kind": `scheduled_tasks` is keyed by `chat_folder`
(`internal/store/schema.go:93-104`) and no session entity exists (§4.1). The
property that is actually testable is narrower and sufficient: the reserved
folder is unregisterable, task creation rejects it by name, and no registered
folder can produce the elevated container's name.

### 3.3 The residual risk, stated plainly

Text the agent reads during the session — a fetched page, a repository file, a
mail body, a command's own output — can carry instructions, and inside this
container those instructions act with host authority. Nothing here prevents
that. What the design does is make the window deliberate and visible: the owner
starts it explicitly, one session exists at a time, only the owner's own
messages steer it, the chat shows it is live, and every exec the host performs
is journalled. The owner accepts this risk; the implementation must not
describe it as mitigated.

## 4. Identity, routing and command surface

### 4.1 A reserved folder, not a new session entity

The first draft specified a new `elevated` session kind. There is no session
entity in gopod to extend: `sessions` maps `(chat_folder, thread_id, provider)`
to the provider CLI's own session string
(`internal/store/schema.go:83-90`), and CP-037's execution-session concept is
design-stage, slice P1, unstarted. Building a second session model here would
be work P1 then has to reconcile.

Instead the elevated runtime is bound to one **reserved chat folder**,
`elevated`, at thread 0. Everything folder-keyed then works unchanged:

| Mechanism | Effect with the reserved folder |
|---|---|
| `runner.ContainerName` | `gopod-elevated`, no new naming scheme |
| `sessions` table | Row `("elevated", 0, provider)`, existing shape, so a reopened session can continue the previous elevated conversation |
| Provider state dirs | `data/sessions/elevated/.claude` and `.codex`, exactly the layout `standardMounts` already uses (`internal/runner/mounts.go:154-162`) |
| Workspace | `chats/elevated/` at `/workspace/chat`, so `WorkingDir` is unchanged |
| Memory | Folder-scoped, so elevated extraction cannot overwrite the ordinary topic's facts (D023) |

The folder is created by the host at bootstrap and is never registered:
registration rejects the name, which also removes the collision the first draft
tried to fix with an `elevated-` prefix rule.

Two folder-keyed mechanisms must be excluded by name rather than inherited:

- **The idle watcher.** `killIdleChat` lists containers and stops every one
  whose `gopod.chat` label equals the idle folder
  (`internal/runner/lifecycle.go:163-187`). Left alone it would kill the
  elevated container 30 minutes after the owner stopped typing, which is the
  timer the owner declined. The watcher skips the reserved folder.
- **The scheduler**, per §3.2.

### 4.2 One at a time

One live elevated session per host, which the single reserved folder makes
structural rather than enforced. A second `/root` while one is live returns
that session's status and its controls.

### 4.3 Routing

While a session is live, every owner-private message that is not a command
routes to the reserved folder instead of the owner topic. A private chat has
one message stream and gopod cannot create a private topic — `ThreadID` is only
ever read off inbound messages (`internal/telegram/handler.go:281-286`) — so
this is modal by necessity, and the owner chose it knowing that ordinary
conversation pauses for the duration. The marker (§7) is what makes the mode
visible, and the explicit close is what ends it. The ordinary topic's history,
session row and provider choice are untouched throughout.

### 4.4 Commands

All `PermOwnerOnly`, all requiring the §3.1 identity.

| Form | Behaviour |
|---|---|
| `/root` | No live session: provider picker (`claude`, `codex`), per the owner's picker convention and D028. Live session: status plus its buttons. |
| `/root claude` \| `/root codex` | Text form. Returns the confirmation; the press starts the session. |
| `/root status` | Provider, start time, exec count, container state. |
| `/root log [n]` | Last `n` journal rows, default 20. |
| `/root stop` | Closes the session and removes the container. |

`/stop` keeps its CP-036 meaning and stops the current answer without closing
the session. Because routing is modal and the reserved folder is the live
conversation, `/stop`'s `(ChatID, ThreadID)` lookup
(`internal/control/stop.go:15`) resolves to the elevated turn while a session
is live, which is the intended behaviour and needs a test rather than new
machinery.

## 5. Container contract

### 5.1 Two builders, not one switch

`BuildContainerArgs` and `SpawnConfig` stay as they are, including `Validate`'s
refusal of uid 0 (`internal/runner/docker_args.go:83-85`). The elevated profile
gets its own `ElevatedSpawnConfig` and its own builder in its own file. No
configuration value may turn a chat spawn into an elevated one: the only way to
the elevated profile is to call the elevated builder, and its only caller is the
elevated session path.

The regression test for that rule is scoped precisely: **`BuildContainerArgs`'s
own output** for an ordinary turn is byte-identical to the pre-change fixture.
It cannot be asserted on the final create arguments, because the policy labels
are merged after the builder returns (`internal/runner/task_managed.go:186-188`).

### 5.2 Fields

| Field | Value | Why |
|---|---|---|
| `User` | `0:0` | The point of the profile |
| `Privileged` | `true` | Owner decision |
| `CapDrop` | unset | Implied by privileged; recorded in the digest as "not dropped" |
| `SecurityOpt` | unset | `no-new-privileges` is meaningless here and its presence would misrepresent the profile |
| `ReadonlyRootfs` | `false` | Host work needs a writable root |
| `PidMode`, `NetworkMode`, `IpcMode` | `host` | Owner decision |
| `WorkingDir` | `/workspace/session` | Distinct from the chat default so a wrong mount set is obvious |
| Bind `/` → `/host` | read-write | Host filesystem |
| Bind `/var/run/docker.sock` | read-write | Daemon control |
| Bind `chats/elevated/` → `/workspace/session` | read-write | The session's own files |
| Bind `data/sessions/elevated/.claude`, `.codex` → `/home/node/.claude`, `.codex` | read-write | Provider state at the paths the CLIs expect, with `HOME=/home/node` unchanged, so `stateDirSources` keeps recognising them (`internal/runner/exec_policy.go:190-198`) |
| `Memory`, `NanoCPUs`, `PidsLimit` | from config, all positive | Availability, not security: a privileged container with no pids cap can take the Pi down |
| `RestartPolicy` | `no` | gopod owns the lifecycle |
| `ExtraHosts` | unset | With `NetworkMode: host` the host-gateway alias is meaningless |
| Tmpfs | none | Unnecessary with a writable rootfs |
| Labels | `gopod.chat=elevated`, `gopod.version`, plus the four policy labels | Inventory and guard |

The ordinary chat mounts (project, vault, store, skills, IPC, memory) are not
added. The host filesystem is already at `/host`, so they would only create a
second set of paths for the same bytes.

### 5.3 Guard and policy role

A new `ExecRole`, `RoleElevatedHost`, added to `Valid()` and to `roleGrants`.
Three corrections to the first draft, each verified against the code:

**No `policyRevision` bump.** `Fingerprint` hashes the policy's own role, scope,
mounts, env names, credential binding, image and grants
(`internal/runner/exec_policy.go:149-173`), so adding a role leaves every
existing fingerprint untouched. A bump would instead make every container
created under revision 1 fail `deny("labels_stale_version")`
(`internal/runner/exec_guard.go:211-213`) until it is gone, which is a
deployment-ordering hazard for no benefit.

**A stale policy version denies, it does not recycle.** The first draft and the
comment at `internal/runner/exec_policy.go:45-46` both say "recycled". The code
denies, and nothing is removed. Any acceptance text that says otherwise is
wrong.

**New comparison logic is required.** The first draft claimed none was needed.
Two additions:

- `decideContainer` must reject a role the plan did not ask for. Today a
  well-formed container whose labels are self-consistent but whose fingerprint
  differs reaches `actRecreate`, "policy_mismatch"
  (`internal/runner/exec_guard.go:217-219`). For a role difference that is
  wrong in both directions, so the role is compared before the fingerprint and
  a mismatch denies.
- The namespace modes are not inspectable. `ContainerFacts` has no `PidMode`,
  `NetworkMode` or `IpcMode` (`internal/runner/exec_guard.go:52-66`) and
  `InspectContainer` does not read them
  (`internal/runner/docker_policy.go:34-50`), so `hardening` and therefore
  `configDigest` cannot currently tell a privileged container that kept host
  namespaces from one that lost them. The three fields are added to
  `ContainerFacts`, to the inspection and to `hardening`, each tagged
  `omitempty` so a chat container's empty values serialise exactly as before
  and no existing container's recorded digest changes.

**Wiring, and the managed-runtime gate.** `ensureGuarded` returns early unless
`r.managed` is set (`internal/runner/exec_guard.go:233-235`), which happens
only under `GOPOD_MANAGED_TASKS` (`internal/runner/task_managed.go:70-80`,
`cmd/gopod/main.go:255-259`, default off at `internal/config/config.go:226`).
Making `GOPOD_ELEVATED` imply that flag would turn the policy guard on for every
ordinary turn in production, which this design promises not to do. So the
elevated path takes the `containerEngine` from the Docker facade directly —
`*Docker` already implements it (`internal/runner/docker_policy.go:13`) — and
runs the same `decideContainer` with its own plan, independent of
`SetManagedTasks`. The owner's requirement of a separate `exec_guard` profile is
met by the shared decision function and the distinct role, not by the managed
runtime.

**Scope.** `EffectiveExecutionPolicy.Scope` normally comes from
`SnapshotScope` for a registered folder (`internal/runner/task_managed.go:145-148`),
and the elevated folder is deliberately unregistered. The elevated plan uses a
reserved constant `ScopeRef`: empty namespace ID, zero epoch, zero registration
revision, empty JID, `Folder: "elevated"`. It is a declared constant rather
than a snapshot, it is part of the fingerprint for good, and it is documented
here so it is never mistaken for a real namespace.

### 5.4 Image contents

The elevated session gets its own images, built from
`container/elevated/Dockerfile` and `container/elevated/Dockerfile.codex` and
added to `container/build-profiles.sh`, which builds from `container/personal/`
and `container/group/` today (`container/build-profiles.sh:4-8`); the top-level
`container/Dockerfile` is stale and built by nothing. There is one image per
provider because the chat images already split that way, with each CLI pinned
in its own file. The images run as root and keep the `sleep infinity`
entrypoint.

**Two routes to host work, and the image only has to serve the first.** With the
host PID namespace shared, `nsenter -t 1 -m -u -i -n -p -- <cmd>` runs a command
inside PID 1's namespaces, using the host's own binaries, libraries and
configuration; `chroot /host <cmd>` does the same for the filesystem view alone.
That is how host administration should be done here, because the Pi's own tools
match the Pi's own state. `systemctl`, `journalctl`, `apt` and anything else
distribution-specific are therefore deliberately **not** installed: a second
copy invites version skew against the host it is meant to manage. The image's
job is to bootstrap into the host and to work on files and on the Docker daemon
from outside it.

Over what the chat images already carry (`ca-certificates`, `curl`, `git`,
`ripgrep`, Node and the provider CLI):

| Addition | Why it is needed |
|---|---|
| `docker-ce-cli` plus the Compose plugin | The socket is bound in; the daemon is managed from here. Pinned to a major version and checked against the daemon the Pi runs |
| `util-linux` | `nsenter` is the bootstrap above; `flock`, `lsblk` and `dmesg` come with it |
| `procps` | Host processes are visible through the shared PID namespace and need `ps`/`top` to be read |
| `iproute2`, `iputils-ping`, `dnsutils` | The host network namespace is shared: `ip`, `ss`, `ping`, `dig` |
| `lsof` | Which process holds a port or a file |
| `rsync` | Moving and comparing trees under `/host` |
| `sqlite3` | Reading gopod's own store, with the caution below |
| `jq` | `docker inspect` and the Docker API return JSON, and gopod can be switched to JSON logs |
| `less`, `vim-tiny` | Editing in place rather than echoing heredocs at host config |

`tar` and `gzip` are in the base. Nothing here is a credential, so §8's secrets
rule is unaffected. Note that gopod's own logs are **text** by default
(`internal/config/config.go:216`, `.env.example:31`) and also land in the store's
`logs` table, so `jq` is justified by the Docker API rather than by the logs.

**Caution on `sqlite3`.** The controller holds `data/store.sqlite` open while it
runs, and gopod's driver is the pure-Go WASM one. Reads from the CLI must be
read-only and non-locking (`sqlite3 -readonly`, or `immutable=1` on a copy); a
write from the CLI against the live file is a corruption risk, not a maintenance
step. The session's own text says this where the owner will see it.

**Build.** arm64 for the Pi, pinned by base-image digest. Exact apt version pins
are deliberately not used: they break on every Debian point release and the base
digest already fixes the package set.

## 6. Lifecycle

**Start.** Confirmed command → journal the session-open row → create and start
the container → write the session state → post the start marker. If create
fails, no session state exists and `CreateStart` already removes a half-created
container (`internal/runner/docker_policy.go:73-85`).

**While live.** No timer, and the idle watcher skips the reserved folder
(§4.1). D054's occupancy rule still protects an in-flight turn.

**Close.** One teardown function with exactly four callers: `/root stop`,
`/restart` drain, `/restart force`, and controller shutdown. It removes the
container, settles the journal, clears the live-session state so routing
reverts, and posts the teardown marker. Keeping it single-valued is what makes
a later TTL a new caller rather than a redesign.

**`/restart`.** An elevated session with an active turn counts as busy for the
drain, like any other in-flight reply. The runtime is torn down before the
controller exits and never recreated at boot: resuming host authority without a
fresh confirmation would defeat §3.1.

**Boot.** The first draft said "before any other startup work", which cannot
hold: Docker does not exist until `cmd/gopod/main.go:163`, after the store and
the instance lock, and the D054 restart recovery runs at `:179`. The real
ordering is **after `restart.recycle`, before `CleanupLeftovers`**, and it is
load-bearing rather than cosmetic. `inventoryLeftovers` quarantines *any*
non-exited gopod-labelled container and returns `ErrExecutionUnproven`
(`internal/runner/lifecycle.go:49-71`), which `main.go:200-206` turns into
`restartRecoveryErr` and a global execution block. A surviving elevated
container that reached the inventory would not merely fail to be removed; it
would block the whole controller. So it is removed on its own path, logged, and
reported in the chat on the next owner contact. It is deliberately not
quarantined: a quarantined chat container exists so a possibly running execution
is not lost, and a surviving elevated container has no durable claim to protect.

## 7. Audit

**Confirmation.** `control.Confirm` with the danger style, and a question that
names the authority in plain words rather than a label.

**Chat marker.** A start notice, a prefix on every reply from the session, and a
teardown notice, all rendered by the host from the live-session state. Agent
output never produces or suppresses the marker. While routing is modal (§4.3)
the prefix is also the only thing telling the owner which conversation they are
in, so it is not optional and not configurable.

**Exec journal.** A new append-only store table, one row per `docker exec` into
the elevated container: sequence number, argv, start and end time, exit code,
truncation flag. Ordering is journal-first: the intent row is committed before
the exec runs and settled after it returns, so a crash leaves an unsettled row
rather than no row, and a journal write that fails before the exec refuses the
exec. argv is capped at 4 KiB and passes `log.Redactor.Text`
(`internal/log/redact.go:30-36`), which covers the controller's own
host-provisioned secret values.

**What the journal records, exactly.** gopod's execs into an agent container are
the provider invocations (`ExecStream`, `internal/runner/docker.go:357-380`).
The host commands the agent runs are its own tool calls *inside* the container
and never cross the Docker exec API. So a journal row says "the Claude CLI was
invoked at 14:02 and exited 0", not "the agent restarted a unit". The owner
chose this deliberately over a wrapper inside the image that would log the
commands but could not be attested by the host. To see what was actually done,
read the session transcript. The docs and the owner-facing text must say this
plainly; an acceptance criterion that implies the journal records administrative
actions would be false.

**What the journal is not.** It lives in a file the elevated container can
rewrite through `/host`, and so can the log beside it. It is evidence of
ordinary operation, not tamper-proof audit. Making it tamper-evident needs an
off-host sink and is out of scope.

## 8. Configuration

| Variable | Default | Effect |
|---|---|---|
| `GOPOD_ELEVATED` | off | On: registers `/root`. Off: the command is not registered |
| `GOPOD_ELEVATED_IMAGE` | `gopod-elevated:latest` | Claude image (§5.4) |
| `GOPOD_ELEVATED_IMAGE_CODEX` | `gopod-elevated-codex:latest` | Codex image |
| `GOPOD_ELEVATED_MEM` | conservative, Pi-4 safe | Hard memory cap |
| `GOPOD_ELEVATED_CPUS` | conservative | CPU cap |
| `GOPOD_ELEVATED_PIDS` | conservative | Pids cap |

Enabling requires `GOPOD_OWNER_CHAT_ID`; without it `Config.validate`
(`internal/config/config.go:302-307`) rejects the configuration with a clear
message. `.env.example` documents the flag in its off state. No secret value
appears in any committed file (D012).

## 9. Failure and recovery

| Condition | Behaviour |
|---|---|
| Docker unreachable | The command reports unavailable, like `/restart` with no supervised controller |
| Create or start fails | No live-session state, error in chat, nothing left behind |
| Journal write fails before an exec | The exec is refused |
| Controller crashes while live | §6 boot removal; the next owner contact is told |
| Container dies on its own | The next turn reports it and closes the session; no silent recreate |
| Provider login missing in the session | Ordinary provider error inside the session; no fallback to the topic's credential home |
| Routing state says live but no container exists | Treated as a dead session and closed, so the owner is never silently talking to nothing |

## 10. Out of scope

TTL and idle expiry. A second concurrent elevated session. Group or non-owner
access. Scheduled, proactive or task-driven elevated runs. Relaxing the chat or
group profile. Installing host-distribution tooling in the image (§5.4). A
command-level journal written from inside the container (§7). Tamper-evident
off-host audit. Remote and multi-host operation. Pi deployment, which stays
separately authorised and which the owner has deferred until CP-037 is complete.

## 11. Prerequisite

**E-pre, its own small PR before any of this.** Populate `MessageID` in
`callbackCaller` (`internal/telegram/cmdbuttons.go:180-191`) and test it. Today
every button dispatch arrives with `MessageID == 0`, so `/restart force`'s
confirmation, `/restart status` and `/restart cancel` are all rejected by
`internal/control/restart.go:28` on the deployed build. The elevated
confirmation needs the same field, and fixing it once serves both.

## 12. Acceptance

**A — gating.** Flag off: `/root` unknown. Flag on without an owner chat ID:
configuration rejected. Group, non-owner private, IPC and expired-token press
each fail to start a session. First `/root` creates nothing; only the confirmed
press does. A scheduled task cannot be created for the reserved folder, and the
folder cannot be registered.

**B — profile.** The elevated container inspects as privileged, uid 0, host
PID/NET/IPC, with `/host` and the socket bound, and the inspection reads the
namespace modes back. `BuildContainerArgs`'s output for an ordinary turn is
byte-identical to the pre-change fixture, and an existing chat container's
recorded config digest still matches after the `hardening` fields are added. A
container labelled with a role the plan did not request is denied, not recycled.

**C — lifetime and audit.** `/root stop`, `/restart`, `/restart force` and
controller shutdown each remove the container, close the session and revert
routing. A container surviving a `kill -9` is removed between `restart.recycle`
and `CleanupLeftovers`, and the controller's execution path is not blocked. The
idle watcher leaves a live session alone past the idle timeout. Every host exec
has a journal row readable by `/root log`, with the intent row present before
the result reaches the chat. Start, prefix and teardown markers appear and are
host-rendered.

**D — not acceptance.** Startup, CI and fixture checks do not establish real
host behaviour. Host PID and network namespace sharing, privileged creation
through the socket by the non-root controller process
(`docker-compose.yml`, `user:` plus `group_add`), and `nsenter` into PID 1 must
be checked on real Docker on the Pi before the feature is considered working
there.

## 13. Corrections to the first draft

Kept so the same errors are not re-derived. Each was verified in the source.

| First draft said | Reality |
|---|---|
| A `policyRevision` bump recycles older containers | It denies them (`exec_guard.go:211-213`), and no bump is needed because a new role does not change existing fingerprints |
| `exec_guard` needs no new comparison logic | A role mismatch currently recycles; namespace modes are not inspected at all |
| The `/restart` and `/stop` test includes the owner chat ID | Neither checks it; `/stop` does not even check `MessageID` |
| The hardened profile is pinned by digest, unconditionally | Only under `GOPOD_MANAGED_TASKS`, which is off by default |
| `ISOLATION.md §6` documents the hardened chat profile | §6 is group tool admission and resource budgets; the `§6.1` the code comments cite no longer exists, which is doc drift to fix |
| A new `elevated` session kind | No session entity exists; a reserved folder reuses what does |
| The build profile lives beside `container/Dockerfile` | That file is stale; profiles build from `container/personal/` and `container/group/` |
| `jq` is justified by JSON logs | Logs are text by default and also go to SQLite |
| Removal happens before any other startup work | It must sit between `restart.recycle` and `CleanupLeftovers`, or a survivor blocks all execution |
| The scheduler refuses the session kind | Nothing is keyed by session kind; the testable property is the reserved folder |
