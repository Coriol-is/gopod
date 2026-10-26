# Personal and group container isolation — implementation plan

Date: 2026-10-25. Baseline: `ea38581e95ae214ff2a4c0dd5fc8f7f40b1f7d86`.
Status: **P0 containment implemented; PRs 2–6 remain open**. This document
neither changes production nor authorizes billing, account settings or data deletion.

Contract: [isolation specification](../specs/2026-10-25-personal-group-container-isolation.md).
Draft agent policies: [group](../../policies/group-agent.md), [channel](../../policies/channel-agent.md).

Evidence: [memory/filesystem audit](../../research/2026-10-25-isolation-memory-filesystem-audit.md)
and [environment/services audit](../../research/2026-10-25-isolation-env-services-audit.md).
Those reports distinguish confirmed source/synthetic counterexamples from
unverified live account behavior. Passing current SQL scoping tests does not
establish container isolation.

## Required contract

A group owns its memory, neutral history, native sessions, files, IPC and runtime.
It cannot retrieve personal facts through any of those surfaces. Its inference
provider does not grant access to the owner's connected services. No personal
environment, credentials, account configuration or skills are inherited.

An integration exception requires an explicit grant for one group, a dedicated
nonpersonal account/service identity/project, named capabilities and scopes, and
independently scoped secrets. Service brands are not a trust classification:
Gmail, Google Cloud, Drive or another service can contain personal or private
organization data. An allowlisted hostname or a fresh local CLI home does not
prove that an account is appropriate. Owner OAuth must never be copied into a
group as a convenient login fallback.

Profiles are derived from trusted identity and context:

| Profile | Admission | Default authority |
| --- | --- | --- |
| `owner_private` | Configured owner identity in a verified private Telegram context | Explicit personal policy and personal namespace |
| `registered_group` | Registered group/supergroup | Restricted group namespace, inference and scoped memory/IPC only |
| `restricted_private` | Other registered private chat | Its own namespace; restricted baseline until its own policy is approved |

Unknown source/context, inconsistent stored flags, missing policy and zero-value
profile fail closed. Group audience remains public even when the actor is the
owner; actor identity never changes the destination's disclosure policy. Groups
and channels are distinct contexts, not interchangeable roles. A configured owner that is a group is a configuration error, not
permission to expose personal state there. DM login display/delivery never changes
the target group's namespace, runtime tier or credential policy.

Keep the existing Docker runtime, Go architecture, SQLite store, path/capability
authorization and per-chat slot. This work does not require the deferred provider
registry or sandbox framework. Topics remain shared within their chat according
to the existing memory contract; topic-level filesystem isolation is not promised.

## Delivery sequence and review ownership

Deliver six focused PRs. Suggested ownership is responsibility, not authorization
to run parallel writers: assign one implementer and one independent reviewer per
PR. Preserve the separate group-login privacy worktree. Every PR must update its
handoff and ADR/spec references as appropriate, pass CI and receive review before
merge. Do not claim deployment from a merge or from this plan.

### 1. Immediate fail-closed containment (P0)

**Owner:** runner/control implementer; **review:** authorization/filesystem reviewer.
**Dependency:** none; no namespace migration or inference/billing decision needed.

Files: [runner admission](../../../internal/runner/runner.go),
[startup](../../../cmd/gopod/startup.go),
[mounts](../../../internal/runner/mounts.go),
[Docker arguments](../../../internal/runner/docker_args.go),
[main wiring](../../../cmd/gopod/main.go),
[templates](../../../internal/runner/chattmpl/chattmpl.go),
[per-turn policy](../../../internal/runner/policy.go),
[IPC watcher](../../../internal/ipc/watcher.go).

Immediately deny new group agent admission for unqualified legacy profiles,
including foreground, native resume, background extraction, scheduled/system
work, recovery, login and management subprocesses. The only privileged baseline
is the configured owner in trusted private context; inconsistent/unknown context
fails closed. Keep groups unavailable until the later qualification gates pass.
Do not relabel an old OAuth-authenticated, unrestricted group as a strict profile.

Remove group personal vault/project/database/owner-home mounts, global personal
env (including FreeFeed and cross-provider OpenAI key), wildcard private extras
and personal-service skills from builders. Group builders default deny; no owner
credential fallback. Generate group-safe instructions and trusted per-turn
policy, but do not treat instructions as access control. Reject protected-root
ancestor/overlap grants even read-only. Dedicated shared data requires a separate
sanitized dataset and named-group grant.

Close IPC metadata escape now: `processChannel` enumerates an unrooted channel
before rooted content access rejects escape. A symlink can leak outside basenames
through receipts although content remains blocked. Open the channel from its
namespace root with `Root.OpenFile` and `O_NOFOLLOW` (verify directory/platform
semantics), enumerate that handle, and apply rooted operations to dispatch,
archive and retention. Never put outside filenames in group receipts.

P0 delivery boundary: all non-owner inference remains denied, so restricted
seeding is dormant and legacy files are preserved without rewrite. Trusted
versioned policy injection and backed-up legacy-file migration must qualify with
the later group runtime before it can be enabled; no prompt-only boundary is
claimed by P0.

Implement the user-requested privacy-aware base agent-file contract in both
trusted `AGENTS.md` and `CLAUDE.md` profiles, with identical semantics for Codex
and Claude. The group/public profile must:

- Never retrieve private/other-space facts or confirm whether those private facts/data exist;
  do not expose owner persona, private paths, vault names or private metadata.
- Describe a shared group audience rather than treating every participant as
  the operator; the owner speaking in a group does not authorize disclosure.
- Read/write memory only within the trusted namespace; exclude authentication
  secrets from long-term memory and avoid recording them in summaries/history
  output paths. Input containing a secret does not grant permission to retain it.
- Use integrations only through explicit controller-owned per-group grants;
  chat messages and edited workspace files cannot create permissions.
- Route sensitive authentication and diagnostic errors through the approved
  private transport, retaining the group's target identity and authority.

Template edits alone do not protect existing workspaces: current `Seed` writes
only missing files. Supply a versioned, read-only trusted policy on **every**
Claude/Codex turn, including resumed and fresh/system execution. Keep its version
in the effective policy fingerprint. Reviewed migration backs up existing edited
agent files, reconciles group copies with the public contract and preserves
personal customizations. Never overwrite personal files as a group-template
upgrade. Policy text supplements access controls; malicious edited agent files
must not broaden filesystem, memory, credential or network authority.

Define a separate future `channel_publication` policy for explicit outbound
publication context: require explicit publish intent, approved destination and
budget, use public-safe content, and apply the same personal-information and
auth-secret restrictions. This is a future/outbound contract only. Current
`allowedUpdates`/default handler omit `channel_post`; this work neither claims
inbound channel support nor implicitly enables updates, subscriptions or channel
posting. Unknown context fails closed rather than becoming a group or channel.

A reviewed, explicitly authorized rollout must separately drain active group
turns and stop their existing containers under the per-chat agent slot, then
verify no legacy group process remains. Admission blocking alone does not revoke
mounts/credentials from a running process. Do not kill foreground work merely to
apply policy; report a drain timeout and retain closed admission pending operator
resolution. No audit-side stopping, task deletion, credential revocation or
production mutation is authorized by this document.

**Urgency/evidence:** read-only live Docker metadata inspection confirmed an
existing nonowner group runtime has `/workspace/vault` RO and env *key names*
`OPENAI_API_KEY`, `FREEFEED_BASE_URL`, `FREEFEED_USERNAME`, `FREEFEED_PASSWORD`.
No credential values or vault contents were read. The consolidated report owns
the inventory details. This makes containment urgent, but **this plan is not
applied**, and source fixes alone do not remove authority from that runtime.

**Gate:** fake-Docker tests prove every group entry path is denied before exec,
legacy image equality cannot bypass admission, owner-private service remains
correctly scoped, and no foreground turn is killed during controlled drain.
Complete mount/env/skills fixtures with configured vault and wildcard extras
contain no personal grants. Test both new seeding and legacy edited workspaces
for matching Claude/Codex policy on resumed and fresh runs. Owner-in-group input,
personal-fact existence probes, auth-secret memory writes and malicious edited
agent files cannot broaden authority. Unknown/channel inputs cannot enable
unsupported inbound handling or accidental publication. Sensitive error/auth
fixtures select private transport while ordinary group output remains public.
Synthetic IPC outside basenames appear in neither
receipts nor group-visible diagnostics; legitimate IPC still works. Record
rollout process-state evidence separately. These gates qualify containment only,
not full group isolation or permission to re-enable groups.

### 2. Immutable namespaces and host-controlled filesystem roots

**Owner:** store/runner implementer; **review:** authorization/filesystem reviewer.
**Dependency:** PR 1 containment. Record the bounded isolation ADR before identity
migration. Groups remain admission-blocked throughout this work.

Files: [registration store](../../../internal/store/registered_chats.go),
[schema](../../../internal/store/schema.go),
[registration commands](../../../internal/control/chats.go),
[Telegram dispatch](../../../internal/telegram/handler.go),
[runner](../../../internal/runner/runner.go),
[IPC authorization](../../../internal/ipc/authz.go),
[mounts](../../../internal/runner/mounts.go),
[configuration](../../../internal/config/config.go).

Introduce a host-assigned immutable `NamespaceID`, durable identity ownership and
retirement/tombstones. A folder is a display/workspace alias, never transferable
security identity. Resolve profile from configured owner plus trusted chat kind;
reject forged `IsOwner`, reserved `owner`, namespace reuse/reassignment and stale
queue identities. Avoid the present zero-value owner-tier footgun. Carry the
resolved namespace/profile through foreground, background, recovery, scheduler,
provider handoff and login-management paths. Owner-DM login presentation retains
the group's namespace and `TierRegistered`.

Inventory all folder keys: memories/vectors, sessions, neutral history,
messages/turns, tasks, IPC, chat files and CLI homes. Bind legacy data to its
original immutable identity; adapters may retain existing internal columns if
every boundary resolves the immutable ID consistently. Never merge namespaces
based on aliases. Ambiguous ownership blocks that chat for review and preserves
its data; clean-epoch activation remains PR 5.

Move separately mounted memory/scratchpad sources outside every agent-writable
parent. Use controller-owned roots per namespace and validate ownership,
containment and no-follow traversal before seeding and mount assembly. Check all
standard sources, including sessions/IPC/skills, for writable-parent aliases.
Docker must never resolve a source that a live agent can replace. Paused groups
cannot race the P0 maintenance window, but pause is not the permanent filesystem
boundary. A solitary `EvalSymlinks` check is insufficient for eventual re-enabling:
remove the writable-parent race structurally and use rooted/no-follow host
operations. Reject unsafe existing paths without following/copying their targets.

**Gate:** owner/group A/group B/other-private markers prove every compiler slot,
API operation, `/recall`, resume, extraction and continuity excludes foreign data.
Forged empty/owner/thread parameters, stale capabilities, retired/reserved aliases
and queue replay fail before access. Reproduce memory symlinks to dummy owner
memory/DataDir, parent swaps, reuse and concurrent replacement; fail before
seeding, Docker create or outside reads. Test login DM identity preservation and
complete protected-root mount denial. No model calls needed; groups remain off.

### 3. Inference identity, CLI policy and explicitly scoped integrations

Implement the accepted two-set build layout from the spec: personal and group
Dockerfiles, each with Claude/Codex variants. Preserve existing personal image
behavior; do not derive the group final image from the personal final image.
Map `(profile, provider)` to explicit image identities, fail closed on missing
group configuration, and update build/CI/deployment configuration for all four
combinations. Shared non-sensitive stages are allowed. Runtime env/mount/auth/
network enforcement remains required; image separation cannot replace it.
Include personal regression checks and negative group-to-personal fallback tests.

**Owner:** provider/auth implementer; **review:** credential-boundary reviewer.
**Dependency:** PRs 1–2; inference decision blocks credential provisioning and
group re-enablement, never PR 1 containment.

Files: [Claude provider](../../../internal/runner/provider_claude.go),
[Codex provider](../../../internal/runner/provider_codex.go),
[provider contract](../../../internal/runner/provider.go),
[management execution](../../../internal/runner/exec_management.go),
[Telegram login](../../../internal/telegram/login.go),
[container build](../../../container), configuration and runner tests.

Choose and record one supported group inference boundary before implementing
credential provisioning:

| Option | Required proof / decision |
| --- | --- |
| Dedicated integration-free subscription account | Confirm provider-supported use and that readable auth cannot reach personal services; separate account lifecycle and login |
| Explicit group API project/key | Owner approves changed billing, budget and project scopes; do not silently convert subscription use |
| Inference-only broker | Agent never receives upstream personal OAuth/key; broker allows only approved inference routes and no connector/service delegation; assess native CLI compatibility |
| Existing subscription with native CLI / external tool separation | Keep native auth in trusted group-specific runtime, disable all untrusted local file/code/integration authority, expose only scoped external tools; prove exact tool inventory and authenticated new/resume round trips for each CLI. See [prototype evidence](../../research/2026-10-25-subscription-cli-tool-isolation-prototype.md): synthetic inventory and Pi native-subscription new/resume round trips passed for both providers; production/account-service isolation remains unqualified |

Do not call owner-account subscription reuse isolated while Bash can read its
auth and invoke an alternate client. If no choice meets the contract, restricted
group inference remains unavailable; memory/mount fixes can still ship.

Pin the audited clients (Claude Code 2.1.310, Codex 0.164.0) or requalify a changed
version. Assemble fresh controlled CLI state; no owner config/auth restore or
backup import. Supply immutable managed policy across new/resume/fresh-system,
login/auth-check and app-server/management subprocesses. Disable apps,
claude.ai connectors, MCP, plugins, remote plugins and tool/skill dependency
autoinstall by default. Explicit integration grants are controller-owned and
validated by namespace, dedicated identity and capability/scope, with grant
revision available for runtime reconciliation.

Use the precise controls and caveats in the service audit: Codex
`--ignore-user-config` still uses `CODEX_HOME` auth; disable/verify `apps`,
`plugins`, `remote_plugin`, `skill_mcp_dependency_install` and enforced MCP
allowlisting. Claude connector disable plus managed empty MCP is defensible only
with verified pinned semantics; do not combine managed MCP blindly with
`--strict-mcp-config`. Claude `--bare` disables OAuth and is not a subscription
replacement. Safe mode/flags and an empty tool list are defense in depth, not
the credential boundary. Validate second-CLI/config-edit bypasses explicitly.

**Gate:** fake connectors/MCP/apps observe zero initialization/calls for restricted
group start/resume/system/management and alternate CLI execution. Group never
receives personal canary auth/config. Grant tests reject wrong group, identity,
scopes and revoked revision; one approved dedicated fixture works. Missing
inference credentials fail closed without API fallback or model requests.

### 4. Egress enforcement and policy-aware serialized reconciliation

**Owner:** Docker/lifecycle implementer; **review:** network/concurrency reviewer.
**Dependency:** PRs 1–3 for full admission; policy fingerprint may land earlier.

Files: [Docker lifecycle](../../../internal/runner/docker.go),
[Docker arguments](../../../internal/runner/docker_args.go),
[runner startup](../../../internal/runner/startup.go),
[agent slot](../../../internal/runner/runner.go),
[management execution](../../../internal/runner/exec_management.go), container and
deployment configuration.

Enforce group egress outside agent-controlled CLI configuration. Permit only the
specific authenticated memory endpoint, approved inference routes and explicit
group integration routes. Block general host gateway, LAN, loopback aliases,
link-local/metadata, DNS rebinding, IPv6 alternatives, redirects and arbitrary
public services. Remove general `host.docker.internal` authority; the required
memory route must not expose other host ports. Domain-only allowlists are
insufficient when inference and account services share a domain: require
route/method controls or a broker that separates capability. Fail admission if
the target Docker host cannot enforce the selected policy.

Fingerprint the canonical effective profile: immutable namespace and epoch,
policy revision and trusted agent-file policy version, mounts, env names/credential-reference *versions*, skills,
provider/client policy, network policy and image identity. Do not hash or log raw
secrets. Image ID equality alone must not permit reuse. Inspect labels and
actual effective runtime configuration; missing/unverifiable identity denies new
exec and schedules reconciliation.

All stop/remove/create/reconcile paths acquire the existing per-chat agent slot.
Drain or wait for a foreground turn; never kill it merely to update policy, and
never recycle a container while its exec is active. Use bounded cleanup and mark
failure unavailable rather than running with stale authority. Recheck at startup,
recovery, login, provider switch and each admission; policy revocation prevents
new work before recreation. No mass startup recreation outside slots.

**Gate:** with unchanged image, revoked env/mount/grant/network policy cannot be
reused. Concurrency fixtures prove no new work enters stale runtime and no active
foreground is destroyed. Fake Docker and network fixtures verify restricted
allow/deny behavior with positive controls; separately qualify actual Docker
networking without model/auth secrets before production rollout.

### 5. Non-destructive migration and restricted namespace epochs

**Owner:** migration/operator implementer; **review:** data-preservation reviewer.
**Dependency:** PRs 1–4; P0 keeps groups unavailable until all boundaries and
final acceptance qualify their re-enablement.

Files: store migrations, startup admission, documented migration command under
[control](../../../internal/control), [handoff](../../HANDOFF.md),
[acceptance register](../../ACCEPTANCE.md), and a new dated deployment runbook.

Provide a dry-run inventory with identity/profile, path safety, runtime policy
fingerprint, pending tasks and migration eligibility; no private content or
secret values. Back up SQLite and relevant files with permissions preserved and
verify restore into an isolated temporary directory. Old group histories, native
homes, files and memory may already contain personal data. Removing credentials
or a vault mount cannot make them unlearn it.

Preserve old group state in host-only quarantine without following symlinks.
Create a clean restricted namespace epoch and fresh CLI state; retire old native
sessions/capabilities and prevent continuity/extraction from importing old state.
Do not automatically replay old turns or scheduled tasks into the clean epoch.
Review any proposed safe content/task carryover explicitly, then bind it to the
new epoch. Preserve private-owner data and the preexisting task inventory.
Retirement/quarantine is not deletion or proof that prior disclosures were undone.

Keep unsafe groups unavailable until their migration and inference identity are
qualified. Coordinate with the separate group-login privacy hotfix: owner-DM,
opaque one-use confirmation and unchanged group target are required inputs, not
proof that this full boundary is deployed. Do not overwrite that work.

**Gate:** dry run makes no mutations; migration/restore fixtures preserve private
rows/files and retired group evidence and edited-agent-file backups, preserve owner customizations,
survive interruption/restart, reject
symlink traversal and cannot resume contaminated state. No account revocation,
credential rotation, historical deletion or production recreation occurs as an
implicit audit/plan action; those are separate explicit operations.

### 6. Adversarial acceptance, review and staged release

Owner update, 2026-10-26: current group chats are controlled sandboxes with no
third-party participants. Run targeted acceptance with each implementation slice
and fix discovered issues incrementally; do not wait for a separate exhaustive
acceptance campaign before sandbox deployment. Keep independent code review and
CI for code changes. Full cross-boundary acceptance remains required before real
group admission. Synthetic sandbox data must not require personal credentials or
services. P0 disables group inference; this acceleration does not enable an
unqualified group profile.


**Owner:** test/release implementer; **review:** independent end-to-end reviewer.
**Dependency:** PRs 1–5 and recorded inference decision.

Use synthetic owner/group A/group B namespaces and unique canaries for facts,
pins, preferences, summaries, vectors/FTS, vault files, transcripts, CLI config,
env and IPC filenames. Exercise read/write/search, native resume and provider
handoff, scheduled/fresh work, capability forgery, group login retry/DM
confirmation, restart/recovery, symlink swaps, policy downgrade and second CLI
from Bash. Check output, logs, Docker/exec env, process-visible credentials,
receipts and fake outbound endpoints. Include both provider agent-file profiles,
legacy edited policy files, owner speaking to public group audiences, personal
fact existence confirmation attempts, secret retention and future outbound-only
channel intent/destination/budget checks without enabling channel input.
Every deny fixture needs a permitted
positive control so a broken test harness cannot claim isolation.

Run focused package tests for each PR; final gate includes `go test ./...` and
race tests for runner/queue/Telegram/IPC plus meaningful new migration and
network tests. Offline fixture tests require no model calls or real credentials.
A separate sandbox acceptance run must explicitly identify the dedicated test
identity, approved destinations, synthetic data, no-exfiltration checks and any
model/billing budget before making authenticated calls. Do not use production
secrets as test canaries. Test the exact image/client versions intended to deploy.

Release in stages: reviewed green CI -> no-secret Docker qualification -> explicit
sandbox account acceptance -> backup and dry-run -> one approved group canary
under its slot -> inspect effective mounts/env/policy/network and test private
canary denial -> other qualified groups. Record source commit, image identity,
policy fingerprint, migration epoch and live evidence separately. Personal and
other groups continue only under their qualified policy; do not toggle platform
settings or personal service integrations as a side effect.

## Release acceptance and rollback

Release is qualified only when all of these statements have evidence:

1. Trusted identity selects the profile; immutable namespace/epoch scopes every
   memory/history/file/session/task/capability path, including DM-presented login.
2. Group has no personal mounts, env, auth/config, skills or inherited connected
   apps; any integration grant is explicit, dedicated and scoped to that group.
3. Host filesystem operations/mount sources cannot escape through writable-parent
   races or IPC enumeration. Protected paths are not disclosed even read-only.
4. Alternate CLI/custom HTTP clients cannot bypass the credential and network
   boundary; hosted provider connectors are independently disabled.
5. Existing runtimes reconcile effective policy even with unchanged image, under
   the slot, without killing foreground work or resuming contaminated sessions.
6. Migration preserves historical data while fresh epochs exclude it; sandbox
   and live evidence are reported accurately, with unresolved checks left open.

Rollback means keeping affected groups stopped/unavailable while restoring a
compatible reviewed binary/config and preserved backup as necessary. Never
restore the old permissive group policy or mount a quarantined home just to
restore availability. Maintain namespace tombstones/epochs across rollback; if
a previous binary cannot interpret them, block group startup. Owner-only service
may resume only after independently checking its profile and data integrity.
Failed recreation/network enforcement leaves group admission closed. Secret
revocation/rotation and historical deletion require their own explicit operation.

## Decisions required before implementation/release

- Accept the immutable namespace/profile ADR and non-destructive clean-group
  epoch approach; identify any approved content/task carryover.
- Select dedicated subscription identity, explicit API billing/project, a
  compatible inference-only broker or the candidate native CLI/external-tool
  separation design with the existing subscription. No silent subscription-to-API migration.
- Select an enforceable Docker-host egress mechanism and prove exact route
  control where provider inference and account services share infrastructure.
- Name any per-group integrations, dedicated identities and allowed scopes;
  absent grants means disabled. No personal-service exception inferred by brand.

These are bounded security/account/deployment decisions. The plan itself does not
start implementation, make paid requests, change account integrations, revoke
credentials, purge data or claim full isolation from the separate login hotfix.
