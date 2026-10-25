# CP-020: native subscriptions with isolated group tools

2026-10-25. Working experimental prototype; production group enforcement is not
implemented. [Code and reproduction](../../experiments/subscription-isolation/README.md).

## Decision supported by the experiment

Keeping the existing personal subscription is technically feasible without
letting group shell commands access the subscription credential. Retain the
native provider CLI in a minimal trusted group-specific runtime, disable its
local execution/file/integration tools, and expose only a controller-bound MCP
tool. That tool runs commands in a separate disposable Docker container with
only the synthetic group workspace and no network or credentials.

There are still two authority profiles, personal and group. The group profile
has two execution roles: trusted authenticated CLI and untrusted command worker.
The personal profile keeps its existing behavior. A worker image can be shared
between group providers without sharing group namespaces, credentials or files.
Separate Dockerfile sets remain the accepted production build design.

This uses supported native CLI authentication; it does not extract OAuth tokens
into a custom inference API, create a new paid API project or prove permission
for every possible multi-user/account arrangement. Account quotas remain shared.

## Observed evidence

| Check | Claude Code 2.1.310 | Codex 0.164.0 |
| --- | --- | --- |
| Actual CLI + synthetic localhost model API | Pass | Pass |
| Scoped MCP to real Docker command execution | Pass | Pass, stdio and HTTP |
| Synthetic provider-request inventory | Only group MCP tool | Exact allowed control/resource functions + group MCP namespace |
| Synthetic request to disabled native execution | Built-ins absent from request | Explicit `exec_command` request rejected; bypass marker absent |
| Native subscription on Linux/arm64 Pi: new session | Pass | Pass |
| Native subscription on Pi: resume same session | Pass | Pass |
| Live init tool inventory | Observed and restricted in both turns | Not independently exposed by this probe |
| Full production/account-service isolation | Not qualified | Not qualified |

The authenticated canaries mounted only the existing native auth **file**
read-only, not personal HOME, history, vault, account integration configuration,
project files or environment credentials. CLI HOME/config/session state was
fresh ephemeral storage, with fixed read-only policy. No API-key fallback was
provided. Each successful turn executed the expected command through MCP,
returned exit code 0 and the synthetic marker, and produced an exact bounded
regular proof file. New/resume used the same disposable CLI session state.
Only statuses, fixed diagnostic labels and canary outcomes were reported.

Separate native `--auth-only` checks confirmed the methods `claude.ai` and
`chatgpt`, both with `subscription_auth_verified=true`; these checks did not
invoke inference or expose account identity fields. The final helper checks
this method before inference and rejects API-key, unknown or failed auth.

Final local suite: **49 tests passed in 19.442s**, with real Docker and both
pinned native CLIs against localhost mock providers; no tests were skipped.
The native subscription checks above are separate Pi evidence, not CI tests.

Claude canary model: `claude-haiku-4-5`. Codex used the account/CLI default model;
its exact model identity was not captured, so this evidence cannot certify all
models. Claude's live inventory showed only the approved MCP tool and no
unexpected tools. Codex's live executed calls were scoped MCP calls, but an
executed-call trace is not a complete available-tool inventory.

Tool image on Pi:
`node@sha256:de170b39439eb0e5a3444ad7d60b015614a381e10fecae2e30ec9e735122f38d`.
CLI images were the pre-existing pinned 2.1.310/0.164.0 qualification images.
No existing production controller/agent was restarted or reconfigured for the
canaries; temporary runtimes, bridge and workspaces were separate.

## Compatibility fix

The first real Codex calls reached MCP but used the field `cmd`, while the first
experimental schema expected `command`. The native process returned exit code 0
even though no command ran, so process success alone was correctly rejected.
Safe argument-shape diagnostics identified the mismatch without logging values.

The external API is now exactly `{"cmd":"..."}`. The trusted adapter validates
that single nonempty bounded string, rejects aliases/extra fields, and maps it
to the internal Executor's `command` field. Both synthetic fixtures use the
same schema. No authority or sandbox restriction was relaxed to obtain success.

## Hardening included

- Fixed image/workspace/namespace outside model-controlled arguments; no socket
  or provider auth mount inside the command worker.
- Non-root, read-only root, dropped capabilities, no-new-privileges, network none,
  PID/memory/CPU/output/time limits for commands.
- Acknowledged create before start; graceful SIGTERM/SIGINT cancellation and
  request-specific removal. Cleanup tests do not target other runs' containers.
- Capability-protected HTTP MCP, strict JSON/body/Host/Origin checks, bounded
  connection workers, no request/URL/response-text logs and safe JSON responses.
- Bounded no-follow proof verification: symlinks, FIFOs, hardlinks and oversized
  canary files are rejected rather than read on the host.
- Bounded live probe: at most two CLI turns, per-turn deadline and eight actual
  external executions. Authentication values never enter reports.
- Offline qualification in CI with pinned CLIs and tool image; no model account
  or external inference is used by CI.

## Required before production

1. Implement the accepted personal/group profile and image mapping in Runner;
   deny fallbacks to personal images, mounts, env, state or policy on all paths.
2. Treat the authenticated CLI as trusted infrastructure, not an arbitrary group
   shell environment. Enforce immutable policy/inventory checks, including exact
   account model, new/resume and old-session migration. Adversarial service access
   under real login remains untested; do not equate flags with account scopes.
3. Add protected host namespace roots and explicit scoped integration grants.
   Address credential refresh/revocation without exposing tokens to workers or
   racing shared personal auth state. Read-only canary mounts do not solve refresh.
4. Add actual workspace storage quotas and external reconciliation after
   SIGKILL, host/Docker failure and controller restart. Timeout/output/tmpfs limits
   do not impose a quota on the writable bind workspace.
5. Enforce trusted CLI egress, authenticated transport and lifecycle. The current
   HTTP bridge is an ephemeral same-host/Docker canary transport, not a public
   production endpoint. Capability URLs must not be logged or exposed.
6. Complete independent review and all CI checks before merge/release. Prototype
   merge does not activate isolation for existing registered groups.

The existing six-PR [implementation plan](../superpowers/plans/2026-10-25-personal-group-container-isolation.md)
still starts with P0 containment; these results remove the assumption that a new
account or paid API is the only possible inference design.
