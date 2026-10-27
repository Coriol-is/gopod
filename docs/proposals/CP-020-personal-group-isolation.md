# CP-020 — Personal and group container isolation

| | |
|---|---|
| Status | implemented; rollout blocked |
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

P0 remains the deployed baseline (`c82399b`). Local full tests/race/vet, real
Docker tool checks, CI/Sonar and independent review passed. Pi offline tools
passed (6.22 s); earlier-candidate Claude actual new/resume two-turn file/context/session checks
passed. Final latest-v2 both-provider/native-network qualification and rollout remain
open; see [draft release evidence](../deployments/2026-10-27-group-isolation.md). No full
runtime deployment or group re-enablement is claimed here. Owner-private behavior
remains the baseline; group speech and automatic maintenance remain unavailable.

Native policy v2 qualification passed 69 local cases across all 11 public catalog
models, including new/resume and hostile-capability paths. ARM64 release images
were built locally from merged main; production remains untouched. The sanitized
public catalog and explicit `gpt-6.1-sol` default are documented in the
[catalog provenance](../../container/group/policy/README.md).

[Current architecture, disabled configuration example and limits](../ISOLATION.md)
record implementation behavior. [Earlier native-subscription prototype](../research/2026-10-25-subscription-cli-tool-isolation-prototype.md)
canaries are historical evidence, not acceptance of this new runtime. Incremental
checks use controlled sandboxes; no real-group rollout or T9 reboot is implied.

## Out of scope
Silently changing billing, deleting historical data, revoking accounts, enabling
channel/Guest transport or claiming the private-login hotfix is full isolation.

## Acceptance
Adversarial canary tests prove both cross-namespace data denial and effective
filesystem/credential/network boundaries. Existing data is preserved/quarantined;
policy changes apply safely to reused runtimes. Actual release evidence remains
separate from design and synthetic fixtures.
