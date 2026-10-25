# CP-020: subscription CLI / untrusted tools separation spike

Date: 2026-10-25. Experimental only; not wired into gopod or deployed.

## Architecture

```text
trusted controller -> group-specific native CLI session (subscription auth)
                            |
                   only approved MCP tool
                            |
                 trusted stdio tool_server.py
                            |
                   disposable tool container
                   group workspace only; no auth/network
```

The native CLI retains supported subscription login rather than translating an
OAuth token into an undocumented API. This is a candidate architecture, not
proof of provider permission for every multi-user deployment or entitlement.
Quotas remain tied to the account. Personal CLI sessions/config/history must
never be shared with group sessions. No credentials are copied by this spike.

The Docker socket belongs only to the trusted adapter. Model input cannot select
container/image/mount/env/namespace or Docker arguments; it supplies shell text
to a fixed workspace. The adapter accepts newline-delimited stdio MCP requests
and exposes just `group_exec`. There is no listening service or public endpoint.
The tool container has no CLI, credentials, network, host gateway or Docker socket.
It has a non-root user, read-only root, dropped capabilities, resource/output/time
limits and one workspace. Networked fetching is deliberately unavailable.

**Do not run the native CLI directly on the production controller host for this
experiment.** Its tools/config/hooks/automatic context loading must first be
proven incapable of reading the host or authorization state. A production design
needs a separate trusted CLI runtime with minimal filesystem visibility and
managed policy. Built-in file reads are as sensitive as shell execution.

## Reproduce without credentials or inference

Prerequisites: Python 3, Docker, a preloaded trusted `node:22-slim` image. Tests
never pull images, authenticate a provider, read actual auth stores or call models.

```sh
GOPOD_ISOLATION_DOCKER_TEST=1 PYTHONDONTWRITEBYTECODE=1 \
  python3 -m unittest discover -s experiments/subscription-isolation -v
```

Override `GOPOD_ISOLATION_TEST_IMAGE` with a trusted local node image digest for
repeatable qualification. Do not use a personal agent image containing secrets or
unexpected tool customizations. The workspace must be a synthetic temporary
directory, never a production chat or a parent of personal state.

To connect a future controlled MCP client, configure its stdio server command as
`python3 /absolute/path/tool_server.py --workspace /absolute/synthetic/group
--image <trusted-local-image-digest>`. All parameters are operator-controlled.
Do not expose this adapter as a generally callable host execution utility.

## Provider compatibility: evidence and remaining gates

Locally inspected CLI versions: Claude Code **2.1.310**, Codex **0.164.0**.

| Provider | Candidate controls | Evidence boundary |
| --- | --- | --- |
| Claude | `--restricted`, `--tools ""`; explicitly selected MCP only; `disableClaudeAiConnectors=true`; controlled settings/hooks/plugins and MCP tool allowlist | Pinned help and official docs confirm the controls; no authenticated tool-inventory or model run performed |
| Codex | Managed feature restrictions for shell/apps/plugins/remote plugins/skill installation/hooks/subagents; approved MCP only; no local writable auth/config access through tools | Pinned feature inventory confirms flags exist; **not** proof that disabling shell removes every file/code tool. `unified_exec=false` alone can leave one-shot execution. No authenticated inventory or model run performed |

Claude's `--allowedTools` means permission without prompting, not a complete tool
allowlist; use `--tools` for built-ins. `--bare` is not the subscription solution.
Do not blindly mix `--strict-mcp-config` with managed-MCP deployments; qualify the
exact configuration. For Codex, `--ignore-user-config` still loads authentication
from CODEX_HOME and does not by itself disable integrations or project policy.

Before admitting real group messages:

1. Prove exact available tool inventory for both providers (new and resumed
   sessions), including file reads, patch tools, search, subagents, hooks,
   plugins, browser, computer use and provider-hosted connectors. Anything able
   to retrieve auth or run local code must be absent or externally constrained.
2. Prove missing/failed MCP and changed policy fail closed, with no local-shell
   fallback. Resume must not restore old permissive tools/policy/context.
3. Use a designated canary account/session to prove actual subscription auth,
   model inference, MCP round trip and hostile tool requests. Record observed
   tools and pass/fail only, never tokens or personal account data.
4. Implement a controller-owned authorization/runtime boundary. The experimental
   path check is **not** production rooted/no-follow IO: an agent-writable host
   parent could be swapped. Bind sources must live under protected host roots.
5. Add per-chat serialization, cancellation/disconnect cleanup, quotas, durable
   lifecycle reconciliation and namespace-bound grants. Current stdio prototype
   is single-client, sequential, disposable and not a replacement for Runner.
6. Qualify Docker/CLI behavior on the Pi and enforce CLI runtime egress, including
   hosted connectors. Tool-container `network=none` does not govern CLI traffic.

If either CLI cannot exclude all local authority paths, keep the affected group
provider closed; do not silently fall back to personal runtime or paid API.

## Recorded result

Local Linux/aarch64 Docker, 2026-10-25: **6 tests passed in 7.065s** against
preloaded `node:22-slim`. Positive workspace write/read, synthetic environment
non-inheritance, sibling symlink escape denial, missing auth/vault/socket/CLI,
read-only system filesystem, loopback-only networking, fixed tool arguments,
MCP initialization/call, timeout/output bound and container cleanup passed.
These are synthetic Docker/MCP transport results, not subscription E2E evidence.
Production configuration, personal chat, accounts and Pi runtime were unchanged.
Observed tool image ID:
`sha256:7323ec8ada5c53c30d18c1d92134274097efa099b4ecb810d65a613a32da973e`.

Security Review Progress:
- [x] Step 1: Map entry points and trust boundaries (stdio model arguments -> fixed Docker tool sandbox).
- [x] Step 2: Load the references this code needs (OWASP Security skill and official provider controls).
- [x] Step 3: Sweep for candidate issues (credential inheritance, local tools, mounts, networking, output and lifecycle).
- [x] Step 4: Triage every candidate (synthetic proofs above; production path/lifecycle and authenticated tool inventory remain gates).
- [x] Step 5: Report findings (experimental-only boundary and explicit release gates above).

## Official references

- [Claude CLI controls](https://code.claude.com/docs/en/cli-reference)
- [Claude account connectors](https://code.claude.com/docs/en/mcp#disable-claude-ai-connectors)
- [Codex configuration and managed requirements](https://learn.chatgpt.com/docs/config-file/config-reference)
