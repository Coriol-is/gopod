# CP-008 — `/model` per chat

| | |
|---|---|
| Status | accepted (2026-10-05) |
| Date | 2026-10-05 |
| Origin | user request 2026-10-05: "у бота нет поддержки команды /model" |
| Size | S |
| Touches | `internal/runner` (provider interface + per-chat model), `internal/control` (command), `cmd/gopod/main.go` (persistence in `router_state`) |
| Depends on | — |
| Spec | bounded change, no spec |

## Problem
Both harnesses run on their default model: `provider_claude.go` and
`provider_codex.go` pass no model flag, and there is no command to change
it. Claude Code accepts `--model <alias|name>` (aliases `fable`, `opus`,
`sonnet` in 2.1.289); Codex 0.160 accepts `-m/--model` on both `exec` and
`exec resume`. Neither CLI lists models, so a typo only surfaces on the next
turn.

## Proposal
`/model [name|default]` through the Router (`PermChatLocal`), mirroring
`/provider`: no argument shows the current choice, `default` clears it.
The choice is stored per chat in `router_state` and restored at boot. The
runner keeps a per-chat model next to the per-chat provider and asks the
provider for its flag (`ModelArgs`), so the Claude/Codex difference stays
inside `AgentProvider`. The model applies to user turns (`Run`, `RunStream`);
system turns (extraction, compaction) keep the provider default.

## Out of scope
Model listing, cost display, per-message overrides, fallback models.

## Risks
An unknown model name fails the next turn with the CLI's error; the
command's usage text names the known aliases, and the stderr now reaches
the log.

## Acceptance
- `/model` shows `claude: default` (or the chosen name); `/model sonnet`
  persists across a gopod restart; `/model default` clears it.
- The next turn's exec command carries `--model sonnet` (Claude) or `-m`
  (Codex), verified by provider unit tests.
- Extraction and compaction commands carry no model flag.
