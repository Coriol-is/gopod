# CP-020: subscription CLI / untrusted tools separation spike

Date: 2026-10-25. Experimental only; not wired into gopod's production Runner.
Disposable subscription qualification has run on the Pi; this does not enable
the profile for existing group chats.

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
and exposes just `group_exec`. Stdio needs no listener. The optional HTTP bridge
used for Pi canaries binds explicitly to all host interfaces for Docker gateway
access, with a secret capability and bounded admission. This is temporary
experimental transport, not a production endpoint.
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
GOPOD_ISOLATION_DOCKER_TEST=1 GOPOD_CODEX_OFFLINE_TEST=1 \
  GOPOD_CLAUDE_OFFLINE_TEST=1 PYTHONDONTWRITEBYTECODE=1 \
  python3 -m unittest discover -s experiments/subscription-isolation -p '*test*.py' -v
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
| Claude | `--restricted`, `--tools ""`; explicitly selected MCP only; `disableClaudeAiConnectors=true`; controlled settings/hooks/plugins and MCP tool allowlist | Real pinned CLI with local synthetic API advertises only `mcp__group__group_exec` and completes Docker round trip. Native subscription on Pi completes new/resume round trips; hostile/account-service tests remain gates |
| Codex | Shell/apps/plugins/remote plugins/skill installation/hooks/subagents disabled; read-only native sandbox; sole approved external MCP | Real pinned CLI with local synthetic API advertises only explicitly allowed control/resource functions and scoped MCP; forbidden native exec is rejected. Stdio/HTTP and Pi subscription new/resume round trips pass; live complete tool inventory remains unproven |

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
   tools and pass/fail only, never tokens or personal account data. Both providers'
   benign round trips have passed; full adversarial qualification has not.
4. Implement a controller-owned authorization/runtime boundary. The experimental
   path check is **not** production rooted/no-follow IO: an agent-writable host
   parent could be swapped. Bind sources must live under protected host roots.
5. Add per-chat serialization, cancellation/disconnect cleanup, quotas, durable
   lifecycle reconciliation and namespace-bound grants. Current stdio prototype
   is single-client, sequential, disposable and not a replacement for Runner.
   Graceful SIGTERM/SIGINT cleanup is tested; SIGKILL/daemon failure still needs
   external reconciliation. RW bind workspace storage is not quota-limited.
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
These original six tests used no accounts and made no Pi changes. Later native
subscription canaries used separate disposable runtimes, described below.
Observed tool image ID:
`sha256:7323ec8ada5c53c30d18c1d92134274097efa099b4ecb810d65a613a32da973e`.

Follow-up adds real native CLI/mock-provider tests, exact Codex inventory
allowlisting, hostile native function rejection, missing required MCP behavior,
HTTP capability/body/connection admission tests, graceful cancellation and
bounded/no-follow canary evidence. CI runs the offline suite without login or
external inference using pinned CLI versions and a pinned tool-image digest.

### Pi subscription canaries

`live_probe.py` is opt-in: the operator supplies one native auth-file path and
trusted image names. It mounts the auth file **read-only**, not the containing
personal directory, with fresh tmpfs CLI home/workspace and fixed read-only policy.
It does not copy/export credentials or supply API keys. Native CLI authentication
may contact the account/provider; this is not an offline test. No real group
messages, personal session history, vault or integration configuration are used.
The current probe performs at most two CLI turns (new/resume), 90 seconds each,
with a total eight external-tool execution ceiling. Metadata excludes command
arguments, output, response text, endpoint capabilities and credential values.

Native auth-only checks confirmed `claude.ai` and `chatgpt`; the helper now
requires a verified subscription method before inference. Use `--auth-only`
with the same operator-supplied parameters to inspect only these fixed method
enums without a model request. API-key and unknown auth are rejected.

The final local suite passed **49 tests in 19.442s** with all opt-ins enabled.

Claude Code 2.1.310 / `claude-haiku-4-5` and Codex 0.164.0 / account-default model:
new and resumed subscription sessions both wrote/read the synthetic proof via
MCP on Pi. No fallback to native tools or API billing was enabled. The canonical
external schema is exactly `{"cmd":"..."}`, mapped after strict validation to
the internal Executor `command` field. See the [qualification report](../../docs/research/2026-10-25-subscription-cli-tool-isolation-prototype.md)
for the real Codex schema mismatch and evidence boundaries.

Round-trip success is deliberately separate from `isolation_qualified=false`:
production hostile inputs, old sessions, account-level service denial, enforced
egress, protected namespace roots, disk quotas and forced-death reconciliation
still need their own evidence. No production chat profile has been switched.

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
