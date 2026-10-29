# gopod — Container isolation policy

Updated: 2026-10-27. CP-020 implementation merged to main in PRs #66/#67
(`a3a9c53a24456cce40678231ef048e6baba51956`) with green CI/Sonar and independent
review. Native policy v2 and the catalog architecture below are implemented;
the controller is deployed with zero enabled groups. Both exact-Pi native TCP
preflights passed; full qualification failed (expired Claude auth; Codex tree-budget
admission after preflight). Controlled-group activation remains blocked. See
[release evidence](deployments/2026-10-27-group-isolation.md).

[Contract](superpowers/specs/2026-10-25-personal-group-container-isolation.md) ·
[Implementation and acceptance](superpowers/plans/2026-10-25-personal-group-container-isolation.md) ·
[Audit](research/2026-10-25-personal-group-isolation-audit.md)

## 1. Threat model

Group messages, attachments, generated files, model tool arguments and edited
workspace instructions are untrusted. They cannot grant access to personal facts,
other groups, credentials, account services, host mounts or arbitrary networking.
An owner speaking in a group does not make its audience private. Read-only private
file access is still disclosure; prompt instructions are not the access boundary.

The Docker daemon, host controller, pinned native CLI and reviewed relay are trusted
components. Untrusted commands run elsewhere. A passed scoped SQL test or a
successful model reply alone does not qualify the complete boundary.

## 2. Trust profiles

| Context | Runtime authority |
| --- | --- |
| Configured positive owner ID in its registered private chat | Existing personal profile, workspace, services and native account management |
| Registered, observed group/supergroup with a qualified clean epoch | Explicit group policy, inference-only native account reference, offline scoped tools and local memory/IPC |
| Unknown, unregistered, channel/Guest or other-private agent context | No qualified agent runtime; no fallback to personal authority |

Registration, `IsOwner`, a folder named `owner` and a negative chat ID alone are
insufficient. Group admission checks the exact registration, observed chat kind,
namespace/epoch/revision, explicit enabled configuration, readiness and cleanup
state. Threads retain their existing within-chat sharing; per-thread filesystem
secrecy is not promised. Additional integrations and one-way private reads of
group data require future explicit grants; none is enabled by this implementation.

## 3. Standard mounts and state

### 3.1 Owner chat

Personal behavior is preserved: the existing chat/memory/IPC and both native homes,
optional personal vault, selected env and configured skills/extras remain personal.
The project is read-only with its `.env` masked. The store file bind retains its
existing parent/WAL limitation; agents schedule through IPC, not direct SQL.
See [mount construction](../internal/runner/mounts.go) and [login](LOGIN.md).

### 3.2 Qualified group

Store assigns a UUID namespace and monotonic epoch/revision. Permanent ownership
records reserve old keys. A clean epoch uses an opaque `ns-<uuid>-e<N>` storage key;
changing a display alias cannot transfer memory/history/files or native sessions.
Old data stays preserved under retired keys and is never automatically imported.

Protected sources are separate siblings under
`${DATA_DIR}/shared-namespaces/<UUID>/e<N>/`:

| Source | Boundary |
| --- | --- |
| `workspace` | Controller-downloaded uploads; native inference never mounts it |
| `memory` | Group-only file state, separate from writable workspace parents |
| `ipc` | Host-validated group requests, receipts and archives |
| `native/claude`, `native/codex` | Current group/provider session state only |
| `policy` | Host-only fixed policy, relay configuration and temporary auth references |

Creation/validation holds parent descriptors with no-follow traversal, verifies
ownership/modes and rejects unsafe sources. No ancestor of these protected parents
is mounted into native inference or untrusted tools. Legacy vault/project/store,
shared skills, allowlist extras and host credential env are absent from groups.

The trusted native CLI sees a synthetic `/workspace/inference`, its group-only
native state, fixed read-only policy and one explicit read-only auth-only file.
The source inode is pinned with a temporary host-only hardlink; no personal HOME,
settings, plugins, history or service configuration is copied. Untrusted tools
receive neither credentials nor host bind mounts. Files persist only through
bounded canonical regular-file snapshots under the controller-owned
`${DATA_DIR}/shared-tool-snapshots/<storage-key>` root.

## 4. Extra mounts and services

The legacy `${DATA_DIR}/mount-allowlist.json` is a **personal** facility. Neither
wildcard entries nor read-only mode grant group access. `non_owner_read_only` is
retained for compatibility, not as group authorization. Group image/profile
selection cannot fall back to a personal image when configuration is missing.

Group memory uses local SQL/FTS without a personal OpenAI embedding key. Group
speech input and voice replies are disabled before ASR/TTS service calls; photo
and document downloads use protected workspace paths. Automatic group extraction,
compaction and maintenance remain unavailable pending qualified CP-006 support.
The private baseline keeps its existing speech and memory behavior.

Provider apps/connectors, inherited MCP/plugins/hooks, remote plugin installation,
native file/code tools and arbitrary account services are disabled. The generic
native-subscription account still shares quota: this is not a dedicated account
or billing partition. Auth refresh endpoints are excluded. Missing/expired auth
fails closed until private account management refreshes the source; groups never
rotate the personal refresh token or fall back to a paid API key.

## 5. Network and capability boundary

A fresh internal isolated Docker network gives native inference no general host
route or external egress. A separate dual-homed relay is its only permitted
provider path and has no native credentials or Docker socket. Exact HTTPS
host/method/path/query grants and request-tool inventories are enforced outside
agent-controlled CLI files. DNS/IP/redirect alternatives, generic CONNECT tunnels,
WebSockets, refresh and account-service routes do not become implicit grants.

Claude uses pinned restricted native policy; Codex uses native ChatGPT auth with
qualified HTTPS/SSE. See [SSE qualification source](research/2026-10-26-codex-group-native-sse.md)
and [proxy implementation](../internal/isolationproxy/README.md). Docker metadata
and proxy env alone are insufficient: the exact host network must be tested.

`group_exec` runs offline commands. `group_request` accepts only current-scope
local memory, IPC message/task requests and bounded receipt reads. It cannot pick
another namespace, change credentials or add routes. Group schedule creation omits
`taskId`; the host returns a scoped ID for later management. Missing and foreign
task/namespace probes produce equivalent refusal instead of private metadata.
Message acceptance is not proof of Telegram delivery; CP-002 remains separate.

## 6. Resource and execution limits

Untrusted tools have no network or host binds, read-only root, dropped capabilities,
non-root uid, bounded tmpfs/inodes, a 20-second command deadline and 32 KiB output.
Snapshots allow at most 8 MiB total and 4 MiB per regular file; traversal, links,
special files and oversized archives are rejected. A hard 32 MiB address-space
ceiling applies to each command process. The kernel PID controller must report
a ceiling of eight before commands run. Background/leftover processes prevent
successful export; arbitrary background services are unsupported.

Native/policy/workspace state has admission budgets (native 512 MiB / 20000
files, workspace 256 MiB / 5000, policy 8 MiB / 128; depth 64 path components). The byte
and file limits are runaway-growth guards for owner-run groups, not security limits and
native files have a hard file-size limit. The walk is rooted and no-follow and
rejects symlinks, FIFOs/sockets/devices, depth, bytes and file count, returning a
typed error with a fixed reason code (`symlink`, `special_file`, `depth`, `bytes`,
`files`, `walk`) and a sanitized two-component path class. It is logged on the
host and carried as `AdmissionError.Detail` for owner-private diagnostics (the
canary prints `admission_detail=`); group chats never see it. One narrow
exemption exists: pinned Codex leaves per-process alias symlinks at exactly
`codex/tmp/arg0/codex-arg0<id>/{apply_patch,applypatch,codex-linux-sandbox,codex-execve-wrapper}`
after every run (even `--version`, and when killed), so only those entries in the
native tree may be symlinks; they are never followed and Codex's own janitor
removes stale ones. Any other symlink, including a different name or depth under
`tmp/arg0`, still fails closed. Self-healing: in the native and workspace trees (writable by untrusted or
semi-trusted code) a planted symlink or special file outside those aliases is not a
reason to refuse the group. The host `Lstat`s it, unlinks exactly that entry with
`unlinkat` on the already verified parent directory handle (never following,
never recursing; a directory swapped in meanwhile makes the unlink fail), logs a
warning with the sanitized reason and path class and increments an in-process counter, then
admits. Failure to remove (`heal`), or any bytes/files/depth/walk violation, still
refuses. The policy tree never heals. No owner-private note is sent (log only).
Raising the limits is a tuning change to guards, not a contract change (no D048). Requested Docker memory settings do not prove target-host
memory-cgroup enforcement. T9 remains a separate paused host task: this change
does not edit boot flags, enable the memory cgroup or reboot the Pi.

## 7. Lifecycle, migration and rollback

Owner-only `/isolation status|prepare|enable|disable` routes through control; mutations
require confirmation. Preparation and enable/repair hold the runner and namespace
slots. Preparation retires the old key, pauses old group schedules and seeds a
clean unready epoch without copying old state. Interrupted preparation resumes that
same epoch. It never deletes personal/retired data or force-stops legacy foreground
work to make migration appear safe. `/register <id>` for an existing chat uses its
current key; an explicitly different or retired alias remains forbidden.

Enable requires the explicitly qualified configuration and observed group. Disable
binds immediate new-admission revocation to the current scope with a SQL CAS; an
active generation may settle. Configuration files load at controller startup;
editing a file is not a live policy grant. Owner-private runtime behavior stays
separate from group preparation, account provisioning and source refresh.

Each native turn uses immutable image IDs and an effective policy fingerprint.
On completion/cancellation, lease admission closes and active host requests/tools
must drain before namespace release. A timeout retains the draining lease; future
grants and repair remain blocked until actual settlement. Reconciliation checks
native/relay and current/retired tool resources, confirms Docker/network cleanup
and prunes only known current-policy `turn-*` and `auth-ref-*` temporary entries.
Native sessions, workspace, memory, IPC and all retired data are preserved.

Unknown cleanup never means a free slot or permission to replay effects. Rollback
keeps affected groups unavailable and preserves namespace tombstones/epochs. Do
not restore an old permissive group runtime or replace the live database with an
old backup merely to recover availability. Verified backup/restore and exact
runtime release evidence remain separate acceptance gates.

## 8. Module layout

- [Store scopes and migration](../internal/store/namespaces.go), [epochs](../internal/store/namespace_epochs.go).
- [Protected paths](../internal/runner/namespace_paths.go), [native policy](../internal/runner/provider_group.go).
- [Runtime manager](../internal/groupruntime/manager.go), [reconciliation](../internal/groupruntime/reconciliation.go).
- [Offline tools](../internal/groupsandbox/sandbox.go), [MCP leases](../internal/groupsandbox/mcp.go).
- [Exact-route egress](../internal/isolationproxy/README.md), [controller wiring](../cmd/gopod/groups.go).

## 9. Configuration

No group runtime is enabled when `GOPOD_GROUP_CONFIG` is unset. When used, it must
name exactly `${GOPOD_DATA_DIR}/group-runtime.json`, a host-only regular file with
0600 permissions. Example below is **disabled/unqualified**, not a credential or
permission grant. Replace image references and auth-only paths deliberately; set
qualification and one approved sandbox only after exact-image/host checks.
Never put secret values, login URLs or refresh tokens in this JSON or Git.

```json
{
  "EnabledGroups": [],
  "Qualification": "",
  "RelayImage": "gopod-gopod:qualified",
  "ToolImage": "gopod-group-tools:qualified",
  "Providers": {
    "claude": {
      "Image": "gopod-agent-group:qualified",
      "AuthFile": "/absolute/host/auth-only/claude.credentials.json",
      "Routes": [
        {"Host":"api.anthropic.com","Method":"POST","Path":"/v1/messages","Query":"beta=true","Body":"anthropic_messages"}
      ]
    },
    "codex": {
      "Image": "gopod-agent-group-codex:qualified",
      "AuthFile": "/absolute/host/auth-only/codex.auth.json",
      "CatalogFile": "/opt/gopod/data/group-codex-catalog.json",
      "DefaultModel": "gpt-6.1-sol",
      "Routes": [
        {"Host":"chatgpt.com","Method":"POST","Path":"/backend-api/codex/responses","Query":"","Body":"responses"}
      ]
    }
  }
}
```

Codex additionally requires a host-owned model catalog directly in the data
root, never in a chat/workspace/native cache. The controller reads it without
following symlinks, reduces model capabilities and mounts a fresh immutable
snapshot read-only. It validates default and requested model membership before
starting native execution and forces that model on new, resumed and fresh runs.
A native cache, dynamic refresh or unknown-model fallback cannot replace it.

Model metadata is authority in Codex 0.164.0: it can enable patch, code wrappers,
experimental utilities and agents independently of some feature flags. Group
policy v2 removes those capabilities and replaces source prompt templates with
fixed shared-space instructions. Personal native providers and selections do not
use this catalog. The [public metadata artifact](../container/group/policy/README.md)
and [preparer](../cmd/gopod-group-catalog/main.go) contain no owner HOME/cache data.
For the example data root, prepare it explicitly with the trusted host CLI:

```sh
gopod-group-catalog \
  --source container/group/policy/codex-models-0.164.0.json \
  --output /opt/gopod/data/group-codex-catalog.json \
  --default-model gpt-6.1-sol
```

The fresh-group default above is an explicit profile choice, not a change to the
owner's model. The preparer writes `0600` and changes neither its source nor the
owner's account state. This model snapshot does not prove entitlement or quota;
both native-auth inference and direct network denial still need exact-Pi canaries.
Canary configuration and its source catalog must be direct siblings: the canary
stages only sanitized metadata into its disposable data root, preserving explicit
auth-only references without importing any personal state.

The empty qualification is intentionally rejected. Account/refresh/general-service
routes are not supported exceptions. Build instructions and pinned versions are
in [container/README.md](../container/README.md); live auth values remain outside
images, config examples, reports and model-accessible tools.

## 10. Acceptance

Full local tests/races/vet and the real Docker tool checks passed during development.
Synthetic native inventory/HTTP-SSE, scoped local memory, root/symlink, metadata
oracle, interrupted migration and retained-drain tests cover the new boundaries.
CI/Sonar passed and the controller is deployed with groups disabled. Both exact-Pi
native TCP preflights passed. Full provider/tool/new/resume qualification and
real-client acceptance remain pending; this is not an activated group rollout.

The owner currently uses controlled group sandboxes without third-party members.
Check each slice there and fix failures incrementally. A passed prototype or local
fixture does not permit an unqualified real group. Before real-group admission,
record exact revisions/images, credentials' read-only boundary, old-data denial,
private regression, effective network restrictions and verified recovery evidence.

## 11. Sources

Repository source links above are the implementation reference. Earlier research
is dated evidence, not a current deployment assertion. See the [audit](research/2026-10-25-personal-group-isolation-audit.md),
[subscription prototype](research/2026-10-25-subscription-cli-tool-isolation-prototype.md)
and [original accepted contract](superpowers/specs/2026-10-25-personal-group-container-isolation.md).
