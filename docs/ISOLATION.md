# gopod — Container isolation policy

> Companion docs: [ARCHITECTURE.md](ARCHITECTURE.md) · [DECISIONS.md](DECISIONS.md) · [SKILLS.md](SKILLS.md)
> Decision: [D013](DECISIONS.md)

gopod runs one Docker container per chat. The agent inside that container
has tool access to the filesystem (Read/Write/Bash/Edit) and the network. The
container is therefore the **trust boundary**: anything the agent should not
be able to touch must be on the other side of the mount and capability set.

This document mirrors NanoClaw's `src/mount-security.ts` and
`src/container-runner.ts`, adapted to Go and to gopod's flat owner-vs-rest
permission model.

---

## 1. Threat model

**In scope** (we defend against):

- A misbehaving agent in chat A reading or writing chat B's files
- An agent reading host secrets (`.env`, `~/.ssh`, `~/.aws`, OS keychains)
- An agent escaping its container via privileged syscalls or mounted device
  files
- An agent persisting code that survives a container restart unnoticed (we
  audit via labels and an explicit per-chat workspace)
- Path-traversal in user-supplied mount specifications (`../../etc/shadow`)
- Symlink games inside the host workspace (a symlink to `/etc` mounted into
  the container)
- Forgotten leftover containers from a previous gopod run holding stale
  filesystem state

**Out of scope** (acknowledged, not defended against):

- A malicious *operator* who has shell access to the gopod host. The
  operator is trusted; D012 already says "shell access = owner".
- Side-channel attacks against the Docker daemon itself
- Zero-days in `docker exec` stdio multiplexing
- Network exfiltration. The agent can reach the public internet by design
  (it's a Claude agent — it needs API access). Network policy is left to
  the user's host firewall.

---

## 2. Trust tiers

| Tier | Examples | Mounts |
|---|---|---|
| **Owner chat** | `GOPOD_OWNER_CHAT_ID` | Full set: project root (RO), `data/store.sqlite` (RW), own chat folder (RW), shared scratchpad (RW), all configured extra mounts |
| **Registered non-owner chat** | Any other registered chat | Own chat folder (RW), own memory dir (RW), own IPC namespace (RW), own session dir (RW), filtered container skills (RO), allowlisted extras (RO unless explicitly RW) |
| **Unregistered chat** | A Telegram chat gopod has not been told about | No container at all. Messages are stored, control plane responds to `/whoami` and `/help`, agent never runs. |

There are exactly these three tiers. The "main group is privileged" idea
from NanoClaw collapses to **owner chat = tier 1, all other registered chats
= tier 2** ([D006](DECISIONS.md)).

---

## 3. Standard mounts

These mounts are constructed by `internal/runner/mounts.go` for every chat,
no allowlist consultation needed.

### 3.1 Owner chat

| Host source | Container target | Mode | Purpose |
|---|---|---|---|
| `${REPO_ROOT}` | `/workspace/project` | RO | Read project source for self-modification (`/release`, debugging) |
| `${REPO_ROOT}/.env` | `/dev/null` | RO | **Override**: explicitly block agent access to the `.env` file even though it's inside the project mount |
| `${DATA_DIR}/store.sqlite` | `/workspace/store/store.sqlite` | RW | Direct SQL access for advanced debugging (acknowledged risk: agent can corrupt store; see D006 consequences) |
| `${CHATS_DIR}/<folder>` | `/workspace/chat` | RW | Owner's own chat workspace |
| `${CHATS_DIR}/<folder>/memory` | `/workspace/memory` | RW | Anthropic Memory Tool scratchpad (Layer 2) |
| `${DATA_DIR}/ipc/<folder>` | `/workspace/ipc` | RW | IPC namespace for this chat |
| `${DATA_DIR}/sessions/<folder>/.claude` | `/home/node/.claude` | RW | Claude CLI session state (`*.jsonl`, `settings.json`) |
| `${DATA_DIR}/sessions/<folder>/.codex` | `/home/node/.codex` | RW | Codex CLI session state (rollouts, `auth.json`); mounted for every chat regardless of provider |
| `${REPO_ROOT}/container/skills` (filtered by `skills.json`) | `/home/node/.claude/skills` | RO | Container skills, filtered by per-chat allow/deny ([SKILLS.md](SKILLS.md)) |

### 3.2 Non-owner registered chat

| Host source | Container target | Mode | Purpose |
|---|---|---|---|
| `${CHATS_DIR}/<folder>` | `/workspace/chat` | RW | This chat's workspace only |
| `${CHATS_DIR}/<folder>/memory` | `/workspace/memory` | RW | Memory scratchpad |
| `${DATA_DIR}/ipc/<folder>` | `/workspace/ipc` | RW | IPC namespace |
| `${DATA_DIR}/sessions/<folder>/.claude` | `/home/node/.claude` | RW | Session state |
| `${DATA_DIR}/sessions/<folder>/.codex` | `/home/node/.codex` | RW | Codex session state |
| `${REPO_ROOT}/container/skills` (filtered) | `/home/node/.claude/skills` | RO | Container skills filtered by `skills.json` |

**Never** mounted for non-owner chats:

- `${REPO_ROOT}` (project root) — not even RO
- `${DATA_DIR}/store.sqlite` — the agent has no SQL handle on cross-chat data
- `${REPO_ROOT}/.env`
- `${REPO_ROOT}/.claude/skills` (those are dev skills for gopod maintainers)
- Any other chat's folder, memory, ipc, or session directory
- `~/.config/gopod/`
- `~/.ssh`, `~/.aws`, `~/.gnupg`, `~/.docker`

---

## 4. Extra mounts (allowlist)

The owner can extend a chat's filesystem view with additional bind mounts —
e.g. mounting a personal git project into a chat dedicated to that project.
These extras live in a single allowlist file managed by the owner, never
inferred from chat input.

### 4.1 File location

```
${DATA_DIR}/mount-allowlist.json
```

(per user decision 2026-04-09; lives next to `store.sqlite` so backup is
still one-directory). The file is `chmod 0600`, owned by the gopod
process user. gopod refuses to start if permissions are wider.

### 4.2 Schema

```json
{
  "version": 1,
  "extra_mounts": [
    {
      "name": "code",
      "host_path": "/Users/me/code/myproject",
      "container_path": "/workspace/extra/code",
      "mode": "ro",
      "non_owner_read_only": true,
      "allowed_chats": ["myproject"]
    },
    {
      "name": "downloads",
      "host_path": "/Users/me/Downloads",
      "container_path": "/workspace/extra/downloads",
      "mode": "rw",
      "non_owner_read_only": true,
      "allowed_chats": ["*"]
    }
  ]
}
```

| Field | Required | Meaning |
|---|---|---|
| `name` | ✅ | Identifier the agent and CLI use to refer to the mount |
| `host_path` | ✅ | Absolute path on the host. Must exist. Must be a directory. Must not be (or contain) a symlink that escapes the parent. |
| `container_path` | ✅ | Where it appears inside the container. Must start with `/workspace/extra/`. |
| `mode` | ✅ | `ro` or `rw` |
| `non_owner_read_only` | optional, default `true` | If true, non-owner chats get RO regardless of `mode`. The owner chat always gets the declared `mode`. |
| `allowed_chats` | ✅ | List of chat folder names allowed to mount this entry. `["*"]` allows any registered chat. `[]` means owner-only. |

### 4.3 Validation rules

`internal/runner/mountsec/mountsec.go` runs all of these on every entry,
**every time** gopod starts and **every time** the file changes:

1. **Schema valid.** JSON parses, all required fields present, types match.
2. **`host_path` is absolute** (`filepath.IsAbs`) and contains no `..` after
   `filepath.Clean`.
3. **`host_path` exists** and `os.Stat` reports a directory.
4. **`filepath.EvalSymlinks(host_path)`** resolves successfully and the
   resolved path is itself absolute.
5. **Resolved path does not match a blocked pattern** (see §5).
6. **`container_path` starts with `/workspace/extra/`** and contains no
   `..`. Two entries cannot share the same `container_path`.
7. **`allowed_chats`** entries are valid chat folder names (regex
   `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`) or the literal `"*"`.

gopod refuses to start if any entry fails. The error message names the
entry and the rule.

### 4.4 Per-chat mount construction

For each agent invocation:

1. Start with the standard mounts for the chat's tier (§3).
2. Walk `extra_mounts`. For each entry where `chat_folder ∈ allowed_chats`
   or `allowed_chats == ["*"]`:
   - Compute the effective mode: `non_owner_read_only && !is_owner ? "ro" : entry.mode`
   - Append the mount to the spec.
3. Pass the final list to Docker via the SDK.

---

## 5. Blocked path patterns

Compiled into `internal/runner/mountsec/blocked.go`. Not configurable from
the allowlist file — these are absolute prohibitions, even for the owner.

Patterns are matched against the **resolved** `host_path` (after symlinks).
Glob semantics: `**` matches any number of path segments.

```
# SSH / GPG / cloud creds
**/.ssh/**
**/.ssh
**/.gnupg/**
**/.gnupg
**/.aws/**
**/.aws

# Container runtime config
**/.docker/**
**/.docker

# gopod's own config & secrets surface
**/.config/gopod/**

# Generic secret-shaped files
**/credentials
**/credentials.json
**/id_rsa
**/id_rsa.*
**/id_ed25519
**/id_ed25519.*
**/.netrc
**/.pgpass
**/.env
**/.env.*

# System sensitive
/etc/shadow
/etc/sudoers
/etc/sudoers.d/**
/proc/**
/sys/**
/dev/**
```

The matcher is case-sensitive on Linux and case-insensitive on macOS (because
HFS+/APFS default to case-insensitive). Tested both ways.

If a `host_path` validates against the patterns but its `EvalSymlinks` target
matches a pattern, the entry is rejected — preventing the symlink-to-secrets
attack.

---

## 6. Container spawn flags

Constructed in `internal/runner/docker.go`. Applied to **every** gopod
container, owner or not.

### 6.1 Always-on

| Flag | Value | Purpose |
|---|---|---|
| `--name` | `gopod-<folder>` | Predictable name for `docker exec` and cleanup |
| `--label` | `gopod.chat=<folder>` | Cleanup discriminator |
| `--label` | `gopod.version=<version>` | Detect version mismatches at boot |
| `--user` | `<uid>:<gid>` | Host user, not root — required for bind-mount writes to be readable on the host |
| `--read-only` | (set) | Root filesystem RO; agent writes only to mounted volumes |
| `--tmpfs` | `/tmp:rw,size=512m,mode=1777` | Working scratch space |
| `--tmpfs` | `/home/node/.cache:rw,size=256m` | Claude CLI cache, npm cache |
| `--tmpfs` | `/run:rw,size=64m` | systemd-style runtime dir |
| `--cap-drop` | `ALL` | Drop every Linux capability |
| `--security-opt` | `no-new-privileges:true` | setuid binaries can't gain privileges |
| `--security-opt` | `seccomp=default` | Default seccomp profile |
| `--pids-limit` | `1024` | Fork-bomb protection |
| `--memory` | `${GOPOD_CONTAINER_MEM}` (default `4g`) | Per-chat RAM cap |
| `--cpus` | `${GOPOD_CONTAINER_CPUS}` (default `2`) | Per-chat CPU cap |
| `--restart` | `no` | gopod decides restart policy, not Docker |

### 6.2 Linux-only

| Flag | Value | Purpose |
|---|---|---|
| `--add-host` | `host.docker.internal:host-gateway` | Reach gopod on the host (e.g. for Ollama, MCP HTTP frontends). macOS Docker Desktop provides this automatically. |

### 6.3 Conditional

| When | Flag |
|---|---|
| Network policy = isolated (future) | `--network=gopod-isolated` (a per-chat user-defined bridge) |
| GPU access requested via allowlist | `--gpus=all` (owner-chat extras only) |
| Profiling builds | `--cap-add=SYS_PTRACE` (debug only, never default) |

### 6.4 Capabilities deliberately NOT added

We do not add back any of: `NET_ADMIN`, `SYS_ADMIN`, `SYS_MODULE`,
`SYS_PTRACE`, `SYS_RAWIO`, `MKNOD`, `DAC_READ_SEARCH`. If a future skill
needs one of these, it goes through an ADR, not a code change.

---

## 7. Lifecycle

### 7.1 Boot-time leftover cleanup

When gopod starts:

1. `client.ContainerList(ctx, types.ContainerListOptions{All: true,
   Filters: filters.NewArgs(filters.Arg("label", "gopod.chat"))})`
2. For each container found:
   - Log `chat`, `version`, `state`, `started_at`
   - If state is running: `ContainerStop` with 5s timeout, then
     `ContainerRemove`
   - If state is exited: `ContainerRemove`
3. Log the final count: `cleaned N leftover containers`

This handles the case where gopod was killed mid-run and Docker is still
holding the previous container.

### 7.2 Idle kill

For every chat with a running container, a watcher goroutine compares
`time.Since(lastActivity)` against `GOPOD_IDLE_TIMEOUT` (default `30m`).
On timeout:

1. Send `_close` sentinel into the chat's IPC `input/` directory
2. Wait up to 5s for graceful exit
3. `ContainerStop` with another 5s timeout
4. `ContainerRemove`
5. Log the kill at `info` level with the chat folder

### 7.3 Crash recovery

A crashed container (exit code != 0) is logged at `error` level with the
chat folder, the last 50 lines from the `logs` SQLite table for that chat,
and a `gopod.container.crashes_total` counter increment. The next
incoming message for that chat triggers a fresh container — gopod does
**not** auto-restart on its own, because crash loops should be visible.

### 7.4 Update path

When a new gopod version is deployed:

1. Boot reads the `gopod.version` label on existing leftover containers
2. If the version mismatches the current build, the leftover is removed
   even if it's running (the new gopod can't trust state from the old
   one)
3. The first message after restart spawns a fresh container with the new
   version label

---

## 8. Module layout

```
internal/runner/
├── runner.go                # Run(chat, prompt) → reply
├── docker.go                # Docker SDK spawn + attach + demux
├── docker_args.go           # Construct ContainerCreate args (all the flags from §6)
├── subprocess.go            # GOPOD_NO_CONTAINER fallback (lab only)
├── markers.go               # OUTPUT_START/OUTPUT_END parsing
├── mounts.go                # Construct per-chat mounts (calls mountsec for extras)
├── lifecycle.go             # Idle watcher, leftover cleanup, crash logging
└── mountsec/
    ├── mountsec.go          # Allowlist load, validate
    ├── allowlist.go         # JSON file format types
    ├── blocked.go           # Compiled-in blocked patterns
    ├── path.go              # Symlink resolution, traversal check, OS-aware case
    └── mountsec_test.go     # 100% coverage of validation rules
```

`internal/runner/mountsec` is its own subpackage so the security-critical
code can be tested in complete isolation, with no Docker SDK or
filesystem-state dependencies in the test suite (it uses `t.TempDir` and
`os.Symlink`).

---

## 9. Configuration

```
GOPOD_DATA_DIR=./data
GOPOD_REPO_ROOT=                            # auto-detected from binary location
GOPOD_MOUNT_ALLOWLIST=                      # default: ${GOPOD_DATA_DIR}/mount-allowlist.json
GOPOD_CONTAINER_IMAGE=gopod-agent:latest
GOPOD_CONTAINER_MEM=4g
GOPOD_CONTAINER_CPUS=2
GOPOD_IDLE_TIMEOUT=30m
GOPOD_LEFTOVER_CLEANUP=1                    # set to 0 to disable boot cleanup (debug only)
```

---

## 10. Test plan

The `internal/runner/mountsec` package ships with these unit tests:

| Test | Verifies |
|---|---|
| `TestAbsolutePathRequired` | Rejects relative `host_path` |
| `TestNoTraversal` | Rejects `..` in `host_path` and `container_path` |
| `TestMustExist` | Rejects nonexistent host path |
| `TestMustBeDirectory` | Rejects file (regular, device, named pipe) |
| `TestSymlinkResolution` | A symlink to a blocked path is rejected even if the symlink itself is in an allowed location |
| `TestSymlinkOutsideParent` | A symlink that resolves outside the original parent is rejected |
| `TestBlockedPatternsLinux` | Each pattern in §5 is matched correctly (case-sensitive) |
| `TestBlockedPatternsMacOS` | Same patterns matched case-insensitively on darwin |
| `TestContainerPathPrefix` | `container_path` not under `/workspace/extra/` is rejected |
| `TestContainerPathCollision` | Two entries sharing a `container_path` is rejected |
| `TestAllowedChatsValidation` | Folder names not matching the regex are rejected |
| `TestNonOwnerReadOnlyEnforced` | Non-owner chat receives RO even when `mode=rw` |
| `TestAllowlistFilePermissions` | Refuses to start if file mode is wider than 0600 |

The runner-level tests (`internal/runner`) cover spawn-args construction
end-to-end against a fake Docker SDK to verify that every flag from §6
makes it to the `ContainerCreate` call.

---

## 11. Sources

- NanoClaw `src/mount-security.ts` — allowlist validation prior art
- NanoClaw `src/container-runner.ts` — mount construction prior art
- [Docker — `--read-only`, `--cap-drop`, `--security-opt`](https://docs.docker.com/engine/security/)
- [Docker SDK for Go — `container.HostConfig`](https://pkg.go.dev/github.com/docker/docker/api/types/container#HostConfig)
- [seccomp default profile](https://docs.docker.com/engine/security/seccomp/)
- [SCMP_ACT_ALLOW vs default](https://docs.docker.com/engine/security/seccomp/#significant-syscalls-blocked-by-the-default-profile)
