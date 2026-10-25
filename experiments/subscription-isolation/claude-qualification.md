# Claude native subscription candidate

`claude_policy.py` only generates argv and environment overrides. It neither
executes the CLI nor reads/copies authentication. Only loopback HTTP MCP endpoints
without credential/query data are accepted for this offline qualification.

Observed pinned version: **Claude Code 2.1.310**. A real local Docker launch with
network disabled, fresh tmpfs home, no credential mounts and these controls emitted
`system/init` with `tools=[]`, `mcp_servers=[]`, then the expected controlled
`Not logged in` failure. No model request was made and no real credentials used.
This proves startup and empty-native-tool inventory for that no-auth case only.
Explicit MCP inventory/new/resume tool round trips require additional tests.

The candidate uses `--restricted`, `--tools ""`, explicit strict MCP config,
`dontAsk` with only `mcp__group__group_exec` allowed, disabled slash commands,
connectors/hooks/auto-memory disabled, no user/project/local settings, a synthetic
system prompt rebuilt every turn, and explicit session IDs for persistence tests.
No bypass-permission or bare mode is used. `--allowedTools` grants permission;
the actual built-in removal is `--tools ""`. Official docs note that MCP tools
are independent and a non-I/O `EndConversation` tool may remain with MCP enabled.

## Live subscription gate

Run the same native CLI in a trusted minimal CLI runtime already authenticated
through its supported login flow. Preserve per-group native sessions and clean
configuration; never mount a personal conversation/config home. Do not run the
candidate in the production controller host's default home. Never copy/export
tokens to the command container, MCP payload, prompt, shell environment or logs.

The only model-visible filesystem/code capability should be the fixed group MCP
tool, with separate tool execution and no access to the CLI runtime. Qualify exact
init/provider tool inventory and new/resumed runs; managed settings/plugins must
not add authority. Fail closed on additional local file/code/network tools or on
failed/missing MCP. A minimal live test asks for one canary file operation through
group_exec and records names/status only. It must not ask for auth file contents
or enumerate actual personal connectors. Shared quota/account use remains an
account policy consideration, not something the CLI flags establish.

`--strict-mcp-config` may conflict with a managed-MCP deployment; this standalone
candidate assumes none. `--bare` cannot preserve ordinary OAuth subscription
auth. `--safe-mode` disables MCP customizations and is not substituted for the
explicit MCP candidate.

References:
- [CLI controls](https://code.claude.com/docs/en/cli-reference)
- [Account connector controls](https://code.claude.com/docs/en/mcp#disable-claude-ai-connectors)
