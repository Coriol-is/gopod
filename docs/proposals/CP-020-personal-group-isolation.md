# CP-020 — Personal and group container isolation

| | |
|---|---|
| Status | implemented; controller deployed, final provider/client qualification open |
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
The full implementation merged to main in PRs #66/#67
(`a3a9c53a24456cce40678231ef048e6baba51956`) separates personal/group Claude and Codex
images, immutable namespace epochs, protected roots, trusted native inference,
credential-free offline tools and exact-route network egress. Existing native
subscription auth is an explicit read-only auth-only reference; group execution
cannot refresh it or inherit personal HOME, connectors, env or services.

The controller is deployed as `e3b5401`; see [current deployment evidence](../deployments/2026-10-31-security-wave.md).
Follow-ups #88/#89 fixed Codex tree-budget admission and enabled default admission for
registered groups (D047); #93/#95/#97 added group controls and private login guidance.
Historical 2026-10-29 evidence records existing-group preparation and Claude new/resume success
following owner login. Final Codex two-turn/tool and cross-boundary/client qualification remain open.

Local tests/races/vet, Docker tool checks, CI and independent review passed. Earlier native policy
v2 qualification covered 69 cases across 11 catalog models; Pi TCP/offline-tool results remain
[historical evidence](../deployments/2026-10-27-group-isolation.md), not proof of final live acceptance.
Use [current policy and limits](../ISOLATION.md) and [handoff](../HANDOFF.md) for operational state.
Group speech and automatic maintenance remain unavailable. No T9 reboot or additional group
activation was performed by the security rollout or this documentation housekeeping.

## Out of scope
Silently changing billing, deleting historical data, revoking accounts, enabling
channel/Guest transport or claiming the private-login hotfix is full isolation.

## Acceptance
Adversarial canary tests prove both cross-namespace data denial and effective
filesystem/credential/network boundaries. Existing data is preserved/quarantined;
policy changes apply safely to reused runtimes. Actual release evidence remains
separate from design and synthetic fixtures.
