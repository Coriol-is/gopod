# picoclaw

A small personal Claude assistant on Telegram, written in Go.
A focused descendant of [NanoClaw](https://github.com/spaceinvaderz/nanoclaw):
one channel (Telegram), one language (Go), per-chat container isolation,
native long-term memory, plus a small skill ecosystem (container skills + MCP)
that doesn't require recompiling.

**Status:** design phase. No code yet.
**Position vs NanoClaw:** ~10% of the surface area, ~90% of the day-to-day value.

## Quick links

| Doc | What it covers |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | System architecture, package layout, message flow, lib choices |
| [docs/MEMORY.md](docs/MEMORY.md) | 5-layer memory model: working / identity / scratchpad / semantic / cross-chat |
| [docs/INTEGRATIONS.md](docs/INTEGRATIONS.md) | NanoClaw skills triaged into Tier 1 / 2 / Skip |
| [docs/SKILLS.md](docs/SKILLS.md) | Skill ecosystem: container skills, MCP skills, dev skills |
| [docs/CONTROL.md](docs/CONTROL.md) | Control plane: Router, Telegram + CLI frontends, command catalog |
| [docs/DECISIONS.md](docs/DECISIONS.md) | Architectural decision log (ADR-style) |
| [docs/GLOSSARY.md](docs/GLOSSARY.md) | Project vocabulary |
| [docs/HANDOFF.md](docs/HANDOFF.md) | Rolling state — read this first when picking up the project |
| [ROADMAP.md](ROADMAP.md) | Milestones M0–M9 + integrations I1–I10 with statuses |
| [CLAUDE.md](CLAUDE.md) | Instructions for Claude / agentic dev sessions |

## What it does (when built)

- Runs as a single Go process (`cmd/picoclaw`)
- Long-polls a Telegram bot you own
- Spawns one Docker container per chat the agent is registered in (long-lived,
  idle-killed) and runs a Claude Code agent loop inside it
- Per-chat filesystem isolation, per-chat session, per-chat memory
- Native long-term memory via `sqlite-vec` with hybrid (semantic + lexical) search
- Scheduled tasks (cron / interval / once) per chat
- Owner chat (single chat ID in config) has elevated privileges to register
  new chats and schedule tasks for others
- Extensible via a small skill ecosystem (container skills + MCP servers,
  no recompile required) — see [docs/SKILLS.md](docs/SKILLS.md)

## What it deliberately doesn't do

- WhatsApp / Slack / Discord / Gmail / Signal / Emacs / X channels
- A NanoClaw-style "merge a `skill/*` branch" plugin model (skills exist,
  but installed as directories, not as code merges — see [docs/SKILLS.md](docs/SKILLS.md))
- Multi-runtime support (Docker only; subprocess fallback for dev)
- OneCLI credential gateway
- macOS status-bar UI
- Migration tooling for existing NanoClaw installs

See [docs/INTEGRATIONS.md §Tier 3](docs/INTEGRATIONS.md) for the full skip list
and reasoning.

## Layout (planned)

```
picoclaw/
├── README.md                  # this file
├── CLAUDE.md                  # entry point for agentic dev sessions
├── ROADMAP.md                 # milestones with status
├── docs/                      # design docs (stable)
├── cmd/picoclaw/              # main binary
├── internal/                  # implementation packages
├── container/                 # agent container image
├── chats/                     # per-chat workspace (gitignored)
└── data/                      # runtime state (gitignored)
```

## Origin

This project exists because NanoClaw is great but its TypeScript footprint and
plugin surface area are bigger than one person needs. picoclaw keeps the ideas
that matter (per-chat isolation, container-per-chat agent, GroupQueue, IPC,
scheduler, layered memory) and drops everything else.

## License

TBD.
