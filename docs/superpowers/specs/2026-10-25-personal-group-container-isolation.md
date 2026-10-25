# Personal and shared-container isolation contract

Date: 2026-10-25. CP-020. Status: accepted requirements and reviewed plan;
implementation of the broad isolation boundary has not started.

[Audit](../../research/2026-10-25-personal-group-isolation-audit.md) and
[implementation plan](../plans/2026-10-25-personal-group-container-isolation.md)
are the evidence and execution artifacts. The separate private-login hotfix
closes transport/input-retention problems, not all container/service exposure.

## Required behavior

1. Derive an explicit profile from trusted chat identity/type and approved policy.
   Owner-private, registered-group and restricted-private are distinct; unknown
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
Choice of dedicated subscription identity, API project/key or inference broker
requires an explicit supported account/budget decision; no silent paid migration.

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
