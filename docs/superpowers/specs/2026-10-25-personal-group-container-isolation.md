# Personal and shared-container isolation contract

Date: 2026-10-25. Implementation update: 2026-10-27. CP-020 remains **in-progress**.
The full boundary is implemented on the working branch. Final review/CI and exact
RPi Docker/native-subscription qualification remain release gates; this document
does not claim that the full runtime is deployed. The recorded production baseline
is still the separate [P0 rollout](../../deployments/2026-10-26-isolation-p0.md).

[Audit](../../research/2026-10-25-personal-group-isolation-audit.md) and
[implementation plan](../plans/2026-10-25-personal-group-container-isolation.md)
are the evidence and execution artifacts. The separate private-login hotfix
closes transport/input-retention problems, not all container/service exposure.

## Implemented architecture and present limits

The selected subscription-preserving design separates a trusted native CLI from
untrusted commands. [Current policy and configuration](../../ISOLATION.md) is the
implementation reference; the original requirements below remain the acceptance
contract. A registered group does not automatically become eligible to run.

- Store assigns one UUID namespace, epoch and registration revision. Permanent
  ownership records reserve current and retired storage keys. Scope snapshots,
  readiness and admission flags use CAS; changing an alias cannot transfer data.
- Explicit preparation creates a clean epoch with separate protected workspace,
  memory, IPC, native-state and policy directories. Old rows/files stay preserved
  under retired keys; old schedules are paused, not imported. Interrupted prepare
  resumes the same unready epoch. No historical session or content carryover is
  automatic. Owner-private paths and behavior remain unchanged.
- Personal and group Claude/Codex image sets are separate. Group native execution
  is ephemeral per turn, with pinned CLI policy, fresh inference directory and
  group-only native history. It never mounts actual group files or personal HOME.
  An explicit **auth-only** source inode is pinned with a temporary host-only
  hardlink and mounted read-only into trusted inference only. Untrusted tools
  never receive it. This shares the existing subscription/quota; it does not
  create an independent account, paid API fallback or account-service grant.
- The native CLI's only egress is an isolated-network relay enforcing exact
  inference host/method/path/query and request-tool inventories. Provider apps,
  connectors, hooks, plugins, native file/code tools and auth-refresh routes are
  unavailable. Expired auth fails closed until private account management refreshes
  the source. Codex retains native ChatGPT auth with qualified HTTPS/SSE.
- Untrusted commands run in disposable non-root `network=none` containers with no
  host bind mounts. Regular-file snapshots are bounded to 8 MiB total/4 MiB per
  file; commands have 20 seconds, 32 MiB per-process address space, a verified
  kernel PID ceiling of eight and 32 KiB output. Background processes prevent
  successful export; arbitrary background services are unsupported.
- Host-owned `group_request` supplies scoped local SQL/FTS memory and validated
  IPC only. No personal embedding, ASR or TTS service is inherited. Group speech
  input/replies are unavailable; photo/document uploads use protected paths.
  Automatic group extraction/compaction remains unavailable pending CP-006.
- Owner-only `/isolation status|prepare|enable|disable` controls explicit state;
  mutations require confirmation. Missing configuration/images, an unobserved
  group type, stale scope, unsafe roots, unknown cleanup or unqualified policy
  denies admission. No channel, Guest or other-private profile is enabled.
- Reconciliation holds namespace/runner slots, drains retained host callbacks,
  sweeps owned native/relay/tool orphans over all reserved keys and proves Docker
  settlement before current-policy temp GC. A timeout never frees authority by
  assumption. Native/workspace/memory/IPC and retired data are preserved.

The owner authorized incremental checks in controlled group sandboxes, currently
without third-party participants. Local full tests/race/vet and the real Docker
untrusted-tool checks passed during implementation. Those are not evidence of
an authenticated exact-Pi run, deployed private/group isolation or user acceptance.
No T9 memory-cgroup change or host reboot is part of this work. Real groups still
require the complete cross-boundary acceptance gates below.

## Required behavior

### Build and runtime profiles (owner decision)

Provide two Dockerfile sets and two runtime profiles: `personal` and `group`.
Keep the personal image contents and personal-chat behavior unchanged by this
split; shared correctness/security fixes may still apply to both. Each set has
Claude and Codex variants, preserving provider choice independently of profile:
`container/personal/Dockerfile{,.codex}` and
`container/group/Dockerfile{,.codex}`. The controller image also builds the
small group relay entrypoint; the group tool image is an additional credential-free
auxiliary image (`container/group/Dockerfile.tools`).
Shared non-sensitive build stages/scripts may be reused, but the group build
must not inherit the personal final image, private tools/configuration or secrets.
Do not bake credentials into either image.

Resolve images by `(profile, provider)` with separate tags/digests. The trusted
controller selects the profile from chat registration/type; threads inherit it.
Missing group images/configuration fail closed, never fall back to personal.
Apply profile-specific mounts, env, credentials, instructions and network policy
at runtime as well: separate Dockerfiles alone are not an isolation boundary.
Test all four image/profile/provider combinations, personal behavior preservation,
group denial and same-image policy reconciliation. Unknown/private non-owner
contexts remain denied until explicitly qualified; this does not add a third
container profile or enable channel transport.

### Enforcement

1. Derive an explicit profile from trusted chat identity/type and approved policy.
   Owner-private and registered-group select different profiles; unknown
   or inconsistent context fails closed. A configured owner group cannot inherit
   owner-private rights. Future channel publication needs its own grant/profile;
   incoming channel_post handling is not implicitly enabled.
2. Bind memory, history, sessions, files, IPC and credentials to an immutable
   namespace/epoch. Display folder aliases cannot transfer authority or cause a
   group to inherit retired personal state. Threads retain their existing
   within-chat sharing contract; cross-chat sharing is never implicit.
3. Group agents cannot retrieve personal facts through SQL/API, host files,
   vaults, shared skills/configuration, connectors or alternate CLI/HTTP clients.
   Read-only access is still disclosure. Capabilities bind scope server-side;
   request text cannot change it.
4. Group profile defaults deny personal mounts, shared host env, private skills,
   account connectors/apps/MCP/plugins and privilege through configuration files.
   Separate inference authority from service authority. Never fall back to the
   owner's OAuth/API key because a group credential is absent.
5. Exceptions require explicit group/identity/capability/scope grants with dedicated
   nonpersonal credentials and revision/revocation. Gmail/Cloud/Drive are not
   intrinsically safe categories. Do not infer any grant from a group message.
6. Enforce the boundary outside agent-controlled instruction files. Protected host
   roots cannot be replaced via agent-writable parents; host filesystem and IPC
   enumeration use rooted/no-follow access. Credentials/network routes must not
   allow the agent to bypass CLI disable flags.
7. Compare effective policy fingerprints as well as image identity on runtime
   reuse. Policy revocation denies new work; reconciliation is serialized under
   the chat slot and does not silently kill foreground work.
8. Group login can display in an owner DM only after target-bound private consent.
   Runtime/profile/memory remain group-scoped. No copying of owner homes/auth.
   Login URLs/codes/raw output and credential quotes are not public or memory.
9. Use matching trusted privacy policies for AGENTS.md and CLAUDE.md. Treat group
   replies as shared even when the owner speaks; do not reveal or confirm private
   facts/metadata. Templates alone do not update legacy workspaces: versioned
   runtime policy covers new, resumed, fresh and edited-file paths.
10. Preserve historical data non-destructively. Existing group state may already
    contain private material; quarantine/clean epochs must not automatically import
    it. Audit/plan does not delete records, rotate/revoke credentials or change
    billing/platform integrations. Old permissive group authority is not a safe
    rollback target.

Draft instruction contracts:
[group](../../policies/group-agent.md),
[channel](../../policies/channel-agent.md). They are not production enforcement.

## Release boundaries

P0 containment must not wait for a namespace rewrite or API-billing decision.
Unqualified group execution stays closed until the required boundary is proven.
The selected native-auth separation uses only explicitly configured read-only
auth references and exact inference routes. A future dedicated account, API key,
refresh capability or integration requires its own account/budget decision; no
silent paid migration or account-setting change is included.

The implemented selection preserves the existing subscription: keep the native
authenticated CLI in a trusted group-specific runtime and expose only scoped
tools that execute outside its credential boundary. The [offline spike](../../../experiments/subscription-isolation/README.md)
proves synthetic tool-container isolation. Follow-up [Pi canaries](../../research/2026-10-25-subscription-cli-tool-isolation-prototype.md)
also passed native subscription new/resume round trips for both pinned providers;
they do not qualify the new production implementation or its exact-Pi network boundary.
All native file/code/integration paths must be excluded or externally constrained,
including on resume; readable auth plus unrestricted native tools is not accepted.

Acceptance uses isolated owner/group A/group B/private canaries, positive allowed
controls, attempted path/config/CLI/policy bypasses and actual Docker enforcement.
A passed prompt-only test or a scoped SQL search is insufficient. Real accounts,
model calls and production migrations need their own declared scope/budget.

The six-PR plan defines dependencies, ownership, migration, release and rollback.
Channel transport, automatic group publishing, arbitrary integrations, CP-018
per-topic providers and a generic sandbox/plugin framework are outside this
isolation change unless separately scoped and approved.

## Deferred one-way personal access

[Future design note](../../research/2026-10-25-one-way-private-group-access.md)
records possible private-agent read grants to selected group data. It is not
a symmetric namespace share or publishing grant and does not expand P0 scope.
