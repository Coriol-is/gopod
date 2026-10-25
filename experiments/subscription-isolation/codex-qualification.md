# Codex 0.164.0: native CLI / external tools qualification

2026-10-25. Offline experiment; no production configuration or account state changed.

## Result

The pinned native Codex binary completed a real stdio MCP round trip to the
Docker-backed `group_exec` adapter with local execution disabled and read-only
sandbox selected. The provider was a synthetic localhost Responses server, not
OpenAI; there were no authentication headers or external inference requests.

Four opt-in tests passed in **5.015s**:

- Capture actual provider-request tool inventory: no native shell, patch, file,
  image, web, browser, computer or subagent tools advertised in this fixture.
- A synthetic provider attempted an unadvertised native `exec_command`; Codex
  returned a function result without executing it (workspace marker absent).
- Required MCP startup failure stopped execution before any provider request;
  no fallback to native tools was observed.
- A synthetic provider requested `mcp__group.group_exec`; `printf codex-mcp-ok`
  ran in the disposable restricted Docker container and its output reached the
  next provider request. The native CLI itself remained `read-only` / `never`.

The exact observed tool inventory with MCP enabled was:

```text
list_mcp_resources
list_mcp_resource_templates
read_mcp_resource
request_user_input
mcp__group { group_exec }
get_goal
create_goal
update_goal
```

The MCP resource tools can access only configured servers; do not add personal
servers to this profile. In this spike, the group server advertises only tools,
not resources. Goal/input tools are native internal control functions, not shell
or filesystem read tools. This is evidence for the tested model/tool configuration
(`gpt-5` with the built-in model catalog), not a universal inventory guarantee
across future models, versions, login state, resume or platform-managed policy.

## Reproduce

```sh
GOPOD_CODEX_OFFLINE_TEST=1 GOPOD_ISOLATION_DOCKER_TEST=1 \
  PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s experiments/subscription-isolation -p codex_test.py -v
```

`codex_probe.py --mcp` emits only synthetic tool/config metadata and outcomes.
`--native-attack` tests rejection of a forbidden native function;
`--missing-mcp` tests required-server admission failure. All create disposable
synthetic HOME/CODEX_HOME/work directories and replace the provider with localhost.
No actual auth file is read, copied, printed or used by these tests.

## Configuration findings

The candidate disables `shell_tool`, `shell_snapshot`, `unified_exec`, `apps`,
`plugins`, `remote_plugin`, `plugin_sharing`, `hooks`, `multi_agent`,
`multi_agent_v2`, `skill_mcp_dependency_install`, `skill_search`, `browser_use`,
`browser_use_external`, `computer_use`, `image_generation` and `view_image` under
`[features]`. It also disables web search and default apps, selects `read-only`
sandbox with `approval_policy = "never"`, and configures exactly one required MCP
server with `enabled_tools = ["group_exec"]`.

Pinned-version details matter:

- `--strict-config` rejects `tools.view_image=false` even though the current
  online reference documents it. `features.view_image=false` is accepted.
- `unified_exec=false` alone is not a complete shell restriction; `shell_tool`
  must also be disabled.
- `approval_mode="auto"` leaves this write-capable MCP tool approval-gated and
  `never` rejects the call. Explicit per-tool
  `[mcp_servers.group.tools.group_exec] approval_mode="approve"` permits only the
  approved external tool. Do not globally bypass approval/sandbox protections.
- Native read-only sandbox does not sandbox trusted MCP servers. Their boundary
  is implemented by the external adapter/Docker configuration.

## Before a subscription probe or release

Reuse only the native supported subscription login in a **separate group CLI
runtime**; keep its config, session history and credential state distinct from
the personal runtime. Do not execute the candidate directly on the production
controller or mount Docker's socket into a model-accessible runtime. The MCP
adapter is trusted infrastructure; a production transport must enforce immutable
chat/namespace binding outside model-controlled arguments.

Before real traffic, use the same pinned image/managed configuration and record
tool inventory with a synthetic provider again, then qualify new/resumed sessions
and missing/revoked policy. Test at least the actual selected account model and
both CLI host platforms if supported. Only then perform a minimal supported
subscription inference + external canary tool call. Do not rewrite OAuth tokens
into a custom provider or silently switch to API billing. Keep logs limited to
status/tool names/canary outcomes, never credentials or personal connector data.

Remaining unproven: authenticated subscription behavior, hosted integrations
under login, resume/old-session isolation, configuration immutability, production
egress and Pi deployment. The mock-provider success does not prove these.

Reference: [official Codex configuration](https://learn.chatgpt.com/docs/config-file/config-reference).
