# CP-008 — `/model` per chat

| | |
|---|---|
| Status | done (reconciled 2026-10-25) |
| Date | 2026-10-05 |
| Origin | user request 2026-10-05: "у бота нет поддержки команды /model" |
| Size | S |
| Touches | `internal/runner` (provider interface + per-chat model), `internal/control` (command), `cmd/gopod/main.go` (persistence in `router_state`) |
| Depends on | — |
| Spec | bounded change, no spec |

## Original problem (resolved)
There was no per-chat /model command or provider-specific flag plumbing.
This is implemented. Picker labels are not a live model catalog; provider
acceptance still determines whether a supplied model name is usable.

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

## Shipped evidence and remaining acceptance
control/handlers.go RegisterModelCommand and model tests cover selection/reset;
main restores/persists model:<folder>; runner/model_test.go covers provider
flag insertion and chat state. User turns apply the override; fresh system
turns use provider defaults. Live model use and restart acceptance remains A11
in [ACCEPTANCE.md](../ACCEPTANCE.md). Per-topic/model-provider scoping is a new
CP-018 design, not an unfinished part of this per-chat implementation.
