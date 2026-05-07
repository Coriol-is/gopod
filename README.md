# gopod

A personal Claude assistant on Telegram, written in Go. One channel,
one language, per-chat container isolation, native long-term memory
with hybrid search, voice support, and scheduled tasks.

A focused descendant of [NanoClaw](https://github.com/spaceinvaderz/nanoclaw):
~10% of the surface area, ~90% of the day-to-day value.

## Quick start

```sh
# 1. Clone (with submodules) and build the agent image
git clone --recurse-submodules https://github.com/Coriol-is/gopod
cd gopod
docker build -t gopod-agent:latest -f container/Dockerfile .
# Codex variant (optional, for /provider codex):
# docker build -t gopod-agent-codex:latest -f container/Dockerfile.codex .

# 2. Configure
cp .env.example .env
# Edit .env: set TELEGRAM_BOT_TOKEN, GOPOD_OWNER_CHAT_ID, OPENAI_API_KEY

# 3. Run
go run ./cmd/gopod

# 4. Open your Telegram bot chat and type /login
# 5. Complete the OAuth flow → send a message → get a Claude reply
```

## What it does

- **Telegram bot** — long-polls your bot, stores every message in SQLite
- **Multi-provider agents** — pluggable AgentProvider interface; Claude and Codex supported, switch per chat with `/provider`
- **Per-chat isolation** — each chat gets its own Docker container, filesystem, memory, and session
- **Long-term memory** — hybrid FTS5 + sqlite-vec search, auto-extraction of facts from conversations, context compiler with budget-based prompt injection, memory HTTP API for container agents
- **Session compact** — `/clear` (wipe context), `/compact` (summarize + wipe), auto-compact by turns/interval/daily schedule
- **Voice** — Whisper transcription for voice input, TTS for voice output (auto-mirrors input modality)
- **Image vision** — send photos/documents, agent analyzes them
- **Scheduled tasks** — cron/interval/once via natural language (`/tasks add check weather every morning at 9`)
- **Markdown formatting** — Claude's markdown renders as bold, italic, code blocks in Telegram
- **Reactions** — 👀 processing, 👍 success, 👎 error
- **Reply threading** — bot replies are threaded to your original message

## Commands

| Command | Description |
|---|---|
| `/help` | list all commands |
| `/ping` | liveness check |
| `/whoami` | show chat id and registration state |
| `/login` | authenticate Claude Code (Pro/Max subscription) |
| `/register <chat_id> <folder>` | (owner) register another chat |
| `/tasks add <description>` | schedule a task in natural language |
| `/tasks list` | list scheduled tasks |
| `/tasks pause/resume/cancel <id>` | manage tasks |
| `/remember <fact>` | save to long-term memory |
| `/recall <query>` | search memories |
| `/memory gc\|stats` | memory lifecycle management |
| `/voice <mode>` | set reply mode: auto, voice, text, voice+text |
| `/clear` | clear conversation context (memory preserved) |
| `/compact` | summarize conversation + clear context |
| `/provider <name>` | switch agent provider (claude, codex) |
| `/logs` | (owner) show recent gopod logs |
| `/version` | build info |

## Architecture

```
gopod (Go, host)
├── Telegram long-poll
├── Queue (per-chat serialization, global cap)
├── Runner (Docker SDK, container lifecycle, session compact)
│   └── AgentProvider (claude, codex — pluggable per chat)
├── Memory (sqlite-vec + FTS5, OpenAI embeddings)
│   ├── Context compiler (budget-based prompt injection)
│   ├── Extraction layer (auto-extract facts from conversations)
│   └── HTTP API (search/add/list for container agents)
├── Scheduler (cron/interval/once, robfig/cron/v3)
├── Control plane (Router + slash commands)
└── SQLite store (messages, chats, tasks, memories, logs)

Agent containers (node + Claude Code CLI)
├── /workspace/chat (RW, per-chat)
├── /workspace/memory (RW, Anthropic Memory Tool)
├── /home/node/.claude (RW, session state)
└── /home/node/.claude/skills (RO, container skills)
```

## Production deployment (Docker Compose)

```sh
# Set host paths in .env:
GOPOD_HOST_DATA_DIR=/opt/gopod/data
GOPOD_HOST_REPO_DIR=/opt/gopod/repo
GOPOD_UID=1000
GOPOD_GID=1000

docker compose build
docker compose up -d
```

gopod runs inside a container managing agent containers as siblings
on the same Docker daemon (docker.sock mount, not docker-in-docker).

## Layout

```
gopod/
├── cmd/gopod/           main binary
├── internal/
│   ├── config/             env loader
│   ├── store/              SQLite persistence
│   ├── telegram/           Telegram frontend
│   ├── runner/             Docker container lifecycle
│   │   ├── mountsec/       mount allowlist validation
│   │   └── chattmpl/       chat workspace templates
│   ├── queue/              per-chat serialization + global cap
│   ├── memory/             embedder + hybrid search + extraction + context compiler + HTTP API
│   ├── scheduler/          cron/interval/once poller
│   ├── control/            Router + slash commands
│   └── log/                SQLite slog handler
├── container/
│   ├── Dockerfile          agent image (node + claude CLI)
│   └── skills/memory/      memory API skill
├── Dockerfile              gopod binary image
├── docker-compose.yml      production deployment
├── docs/                   design documents
└── data/                   runtime state (gitignored)
```

## Design docs

| Doc | What it covers |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | System architecture, package layout, message flow |
| [docs/MEMORY.md](docs/MEMORY.md) | 5-layer memory model with hybrid search |
| [docs/GATEWAY.md](docs/GATEWAY.md) | Secret gateway (MITM proxy + Vault) |
| [docs/ISOLATION.md](docs/ISOLATION.md) | Container security policy |
| [docs/CONTROL.md](docs/CONTROL.md) | Control plane Router design |
| [docs/SKILLS.md](docs/SKILLS.md) | Skill ecosystem |
| [docs/OBSERVABILITY.md](docs/OBSERVABILITY.md) | Prometheus + OTel design |
| [docs/DECISIONS.md](docs/DECISIONS.md) | Architectural decision log (D001–D018) |
| [docs/HANDOFF.md](docs/HANDOFF.md) | Current state for picking up the project |
| [ROADMAP.md](ROADMAP.md) | Milestones with statuses |

## License

TBD.
