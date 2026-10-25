# CP-020 — Personal and group container isolation

| | |
|---|---|
| Status | accepted |
| Date | 2026-10-25 |
| Origin | Owner requested deep isolation audit and plan for group/private memory, env, connected services and public-space agent instructions |
| Size | L |
| Touches | Runner profiles/mounts/env, memory namespaces, provider policy, IPC, credentials, lifecycle and migration |
| Spec | [Isolation contract](../superpowers/specs/2026-10-25-personal-group-container-isolation.md) |
| Plan | [Six-PR plan](../superpowers/plans/2026-10-25-personal-group-container-isolation.md) |
| Audit | [Consolidated findings](../research/2026-10-25-personal-group-isolation-audit.md) |

## Problem
Existing memory API scoping does not prevent personal data/service access through
vault mounts, global credentials, mutable host bind sources or account-level
connectors. Running containers can retain authority after policy changes.

## Proposal
Implement the accepted isolation contract in reviewed slices, beginning with P0
containment independent of billing/namespace migration. Group integrations are
explicit dedicated scoped grants, not inherited personal services. Planning and
requirements are accepted; broad implementation has not started.

Owner decision: two Dockerfile sets (`personal`, `group`) and two runtime profiles,
each independently supporting Claude and Codex. Preserve personal-chat behavior;
restrict group images and runtime authority. See the spec for the build matrix
and fail-closed image selection contract.

## Out of scope
Silently changing billing, deleting historical data, revoking accounts, enabling
channel/Guest transport or claiming the private-login hotfix is full isolation.

## Acceptance
Adversarial canary tests prove both cross-namespace data denial and effective
filesystem/credential/network boundaries. Existing data is preserved/quarantined;
policy changes apply safely to reused runtimes. Actual release evidence remains
separate from design and synthetic fixtures.
