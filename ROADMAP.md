# picoclaw — Roadmap

Live milestone tracker. Update the **Status** column whenever you change reality.
For the *why* behind each milestone, see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md);
for integrations, see [docs/INTEGRATIONS.md](docs/INTEGRATIONS.md); for memory,
see [docs/MEMORY.md](docs/MEMORY.md). For the rolling "what's actually happening
right now" view, see [docs/HANDOFF.md](docs/HANDOFF.md).

## Status legend

- ⬜ not started
- 🟦 in progress
- ✅ done
- ⏸️ paused
- ❌ dropped (link to ADR)

---

## Phase 1 — Core (M0–M9)

| ID | Milestone | Status | Notes |
|----|-----------|--------|-------|
| M0 | Skeleton: `go.mod`, `cmd/picoclaw/main.go`, `internal/config`, `internal/store` schema, slog wiring | ⬜ | |
| M1 | Telegram echo: long-poll, default handler stores every message, `/ping` replies | ⬜ | Lib: `go-telegram/bot` |
| M2 | Direct API agent (no container): `anthropic-sdk-go`, single chat, trigger pattern, per-chat session | ⬜ | First end-to-end response |
| M3 | GroupQueue: per-chat serialization + global concurrency cap + backoff | ⬜ | |
| M3.5 | **Control plane** (`internal/control` Router + Telegram + CLI frontends + logs subsystem). Sub-steps C1–C5 below. See [docs/CONTROL.md](docs/CONTROL.md). | ⬜ | M4+ depend on this — every later milestone registers commands through the Router |
| M4 | Scheduler: cron/interval/once via `robfig/cron/v3`, registers `/tasks *` handlers with the Router | ⬜ | |
| M5 | Container runtime: Docker SDK, mounts, exec attach, idle kill, label-based recovery | ⬜ | `PICOCLAW_NO_CONTAINER=1` keeps M2 path alive |
| M6 | Agent SDK in container: switch from direct API to `character-ai/claude-agent-sdk-go` Client over `docker exec ... claude` | ⬜ | Real Read/Write/Bash |
| M7 | IPC: filesystem watcher, container → host messages, task ops, owner gating | ⬜ | |
| M8 | Recovery & polish: cursor backfill, leftover-container cleanup, structured logs, README + Compose example | ⬜ | |
| M9 | Native memory: sqlite-vec, embedder, `memory_*` tools, Anthropic Memory Tool, auto-summarize. See [docs/MEMORY.md §10](docs/MEMORY.md) for sub-steps M9.1–M9.10 | ⬜ | |

### M3.5 sub-steps (control plane)

| ID | Step | Status |
|----|------|--------|
| C1 | Router scaffold: `Command`, `Caller`, `Perm`, `Response`, `Router.Register/Dispatch`, auth enforcement, unit tests | ⬜ |
| C2 | Telegram frontend: `parseSlash`, dispatch wiring, `/ping`, `/whoami`, `/version`, `/help` | ⬜ |
| C3 | CLI frontend: argv parser, `serve`/`migrate` reserved subcommands, `--json`, mirror of public commands | ⬜ |
| C4 | First batch of real handlers: chats, queue, container, system | ⬜ |
| C5 | Logs subsystem: `internal/log/sqlite_handler.go`, retention task, `/logs` and `picoclaw logs` | ⬜ |

---

## Phase 2 — Integrations (I1–I9)

After M0–M9 the core works. These add NanoClaw-style features one by one.
See [docs/INTEGRATIONS.md](docs/INTEGRATIONS.md) for full triage.

| ID | Integration | Tier | Status |
|----|-------------|------|--------|
| I1 | Reactions (👀 ✅ ❌) + typing indicator | 1 | ⬜ |
| I2 | Image vision (multimodal blocks) | 1 | ⬜ |
| I3 | Voice transcription (OpenAI + local Whisper toggle) | 1 | ⬜ |
| I4 | PDF reader | 1 | ⬜ |
| I5 | Channel formatting (Markdown → MarkdownV2) + `/compact` | 1 | ⬜ |
| I6 | Karpathy LLM Wiki convention | 1 | ⬜ |
| I7 | Telegram Agent Swarm (multi-bot teams) | 1 | ⬜ |
| I8 | Ollama tool | 2 | ⬜ |
| I9 | Gmail tool mode | 2 | ⬜ |
| I10 | X tool, Parallel tool — as needed | 2 | ⬜ |

---

## Phase 3 — Skill ecosystem (S1–S?)

See [docs/SKILLS.md](docs/SKILLS.md). Originally dropped, now redesigned for Go.
Tracked as its own phase because the milestones are orthogonal to core work.

| ID | Step | Status |
|----|------|--------|
| S1 | Container-skills directory + mount into agent container | ⬜ |
| S2 | MCP skill loader: spawn local MCP servers per chat, register tools | ⬜ |
| S3 | Skill manifest format + validation | ⬜ |
| S4 | Per-chat skill enable/disable | ⬜ |
| S5 | Dev-time Claude Code skills in `.claude/skills/` for picoclaw maintainers | ⬜ |

---

## Deferred

| Topic | Why deferred | Revisit when |
|-------|--------------|--------------|
| Honcho identity reasoning | External service, billing, network dep | Local memory layer (Layer 3) proves insufficient in practice |
| Multi-channel | Out of scope by design | Never (would be a different project) |
| macOS status bar | Not wanted by user | — |

---

## How to update this file

1. Change a status emoji.
2. If you changed scope, add a row or strike through and link the ADR.
3. Mention it in the next [HANDOFF.md](docs/HANDOFF.md) entry.
