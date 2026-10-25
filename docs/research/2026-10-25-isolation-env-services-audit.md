# Container isolation audit — environment, CLI configuration and connected services

Date: 2026-10-25. Source baseline: `ea38581e95ae214ff2a4c0dd5fc8f7f40b1f7d86`.
Scope: read-only source audit plus official documentation and offline help/feature
inspection of **Codex CLI 0.164.0** and **Claude Code 2.1.310**. No production
credential file/environment values were read, authenticated request or model
session started, or production configuration changed. Other agents own the
memory/filesystem audit and the separate login-privacy implementation.

## Requirement and conclusion

A registered group must not inherit personal accounts, facts, connected apps,
MCP servers, plugins, service credentials or a personal filesystem merely because
it can invoke the same inference provider. Separate memory SQL namespaces are
necessary but do not enforce this whole boundary.

Current code has only a provider-wide environment allowlist, unrestricted
container networking, permissive CLI launches and writable persistent CLI homes.
It does not implement a restricted group profile. Personal service access is
therefore **not disabled by construction**. The env leak is directly code-traced;
which actual account integrations or secret values are configured in production
was deliberately not inspected.

## Findings

| ID / severity | Reachable path and evidence | Impact / evidence boundary | Bounded correction |
|---|---|---|---|
| E01 — High | [main.go:188](../../cmd/gopod/main.go) creates one allowlist containing `OPENAI_API_KEY`, `FREEFEED_BASE_URL`, `FREEFEED_USERNAME`, `FREEFEED_PASSWORD`; [runner.go:218](../../internal/runner/runner.go) appends it to the selected provider requirements for every tier; [docker_args.go:276](../../internal/runner/docker_args.go) reads matching host values into container `Config.Env`. | Any registered group receives these credentials when configured. A Claude group also receives the host OpenAI key even though its inference provider is Claude. The group agent has Bash/Read and can read its own process environment. No container escape is needed. Actual production values were not read. | Resolve env by trusted container profile plus explicit chat-specific inference credential reference. Default-deny personal FreeFeed/other service credentials and cross-provider keys for every nonowner profile. Do not union a global personal env list into group defaults. |
| E02 — High | [Claude launches](../../internal/runner/provider_claude.go#L35) use ordinary user/account config with `--dangerously-skip-permissions`; [Codex launches](../../internal/runner/provider_codex.go#L38) bypass approvals/sandbox with no apps/plugins/MCP disable policy. Official Claude docs say subscription-login account connectors are fetched automatically. Codex apps are documented as on by default. | A fresh per-chat config directory does not establish an integration-free account. If a group authenticates the owner's subscription account, account-level services can become available independently of copied local settings. Actual production connector availability was not probed. | Explicitly deny connectors/apps/MCP/plugins, and separately decide whether the group credential/account itself is permitted for a restricted profile. Verify actual tool discovery with fake canaries and a designated test account before claiming the boundary. |
| E03 — High enforcement gap | [mounts.go:210-212](../../internal/runner/mounts.go) gives the agent writable persistent `.claude` and `.codex`; Claude [RestoreConfigCmd](../../internal/runner/provider_claude.go#L58) restores `.claude.json` from backups. Bash and external networking remain available. | Top-level CLI flags alone are bypassable: the agent can alter its writable config or launch another CLI without those flags. Readable OAuth credentials may also authorize direct service requests. A prompt asking the agent not to do this is not access control. This is a code-traced authority path, not a live exfiltration experiment. | Immutable controller-owned managed restrictions, safe config assembly, and a credential/network boundary. Prefer a dedicated integration-free group account/project or an inference-only broker that never exposes the real personal OAuth token/key to the agent. |
| E04 — Medium/High migration gap | [docker.go:118-129](../../internal/runner/docker.go) reuses a running container when its immutable image matches. Env/mount/profile policy is not compared. | Editing an env allowlist or profile without changing the image/reconciling the running container can leave old credentials, mounts and authority active. Profile revocation must affect existing runtimes too. | Include trusted profile/policy revision and approved credential-reference version in runtime identity, and reconcile under the existing chat agent slot. Preserve a runtime on unverifiable identity, but fail its admission closed. Never recycle it concurrently with a user turn. |
| E05 — High capability path / concrete env leak amplifier | [docker_args.go:156](../../internal/runner/docker_args.go) leaves networking enabled and [237](../../internal/runner/docker_args.go) exposes `host.docker.internal` to the host gateway. There is no per-profile egress policy in this builder. | The agent can use exposed credentials through curl/CLI and can reach host/LAN destinations allowed by the host network. This does not prove any particular host port is open; no port scan was run. A command-sandbox domain policy alone does not govern every hosted app/MCP channel. | Restrict group egress at the container/host or proxy layer, deny host/LAN/metadata destinations except the exact authenticated memory endpoint, and separately disable provider-hosted apps/MCP. Shared provider domains may require path/method controls, not just DNS allowlists. |
| E06 — Medium authority/config exposure | Every registered chat gets [the entire shared skills directory](../../internal/runner/mounts.go#L211). The [FreeFeed skill](../../container/skills/freefeed/SKILL.md#L10) advertises personal feed/direct-message/post/like operations. The bundled [frf config](https://github.com/Spaceinvaderz/frf-cli/blob/2ad6aba6754817198d4cc1d8923e906ae8ecc475/internal/app/config.go#L28) accepts username/password as well as a token. | The skill text itself is not a secret. Together with E01 it directs group agents toward working personal-account capabilities. Disabling skill auto-discovery alone does not remove credentials or the bundled CLI. | Mount a profile-specific minimal skills set; no personal-service skills in group profiles. Keep service credentials absent and egress constrained even if the executable remains in the image. |

Memory/vault, standard-mount symlink and extra-mount findings are tracked by the
parallel audit. This document does not downgrade those independent bypasses.

## Existing protections that remain useful

- Per-chat CLI home paths are distinct host directories; the source does not
  indiscriminately bind the owner's entire host `~/.claude`/`~/.codex` into every
  group. Their *account* and loaded service authority still require policy.
- The agent receives a short-lived memory capability through
  [agentEnv](../../internal/runner/capability.go#L28), bound to chat folder/thread,
  rather than the Telegram bot token. This is separate from Docker spawn env.
- The standard Telegram token is not in the main agent env allowlist.
- Root filesystem is read-only, capabilities dropped, no-new-privileges enabled,
  and the Docker socket is not a standard group mount. These controls do not
  prevent using credentials deliberately placed inside the group container.

## Official controls and pinned-version evidence

Official documentation was retrieved before local CLI inspection. Commands below
were inspected with disposable empty config/home directories and a minimal
child-process environment; no personal config or credentials were loaded.

### Codex 0.164.0

Offline `codex --version`, `codex exec --help`, `codex exec resume --help` and
`codex features list --disable apps --disable plugins` succeeded.

- Both exec and resume support `--ignore-user-config`. Help says it skips
  `$CODEX_HOME/config.toml` but still uses `CODEX_HOME` for auth. It is **not** a
  statement that project config, rules, skills or account services are all gone.
- Both support repeatable `--disable <FEATURE>` / `-c features.<name>=false`.
  `apps` and `plugins` were reported stable and false with those overrides.
- `remote_plugin` and `skill_mcp_dependency_install` still reported true in that
  inspection. Disable/verify them separately rather than treating plugins=false
  as proof that every extension-discovery/install path is disabled.
- `--ignore-rules` is supported; ignoring policy files is not a substitute for
  applying a restrictive managed policy.
- Official config docs expose `features.apps=false`, managed
  `features.plugins=false`, default/per-app enables and MCP allowlisting by name
  **and identity**. Managed Unix requirements live at
  `/etc/codex/requirements.toml`.
- Do not assume an empty ordinary `mcp_servers={}` override erases merged user
  config. Use a fresh controlled config tree and an enforced allowlist; verify
  empty-allowlist semantics on the pinned client before release.

Sources:
[config reference](https://learn.chatgpt.com/docs/config-file/config-reference),
[managed configuration](https://learn.chatgpt.com/docs/enterprise/managed-configuration).
The docs explicitly separate command networking from apps/MCP/hosted capability
controls; a shell-network allowlist is not a universal connector firewall.

### Claude Code 2.1.310

Offline `claude --version` and `claude --help` succeeded.

- `--strict-mcp-config --mcp-config '{"mcpServers":{}}'` is a supported
  exclusive CLI-config mechanism when no conflicting managed MCP file is
  deployed. It is stronger than merely hiding a tool-search UI.
- `disableClaudeAiConnectors=true` or
  `ENABLE_CLAUDEAI_MCP_SERVERS=false` disables the connectors terminal/SDK Claude
  Code fetches from claude.ai. This does not govern connectors supplied in
  process by a desktop host/cloud host; this deployment uses the terminal CLI.
- `--safe-mode` is present and disables customizations while keeping auth/model
  selection working. It is useful defense in depth, not an OS/service boundary.
- **`--bare` is not a drop-in OAuth-compatible solution.** The pinned help says
  it uses only `ANTHROPIC_API_KEY` or `apiKeyHelper` for Anthropic auth and never
  reads OAuth/keychain. It still exposes Bash and file tools; explicit settings,
  MCP configs and skills can be supplied. Do not silently change subscription
  billing/auth behavior by selecting bare mode.
- `--setting-sources` exists, but excluding config sources does not by itself
  establish that account-level connectors are disabled.
- Stronger managed local enforcement: a read-only
  `/etc/claude-code/managed-mcp.json` with `{"mcpServers":{}}` suppresses ordinary,
  plugin and fetched claude.ai servers under the documented exclusive set.
  Managed-provided/in-process/built-in exceptions must also be excluded in the
  group image/profile. **Do not combine this blindly with --strict-mcp-config**:
  current official docs say that flag attempts to replace the managed set and
  causes startup failure when the managed file is deployed. Validate the pinned
  combination using a test image before rollout.

Sources:
[CLI reference](https://code.claude.com/docs/en/cli-reference),
[claude.ai connectors and their disable control](https://code.claude.com/docs/en/mcp#disable-claude-ai-connectors),
[exclusive managed MCP](https://code.claude.com/docs/en/managed-mcp#exclusive-control-with-managed-mcp-json).

The existing synthetic `TestBuildContainerArgsEnvAllowlist` passed (0.784s); it
confirms the forwarding mechanism, not a restricted-profile policy.

No claim is made that a particular connected service was invoked, or that all
these controls have been integration-tested in the production container image.

## Proposed container profiles

| Authority | owner_private | group_restricted | other_private |
|---|---|---|---|
| Identity | configured owner in trusted private context | registered group, never inferred owner from display chat | registered nonowner private chat |
| Long-term memory/history | owner's namespace | group namespace + policy epoch | this private chat's namespace |
| Personal vault/project/store | explicit owner policy | absent | absent by default |
| Env | explicit owner allowlist | safe runtime values + explicitly approved group inference credential only | same restricted baseline unless explicitly granted |
| Personal FreeFeed/Gmail/Drive/etc. | explicit owner grants | denied | denied by default |
| MCP/apps/connectors/plugins | explicit owner policy | disabled and enforced | disabled by default |
| CLI state | owner's own state | fresh controller-assembled group-only state; no personal config/credential copying | own state only |
| Skills | approved owner skills | minimal memory/scheduler or explicit group allowlist | minimal explicit allowlist |
| Network | explicit owner policy | authenticated memory endpoint + approved inference path; no general host/LAN or personal service access | restricted baseline |
| Public conversation output | private DM | normal group output | private DM |
| Login display | private DM | owner-private confirmation/display for group target | this private DM |

This is proposed implementation scope. A private login screen is only a safe
presentation channel; it does **not** by itself make the resulting group account
integration-free. The confirmed target must remain group folder/TierRegistered,
not the owner's workspace/TierOwner.

## Delivery plan and non-regression tests

1. **Profile authority first:** derive profile from trusted registered-chat and
   Telegram kind metadata; require a private configured owner for owner profile.
   Agent-written JSON, a prompt, a positive/negative numeric id alone or a DM
   presentation target must not elevate the runtime. Persist a policy revision.
2. **Default-deny env and mounts:** split personal-service credentials from model
   inference credentials. Resolve only the selected group's explicit credential
   reference; no fallback to owner's broad key. Mount no personal vault, global
   service skills or config trees. Use synthetic fixture secrets in tests.
3. **Connected-service enforcement:** choose a dedicated integration-free
   account/project or an inference-only credential broker. Apply immutable
   managed CLI policy and controlled homes as defense in depth. Validate native
   model start/resume, fresh system work, login and management app-server paths;
   every subprocess path must carry the profile. Do not change billing model
   silently to make a flag work.
4. **Egress and lifecycle:** restrict network destinations/capability routes;
   add profile/credential-reference revision to container identity and reconcile
   under the chat slot. Verify no preexisting runtime retains revoked env,
   mounts or service access when the image id is unchanged.
5. **Migration:** existing group homes/workspaces/transcripts may contain
   previously exposed material. Preserve evidence/backup, quarantine under a new
   restricted-profile epoch and decide reinitialization explicitly. Removing a
   mount does not erase knowledge already copied into group state. No deletion
   or production mutation is performed by this audit.
6. **Adversarial acceptance:** with unique dummy secrets/canary facts, prove
   absence from group Docker/env/exec env/proc/CLI home/logs/outgoing messages;
   prove private memory and vault canaries cannot be retrieved through SQL/API,
   files, native resume, background extraction or IPC. Fake MCP/apps endpoints
   must observe zero initialization/tool calls for group runs, including resume,
   a freshly edited project config, a second CLI launched from Bash and a
   downloaded/custom HTTP client. Deny tests need independent positive controls.
   If a readable personal credential and unrestricted alternate client can
   still reach the service, the profile is **not isolated** despite a clean
   top-level tool list.

The billing/account/broker choice is a concrete design decision for the overall
plan; disabling a top-level tool list cannot honestly substitute for it.
