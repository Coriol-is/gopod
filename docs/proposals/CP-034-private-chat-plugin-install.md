# CP-034 — Install official skills and plugins from the private chat

| | |
|---|---|
| Status | accepted (2026-10-30; auto-update stays on for official marketplaces) |
| Date | 2026-10-30 |
| Origin | owner requirement during the connector discussion; analysis in the [mechanisms note](../research/2026-10-30-skills-and-plugins-mechanisms.md) |
| Size | M |
| Touches | `internal/control` (new owner-only command), `internal/runner` (one-shot install container, probe), `internal/store` (installed-set record), `internal/telegram` (picker buttons), `container/` (provider-side allowlist config) |
| Depends on | CP-033 for the connector probe and the "other connectors" offer; CP-007 for backup of the session directories |
| Spec | `—` until accepted |

## Problem
The owner can add a skill or plugin only by editing files on the Pi or by asking the agent to do
it inside a container, and nothing guarantees it survives the next container recreation or that it
came from an official library. gopod mounts `~/.claude` and `~/.codex` but has no command that
installs, lists, updates or removes what the two providers publish.

## Proposal
A gateway command (name open) through `internal/control`, owner-only and **private chat only**
(Router `Perm`, refused in groups and for any non-owner), that lists installable official skills
and plugins from the providers' own libraries (Claude's official plugin marketplace and Anthropic
skills; Codex's curated and bundled marketplaces) and installs, enables, disables, updates and
removes them, with inline-button pickers (CLAUDE.md "Commands with parameters"). Installs are
restricted to a short allowlist of official marketplaces and run in a one-shot container sharing the
owner's `~/.claude` and `~/.codex` mounts, not inside a running turn, so the result lands in the
host directories and survives container recreation. After each change the gateway re-probes with
`claude plugin list --json` and `codex plugin list --json`, records the installed set, and the
next turn sees it. Consent that must be given in the provider's account is handled by the setup
guide of CP-033; the gateway never signs in for the owner.

## Out of scope
Non-official sources and arbitrary marketplaces; installing from groups or for other chats; writing
or publishing skills; MCP servers outside plugins; restricting what an installed plugin may do in
the owner's own chat; changing the existing `container/skills` mount.

## Risks
A plugin can ship hooks and scripts that run with the agent's rights, so "official" must be a real
allowlist of marketplace ids, not a name match. Auto-update stays on for official marketplaces
(owner decision 2026-10-30), so behavior can change between runs; the recorded installed set shows
version changes after each probe. An
install that races a running turn can leave the CLI with a half-seen set. Session directories grow
and are not backed up yet.

## Acceptance
- The command is refused by the Router outside the owner's private chat, including forged callbacks.
- A plugin from an allowlisted marketplace installs and appears in the next turn; one from any other
  source is refused with the reason.
- After the container is removed and recreated, the installed plugins are still listed and usable
  for both providers, shown by `plugin list --json` and a turn that uses one.
- Install, disable and remove change the recorded set and the provider-side state consistently, and
  a failed install leaves the previous set intact.
- An install during a running turn waits or is refused, never half-applied.
