# Skills and plugins: how Claude Code, Codex and gopod attach them

| | |
|---|---|
| Date | 2026-10-30 |
| Status | analysis for [CP-034](../proposals/CP-034-private-chat-plugin-install.md); nothing built or enabled |
| Question | How are skills, plugins, MCP servers and connectors attached in each CLI and in gopod today, and what does installing official ones from the owner's private chat (surviving container recreation) require? |
| Evidence | Official docs read 2026-10-30 (Claude: code.claude.com; Codex: learn.chatgpt.com and developers.openai.com), Codex source on GitHub `main` (not the pinned tag), gopod source, and read-only local commands on the owner's Mac (`codex plugin list --json`, `codex mcp list`, `claude mcp list`). Neither CLI was run against an account in a container. |
| Versions | Pinned: Claude Code 2.1.310, Codex 0.164.0. Local: 2.1.312 and 0.165.0, so local observations are close, not identical. |

## 1. Conclusion

Both CLIs have a documented, scriptable install path for plugins (`claude plugin install ...
--json`, `codex plugin add ... --json`) and keep their state under the directory gopod already
bind-mounts from the host (`~/.claude`, `~/.codex`). Persistence across container recreation
therefore follows from the existing mounts and needs verification, not new storage. The work is
the gateway side: an owner-only command, an allowlist of official marketplaces, an installer that
runs outside the foreground turn, and a probe that detects what changed.

## 2. Comparison

| | Claude Code | Codex |
|---|---|---|
| Skill format | `<name>/SKILL.md` with frontmatter (`name`, `description`, more) | `<name>/SKILL.md` with `name`, `description`, optional `scripts/`, `agents/openai.yaml` |
| Skill locations | enterprise, `~/.claude/skills`, project `.claude/skills`, plugin-provided (`/plugin:skill`) | repo `.agents/skills`, `$HOME/.agents/skills`, `/etc/codex/skills`, bundled; `CODEX_HOME/skills` not documented |
| Loaded in headless | yes, without `--bare` (`--bare` ignores OAuth, so unusable on a subscription) | not documented for `codex exec` |
| Disable a skill | deny `Skill(name)`, `skillOverrides`, `--disable-slash-commands` | `[[skills.config]] path=..., enabled=false` |
| Plugin bundles | skills, commands, agents, hooks, MCP servers, LSP, monitors, `bin/` | skills, MCP servers, apps/connectors, hooks, browser extensions |
| Plugin manifest | `.claude-plugin/plugin.json` | `plugin.json` (or `.codex-plugin/plugin.json`) |
| Marketplaces | `claude plugin marketplace add/remove/update`; official ones auto-update | `codex plugin marketplace add/list/upgrade/remove`; names seen: `openai-curated-remote`, `openai-bundled`, `openai-primary-runtime` |
| Install / remove | `claude plugin install <id>@<marketplace> --scope user|project|local`, `uninstall`, `enable`, `disable`, `update`, `list`, `details`, all with `--json` | `codex plugin add <id>@<marketplace> [--json]`, `list [--json] [--available]`, `remove`; no enable/disable subcommand (enable by `[plugins."id@mkt"] enabled` in config or the TUI) |
| List output (`--json`) | array of `id`, `version`, `scope`, `enabled`, `installPath`, `errors` | `installed`/`available` arrays of `pluginId`, `name`, `marketplaceName`, `version`, `installed`, `enabled`, `installPolicy`, `authPolicy` (observed locally) |
| State on disk | `~/.claude/plugins/` (`installed_plugins.json`, `known_marketplaces.json`, `cache/`, `data/`) and `enabledPlugins` in settings; `CLAUDE_CONFIG_DIR` moves it | `plugins` and `marketplaces` tables in `~/.codex/config.toml`; cache location not documented |
| Pinning | manifest `version`; git sources by `ref`/`sha` | git sources by `ref`/`sha`; npm ranges |
| Admin allowlist of sources | managed settings `strictKnownMarketplaces` (cannot be overridden by the agent) | `requirements.toml` (`features.plugins`, MCP allowlist); marketplace-source keys not found |
| Connectors | `claude.ai` connectors, tools `mcp__claude_ai_<server>__<tool>`, off via `disableClaudeAiConnectors` | ChatGPT apps/connectors reached through `*@openai-curated-remote` plugins; `authPolicy ON_USE` / `ON_INSTALL`; `apps.<id>.enabled` |
| Seeing what is active | `system/init` event: `plugins`, `plugin_errors`, `mcp_servers`, `mcp_server_errors`; `claude plugin list --json`; `claude mcp list` | `codex plugin list --json` (diff before/after), exec `--json` events; `codex mcp list` shows only local servers |
| Sandbox note | gopod runs with `--dangerously-skip-permissions` (documented as for isolated containers) | gopod runs with `--dangerously-bypass-approvals-and-sandbox`; flag not in the docs fetched, existence on the pinned tag unverified |

## 3. gopod today (source read)

- **Container skills (type 1)** are implemented only as a mount: `container/skills/` (currently
  `freefeed`, `memory`, `scheduler`) is bind-mounted read-only at `/home/node/.claude/skills`, and
  only for the owner tier (`internal/runner/mounts.go:216`). Codex gets no skills mount.
- The per-chat `chats/<folder>/skills.json` filter and MCP skills (types 2) are **designed, not
  built** (`docs/SKILLS.md` section 8, steps S2-S5); no `internal/skills` package exists.
- **Provider plugins and connectors are not managed by gopod at all.** They live inside the
  per-chat session directories mounted at `/home/node/.claude` and `/home/node/.codex`
  (`mounts.go:212-215`, both writable); `/home/node` itself is tmpfs.
- Local observation (owner's Mac): Claude lists 15 claude.ai connectors by name and health
  including Gmail, Google Calendar, Drive and Docs; Codex lists 27 installed plugins including
  `gmail@`, `google-calendar@`, `google-drive@`, `slack@`, `github@` (all `*@openai-curated-remote`),
  some with opaque names (`app-6a31...`).

## 4. Persistence across container recreation

- Plugin state for both CLIs is under `~/.claude` and `~/.codex`, which are host directories, so it
  survives recreation by construction.
- **Risk 1:** `~/.claude.json` lives in `HOME` (tmpfs) and holds user-scope MCP servers and trust
  decisions. gopod restores it from `~/.claude/backups/` at start (`internal/runner/auth.go`). Plugins
  installed with `--scope user` are recorded under `~/.claude/plugins`, not in that file, but whether
  `CLAUDE_CONFIG_DIR` also moves `~/.claude.json` is undocumented. Verify in a container.
- **Risk 2:** the owner-tier read-only skills mount sits on `~/.claude/skills`. Plugin skills are in
  the plugin cache, not in that directory, so no overlap is expected; verify.
- **Risk 3:** plugin caches can be large and are never cleaned by gopod; backups (CP-007) do not yet
  cover the session directories. Decide retention.
- **Risk 4:** a plugin installed inside a running container is invisible to a CLI process that
  already started; the next turn sees it. Installing must not race a running turn.
- Codex remote plugins may create an account-level grant on the ChatGPT side (an unofficial issue
  report, openai/codex#30023); that part is in the provider account, not in the container, so it
  persists independently of gopod.

## 5. Official sources only

- Claude: restrict marketplaces to the official ones with `strictKnownMarketplaces` in
  `/etc/claude-code/managed-settings.json` inside the image (the agent cannot override it) and have
  the gateway accept only `<plugin>@<official-marketplace>` ids.
- Codex: keep to the curated marketplaces (`openai-curated-remote`, `openai-bundled`,
  `openai-primary-runtime`); an admin-enforced marketplace allowlist is not confirmed, so the gateway
  allowlist is the control. Pin git marketplaces by `sha` where the entry allows it.
- The exact official marketplace names and ids are checked when the spec is written, from the
  provider's own listing, not from this note.

## 6. Open items for the spec (verify in a container, no account use beyond a normal install)

1. Whether a plugin installed with `claude plugin install --scope user` and
   `codex plugin add` survives `docker rm` + recreate with the current mounts, and what each leaves
   in `HOME`.
2. Whether install must run in the owner's container (`docker exec`) or in a one-shot container
   sharing the mounts (cleaner; no race with a running turn).
3. Non-interactive behavior: `-y` and `CLAUDE_CODE_SYNC_PLUGIN_INSTALL=1` for Claude; `authPolicy`
   consent flow for Codex remote plugins (the consent happens in the provider account).
4. Auto-update: decided 2026-10-30, stays on for official marketplaces; the spec only records version changes.
5. Whether Codex `exec` loads skills and whether `CODEX_HOME/skills` is searched.
6. The pinned Codex tag's real `plugin --help` and `--json` shapes (the research used `main` and the
   owner's 0.165.0).

Note on restrictions: the research suggested disabling plugins/apps or bypass-mode limits for
headless runs. Those are not applied: per the owner's decision (2026-10-30) nothing is restricted in
the private chat or the owner's sessions, and connectors must work. The only gate is the
owner-requested "official sources only" for what the gateway installs.

## Sources

Claude: code.claude.com/docs/en/{skills,plugins/loading,plugins/cli-reference,plugins/manifest-reference,
plugins/create-marketplace,mcp,settings,permissions,managed-settings,headless,cli-reference,env-vars}.
Codex: learn.chatgpt.com/docs/{build-skills,plugins,non-interactive-mode,enterprise/managed-configuration,
config-file/config-advanced}, developers.openai.com/plugins/build/plugins, github.com/openai/codex
(`plugin_cmd.rs`, `marketplace_cmd.rs`, `config.schema.json` on main; issue 30023). gopod:
`docs/SKILLS.md`, `internal/runner/mounts.go`, `internal/runner/auth.go`.
