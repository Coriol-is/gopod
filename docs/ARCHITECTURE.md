# gopod — Architecture

> Companion docs: [MEMORY.md](MEMORY.md) · [INTEGRATIONS.md](INTEGRATIONS.md) · [SKILLS.md](SKILLS.md) · [CONTROL.md](CONTROL.md) · [GATEWAY.md](GATEWAY.md) · [DECISIONS.md](DECISIONS.md) · [GLOSSARY.md](GLOSSARY.md)
> Status & next steps: [HANDOFF.md](HANDOFF.md) · [../ROADMAP.md](../ROADMAP.md)

A Go reimplementation of [NanoClaw](https://github.com/nanocoai/nanoclaw) with reduced scope:
**Telegram only**, **no multi-channel registry**, **no OneCLI gateway**, plus a
**simplified skill ecosystem** (container skills + MCP, no branch-merge — see
[SKILLS.md](SKILLS.md)).
Focus is a small, idiomatic Go service that runs a Claude agent per Telegram chat with
the same per-chat isolation guarantees as NanoClaw.

---

## 1. Goal

Run a personal Claude assistant that:

- Listens on a single Telegram bot
- Routes messages to a Claude Agent SDK process per chat
- Keeps each chat's filesystem, memory, and SDK session isolated
- Supports scheduled tasks (cron / interval / one-shot)
- Persists chat history, sessions, tasks, and cursors in SQLite
- Recovers cleanly from crash (cursors, in-flight messages)
- Optionally sandboxes the agent inside a container

Non-goals (vs NanoClaw):

- WhatsApp / Slack / Discord / Gmail / Signal / Emacs / X
- Self-registering channel registry (only Telegram, statically wired)
- OneCLI credential proxy (use plain `.env` / OS keychain)
- The four-type skill ecosystem and `.claude/skills/` apparatus
- Migration tooling (`update-nanoclaw`, `migrate-from-openclaw`)
- macOS Apple Container support (Docker only, optional Podman later)

---

## 2. NanoClaw recap (what we are cloning)

Source: [nanocoai/nanoclaw](https://github.com/nanocoai/nanoclaw). Key components:

| NanoClaw component                  | Role                                                          |
| ----------------------------------- | ------------------------------------------------------------- |
| `src/index.ts`                      | Orchestrator, message loop, state, sessions                   |
| `src/channels/registry.ts` + `*.ts` | Self-registering channel modules, common `Channel` interface  |
| `src/router.ts`                     | XML-wrap inbound, strip `<internal>` outbound                 |
| `src/ipc.ts`                        | Filesystem IPC: container → host messages, tasks, input drain |
| `src/group-queue.ts`                | Per-group serialization, global concurrency limit, backoff   |
| `src/container-runner.ts`           | Spawns Docker container per chat with per-chat mounts         |
| `src/mount-security.ts`             | Allowlist of mountable host paths                             |
| `src/task-scheduler.ts`             | Cron / interval / once tasks, queued through GroupQueue       |
| `src/db.ts`                         | better-sqlite3 schema and queries                             |
| `container/agent-runner/`           | Inside-container TypeScript runner using Claude Agent SDK     |
| `groups/<name>/CLAUDE.md`           | Per-chat identity / memory                                    |

End-to-end flow: Telegram message → channel callback → SQLite store → poll loop →
trigger match → format as `<messages>` XML → GroupQueue → container spawn →
agent-runner reads stdin JSON → SDK `query()` → output framed by markers →
host parses, strips `<internal>`, sends back via channel.

---

## 3. Scope reduction for gopod

| Keep                                                  | Drop                                  | Replace                                                              |
| ----------------------------------------------------- | ------------------------------------- | -------------------------------------------------------------------- |
| Per-chat filesystem isolation                         | Channel registry                      | Single `internal/telegram` package                                   |
| SQLite persistence (chats, messages, tasks, cursors)  | Skill ecosystem                       | Static features compiled in                                          |
| Container-per-chat runtime                            | OneCLI gateway                        | Env vars from `.env` / keychain, injected only into agent process    |
| Trigger pattern (`@assistant`)                        | XML envelope (optional, can simplify) | Plain text or minimal JSON envelope                                  |
| Scheduled tasks (cron/interval/once)                  | Apple Container, multi-runtime        | Docker SDK only, with subprocess fallback                            |
| GroupQueue concurrency model                          | Mount allowlist outside repo          | `${DATA_DIR}/mount-allowlist.json` + compiled-in blocked patterns (see [ISOLATION.md](ISOLATION.md)) |
| Filesystem IPC for container → host (messages/tasks)  | "groups/main" privileged group        | All chats are equal; "owner chat ID" in config gates admin operations |
| Crash recovery via cursors (`last_agent_timestamp`)   | Pre-compact transcript archiving      | Optional, behind a flag                                              |

The "main group is privileged" idea collapses into a single `GOPOD_OWNER_CHAT_ID`
env var. That chat can schedule tasks for any other chat and register new chats;
others can only schedule for themselves.

---

## 4. Tech stack

### 4.1 Telegram client

**Choice:** [`github.com/go-telegram/bot`](https://github.com/go-telegram/bot)

- Zero external dependencies
- Bot API 9.5 (March 2026), actively maintained, listed by Telegram officially
- Supports both long polling (`b.Start(ctx)`) and webhooks (`b.StartWebhook(ctx)`)
- Handler model: `RegisterHandler(HandlerTypeMessageText, "...", MatchType*, fn)`
- Middleware via `bot.WithMiddlewares(...)`
- `WithDefaultHandler` lets us catch every update and dispatch ourselves (preferred —
  trigger-pattern matching needs full message text, not Telegram command parsing)

Alternatives considered:

- `mymmrac/telego` — also good, uses fasthttp; extra deps
- `mr-linch/go-tg`, `PaulSonOfLars/gotgbot` — viable but smaller community
- `go-telegram-bot-api/telegram-bot-api` — **avoid**, unmaintained since 2021

**Privacy mode note:** by default Telegram bots in groups only see commands, mentions,
and replies. To make trigger words like `@assistant blah` work as in NanoClaw, the user
must either disable privacy mode in BotFather or always mention the bot. Document this
in setup.

### 4.2 Claude SDK

Two viable paths. gopod will support **both**, choosable per chat:

**Path A — Direct API (`anthropic-sdk-go`)**

- Official, stable v1, latest `v1.33.0` (Apr 2026)
- `go get github.com/anthropics/anthropic-sdk-go`
- Native tool use, beta agents API, message batching
- Requires Go 1.22+
- Pros: no Claude Code CLI dependency, smallest container, fully under our control
- Cons: we re-implement the agent loop, file tools, todo tracking, etc.
- Use case: simple chat (no filesystem agent loop). Cheapest to operate.

**Path B — Agent SDK via Claude Code CLI (`character-ai/claude-agent-sdk-go`)**

- `go get github.com/character-ai/claude-agent-sdk-go`
- Idiomatic Go (channels, context, interfaces)
- `Agent` type wraps the Claude Code CLI; `APIAgent` calls API directly
- Built-in tool registry, hooks, MCP, session resume by `SessionID`, retries, budgets
- Use case: full agent with Read/Write/Bash/Edit/Grep tools — exactly what NanoClaw uses
- Cons: requires `claude` CLI inside container; bigger image

Alternative wrappers (`schlunsen/`, `M1n9X/`, `severity1/`, `panbanda/`) — all
unofficial Python ports. character-ai's is the most idiomatic Go and the one to start
with.

**Recommendation for v0:** ship **Path B** (CLI agent in container) — it preserves
NanoClaw's "agent with full file tools per chat" semantics. Add **Path A** as an
opt-in `mode = "api"` for cheap chats later.

### 4.3 SQLite

**Choice:** [`github.com/ncruces/go-sqlite3`](https://github.com/ncruces/go-sqlite3)

- Pure Go via WASM (no CGO) — keeps cross-compilation and container builds simple
- **Full `sqlite-vec` support** via the official
  [`sqlite-vec-go-bindings/ncruces`](https://github.com/asg017/sqlite-vec-go-bindings)
  WASM binary, including the `vec0` virtual table for KNN
- Standard `database/sql` driver

This driver was chosen specifically to enable native vector search alongside
relational data — see [`MEMORY.md`](MEMORY.md) for the full memory architecture.

Alternatives considered:

- `modernc.org/sqlite` — pure Go, very popular, **but no native sqlite-vec
  support** (only third-party `viant/sqlite-vec` with limited cosine/l2 helpers
  and no `vec0` virtual table). Rejected because it forces us to manage the
  ANN index ourselves.
- `mattn/go-sqlite3` + sqlite-vec CGO bindings — full feature, but reintroduces
  CGO into the build.

Schema mirrors NanoClaw's `src/db.ts`:

```sql
CREATE TABLE chats (
  jid TEXT PRIMARY KEY,             -- "tg:<chat_id>"
  name TEXT,
  last_message_time INTEGER,        -- unix ms
  is_group INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_jid TEXT NOT NULL,
  tg_message_id INTEGER NOT NULL,
  sender TEXT NOT NULL,             -- tg user id or "bot"
  sender_name TEXT,
  content TEXT NOT NULL,
  timestamp INTEGER NOT NULL,       -- unix ms
  is_from_me INTEGER NOT NULL DEFAULT 0,
  is_bot_message INTEGER NOT NULL DEFAULT 0,
  reply_to_tg_message_id INTEGER,
  reply_to_content TEXT,
  reply_to_sender_name TEXT,
  UNIQUE(chat_jid, tg_message_id)
);
CREATE INDEX idx_messages_chat_ts ON messages(chat_jid, timestamp);

CREATE TABLE registered_chats (
  jid TEXT PRIMARY KEY,
  name TEXT,
  folder TEXT NOT NULL UNIQUE,      -- chats/<folder>
  trigger_pattern TEXT,
  requires_trigger INTEGER NOT NULL DEFAULT 1,
  is_owner INTEGER NOT NULL DEFAULT 0,
  added_at INTEGER NOT NULL
);

CREATE TABLE sessions (
  chat_folder TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE scheduled_tasks (
  id TEXT PRIMARY KEY,
  chat_folder TEXT NOT NULL,
  chat_jid TEXT NOT NULL,
  prompt TEXT NOT NULL,
  schedule_type TEXT NOT NULL,      -- 'cron' | 'interval' | 'once'
  schedule_value TEXT NOT NULL,
  next_run INTEGER,                 -- unix ms
  last_run INTEGER,
  status TEXT NOT NULL DEFAULT 'active',
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_tasks_due ON scheduled_tasks(status, next_run);

CREATE TABLE task_run_logs (
  task_id TEXT NOT NULL,
  run_at INTEGER NOT NULL,
  duration_ms INTEGER,
  status TEXT NOT NULL,
  result TEXT,
  error TEXT,
  turn_id INTEGER                   -- turns.id of the run that executed it
);

CREATE TABLE turns (
  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
  source             TEXT NOT NULL,
  source_id          TEXT NOT NULL,
  chat_folder        TEXT NOT NULL,
  chat_id            INTEGER NOT NULL,
  tg_message_id      INTEGER NOT NULL DEFAULT 0,
  is_owner           INTEGER NOT NULL DEFAULT 0,
  is_voice           INTEGER NOT NULL DEFAULT 0,
  text               TEXT NOT NULL,
  file_path          TEXT,
  status             TEXT NOT NULL,
  attempts           INTEGER NOT NULL DEFAULT 0,
  placeholder_msg_id INTEGER NOT NULL DEFAULT 0,
  reply_msg_id       INTEGER NOT NULL DEFAULT 0,
  error              TEXT,
  created_at         INTEGER NOT NULL,
  started_at         INTEGER,
  finished_at        INTEGER,
  UNIQUE(source, source_id)
);
CREATE INDEX idx_turns_status ON turns(status);

CREATE TABLE router_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
-- keys: 'last_timestamp', 'last_agent_ts:<chat_jid>'
```

`turns` is the durable queue: one row per requested agent run, written before dispatch and finalized after. `UNIQUE(source, source_id)` deduplicates Telegram redeliveries and re-fired scheduler slots. Boot recovery replays `pending`, `running` and `interrupted` rows; see `docs/superpowers/specs/2026-10-03-durable-turns-design.md`.

Plus the memory tables (`memories`, `memory_vec` virtual table backed by
`sqlite-vec`, `memory_fts` for hybrid lexical search) — see
[MEMORY.md §1 Layer 3](MEMORY.md). Everything lives in the **same**
`data/store.sqlite` file. One file = one backup.

### 4.4 Cron / scheduling

**Choice:** [`github.com/robfig/cron/v3`](https://pkg.go.dev/github.com/robfig/cron/v3)

- v3 cleaned up timezone handling, supports `CRON_TZ=Asia/Tokyo 30 04 * * *` form
  and `WithLocation(*time.Location)` per-scheduler
- Parser is reusable (`cron.NewParser(...)`) so we can validate user-supplied
  expressions before persisting them
- Standard 5-field cron + named days

For interval and once schedules we don't need a library — just `time.Timer` /
direct `time.Time` math. The scheduler polls SQLite every 60s (mirrors
`SCHEDULER_POLL_INTERVAL`), computes `next_run` after each run, and pushes work
through the same per-chat queue as messages.

### 4.5 Container runtime

**Choice:** Docker via [`github.com/docker/docker/client`](https://pkg.go.dev/github.com/docker/docker/client)

- Native Go SDK, supports `WithAPIVersionNegotiation()` for cross-version
- `ContainerCreate` + `ContainerAttach({Stdin, Stdout, Stderr, Stream: true})` is
  exactly the pattern NanoClaw uses (`docker run -i`) — agent reads stdin JSON, host
  streams stdout
- Multiplexed stdout/stderr demultiplexed via
  `github.com/docker/docker/pkg/stdcopy.StdCopy`
- Bind mounts via `mount.Mount{Type: mount.TypeBind, Source, Target, ReadOnly}`

Fallback: subprocess mode (no container). When `GOPOD_NO_CONTAINER=1`, spawn
the Claude CLI directly with a working directory under `chats/<folder>/`.
Useful for dev and for users who don't want Docker. Document the lost isolation.

### 4.6 Misc

- Config: `github.com/joho/godotenv` (or just `os.Getenv`)
- Logging: `log/slog` (stdlib)
- Validation: hand-rolled regex for chat folder names

---

## 5. Architecture overview

```
                    ┌──────────────────────────────────────┐
                    │            gopod process          │
                    │  ┌────────────────────────────────┐  │
[Telegram] ──────►  │  │ telegram.Bot (long poll)       │  │
  Update             │  │   defaultHandler →             │  │
                    │  │     store.SaveMessage          │  │
                    │  └────────────────────────────────┘  │
                    │              │                        │
                    │              ▼                        │
                    │  ┌────────────────────────────────┐  │
                    │  │ loop.MessagePoller (every 2s)  │  │
                    │  │   getNew → triggerMatch        │  │
                    │  │   → queue.Enqueue(chat)        │  │
                    │  └────────────────────────────────┘  │
                    │              │                        │
                    │              ▼                        │
                    │  ┌────────────────────────────────┐  │
                    │  │ queue.GroupQueue               │  │
                    │  │   max N concurrent containers  │  │
                    │  └────────────────────────────────┘  │
                    │              │                        │
                    │              ▼                        │
                    │  ┌────────────────────────────────┐  │
                    │  │ runner.Run(chat, prompt)       │  │
                    │  │   skills.PrepareForChat        │──┼──► spawn MCP servers
                    │  │   memory.ToolsForChat          │  │
                    │  │   formatPrompt → spawn agent   │  │
                    │  │   demux stdout → parse markers │  │
                    │  └────────────────────────────────┘  │
                    │      │          │            ▲       │
                    │      ▼          ▼            │       │
                    │  ┌────────┐ ┌────────┐ ┌──────────┐  │
                    │  │ Docker │ │ memory │ │ ipc.     │  │
                    │  │ client │ │ store  │ │ Watcher  │  │
                    │  │        │ │ (vec0+ │ │ (poll fs)│  │
                    │  │        │ │  fts5) │ │          │  │
                    │  └────────┘ └────────┘ └──────────┘  │
                    │      │                       ▲       │
                    └──────┼───────────────────────┼───────┘
                           │                       │
              spawn        ▼                       │  fs files
                    ┌─────────────────────────┐    │
                    │ container (per chat)    │    │
                    │  ─ /workspace/chat (RW) │◄───┘
                    │  ─ /workspace/memory    │  outbound msgs,
                    │  ─ /workspace/ipc       │  task ops
                    │  ─ /home/node/.claude/  │
                    │      skills/<…>  ◄─── container skills (RO mount)
                    │  ─ Claude Code CLI      │
                    │     ↕ MCP stdio ────────┼──► gopod MCP skills
                    │     (agent loop)        │
                    └─────────────────────────┘
                           │
                           ▼
                    ┌──────────────────────┐
                    │ telegram.Bot.Send    │
                    │  (back to host)      │
                    └──────────────────────┘
                           │
                           ▼
                       [Telegram]
```

### 5.1 Module / package layout

```
gopod/
├── go.mod
├── cmd/
│   └── gopod/
│       └── main.go              # wires config + components, runs
├── internal/
│   ├── config/
│   │   └── config.go            # env, .env, defaults, validation
│   ├── store/
│   │   ├── store.go             # *sql.DB wrapper
│   │   ├── schema.go            # CREATE TABLEs + migrations
│   │   ├── messages.go          # SaveMessage, GetNewMessages, GetSince
│   │   ├── chats.go             # RegisterChat, ListChats
│   │   ├── tasks.go             # CRUD + GetDue
│   │   ├── sessions.go          # GetSession, SetSession
│   │   └── state.go             # router_state KV
│   ├── telegram/
│   │   ├── bot.go               # wraps go-telegram/bot, owner gating
│   │   ├── handler.go           # default handler → store.SaveMessage
│   │   └── send.go              # SendText, SendChunked, SendTyping
│   ├── trigger/
│   │   └── trigger.go           # buildPattern + match (regex)
│   ├── prompt/
│   │   └── format.go            # XML envelope or plain (configurable)
│   ├── queue/
│   │   └── queue.go             # GroupQueue (per-chat serialization, global cap)
│   ├── runner/
│   │   ├── runner.go            # Run(chat, prompt) → reply
│   │   ├── docker.go            # Docker SDK spawn + attach + demux
│   │   ├── subprocess.go        # GOPOD_NO_CONTAINER fallback
│   │   ├── markers.go           # OUTPUT_START/OUTPUT_END parsing
│   │   └── mounts.go            # per-chat mount construction + safety
│   ├── ipc/
│   │   ├── watcher.go           # poll /data/ipc/<chat>/messages, /tasks, /input
│   │   ├── messages.go          # outbound msg files → telegram.Send
│   │   └── tasks.go             # schedule/pause/resume/cancel/update
│   ├── scheduler/
│   │   └── scheduler.go         # robfig/cron + interval + once, polls store
│   ├── chatfolder/
│   │   └── chatfolder.go        # validate/sanitize folder names, paths
│   ├── control/                 # see CONTROL.md (M3.5)
│   │   ├── control.go           # Router, Command, Caller, Perm, Response, Deps
│   │   ├── parse.go             # parseSlash + parseArgv
│   │   ├── help.go              # /help and `gopod help` rendering
│   │   ├── frontend_telegram.go
│   │   ├── frontend_cli.go
│   │   └── handlers/            # one file per command group
│   │       ├── system.go        # ping, whoami, help, version, system.*
│   │       ├── chats.go
│   │       ├── container.go
│   │       ├── queue.go
│   │       ├── tasks.go
│   │       ├── skills.go
│   │       ├── memory.go
│   │       ├── logs.go
│   │       ├── model.go
│   │       └── debug.go
│   ├── log/                     # see CONTROL.md §9
│   │   ├── log.go               # init slog with multi-handler
│   │   ├── sqlite_handler.go    # slog.Handler writing to logs table
│   │   ├── redact.go            # secret-name redaction (D012)
│   │   └── retention.go         # daily cleanup task
│   ├── memory/                  # see MEMORY.md
│   │   ├── memory.go            # Store interface (add, search, list, delete)
│   │   ├── sqlite.go            # sqlite-vec backed implementation
│   │   ├── schema.go            # memories + memory_vec + memory_fts
│   │   ├── embed.go             # embedding provider abstraction
│   │   ├── embed_openai.go
│   │   ├── embed_voyage.go
│   │   ├── embed_ollama.go
│   │   ├── tools.go             # Claude tool definitions (memory_*)
│   │   └── ingest.go            # auto-summarize + index after N messages
│   ├── skills/                  # see SKILLS.md
│   │   ├── skills.go            # Loader, allow/deny filter, PrepareForChat()
│   │   ├── container.go         # Type 1: bind-mount enumeration
│   │   ├── mcp.go               # Type 2: manifest parsing, lifecycle, env subst
│   │   ├── mcp_stdio.go         # stdio transport
│   │   ├── mcp_http.go          # http transport
│   │   ├── registry.go          # in-memory map of loaded skills per chat
│   │   └── cli.go               # `gopod skills *` subcommands
│   └── recover/
│       └── recover.go           # restore cursors, replay pending on boot
├── container/
│   ├── Dockerfile               # node:22-slim + claude CLI. No TS shim — the
│   │                            #  Go runner talks to claude via docker exec.
│   ├── entrypoint.sh
│   └── skills/                  # Type 1 skills: SKILL.md directories,
│       └── <name>/              #  bind-mounted into agent container at
│           ├── SKILL.md         #  /home/node/.claude/skills/<name>/
│           ├── scripts/
│           ├── references/
│           └── assets/
├── skills/
│   └── mcp/                     # Type 2 skills: MCP server manifests,
│       └── <name>/              #  spawned per chat or globally
│           └── manifest.json
├── .claude/
│   └── skills/                  # Type 3 skills: dev-time slash commands
│       └── <name>/              #  (release, check, handoff, …)
│           └── SKILL.md
├── chats/                       # one dir per registered chat (gitignored)
│   └── <folder>/
│       ├── CLAUDE.md            # per-chat identity (templated on register)
│       ├── memory/              # Layer 2 scratchpad (Anthropic Memory Tool)
│       ├── skills.json          # per-chat allow/deny filter for skills
│       └── conversations/       # archived transcripts (optional)
├── data/                        # runtime state (gitignored)
│   ├── sessions/<folder>/.claude/   # Claude CLI session jsonl
│   ├── ipc/<folder>/{messages,tasks,input,_close}
│   └── store.sqlite             # chats, messages, tasks, sessions, memories, vec0, fts5
├── README.md
├── CLAUDE.md
├── ROADMAP.md
└── docs/
    ├── ARCHITECTURE.md           # this file
    ├── MEMORY.md
    ├── INTEGRATIONS.md
    ├── SKILLS.md
    ├── CONTROL.md
    ├── DECISIONS.md
    ├── HANDOFF.md
    └── GLOSSARY.md
```

**No TypeScript in this repo.** The agent inside the container is the `claude`
CLI itself, talked to from Go via `docker exec` using
`character-ai/claude-agent-sdk-go`'s `Client` type with a custom command. See
[D002](DECISIONS.md). The Claude Code tool ecosystem (Read/Write/Bash/Edit/Grep,
plus MCP) is provided by the CLI natively — gopod doesn't need to
re-implement any of it.

### 5.2 Runner + container interaction

The host runner needs to talk to a `claude` CLI living inside the container.
Two possible boundaries:

**Option 1 — `docker exec` with attached stdio (chosen)**

1. Container is long-lived per chat (started on first message, idle-killed after
   `GOPOD_IDLE_TIMEOUT`).
2. Before each turn the runner calls `skills.PrepareForChat(chatFolder)` and
   `memory.ToolsForChat(chatFolder)` — see steps below.
3. Host calls `ContainerExecCreate` + `ContainerExecAttach` to start one
   `claude` invocation per turn.
4. Stdin/stdout streamed; `stdcopy.StdCopy` demultiplexes.
5. `character-ai/claude-agent-sdk-go`'s `Client` is configured with:
   - a custom command that runs `docker exec -i <name> claude ...` instead of local `claude`,
   - the `memory_*` tools registered in its `ToolRegistry`,
   - any MCP servers from enabled skills connected via the SDK's MCP support.

This matches NanoClaw's "container per chat, agent invoked many times" model
without spawning a new container per message.

**Per-turn preparation steps (`runner.Run`):**

```
1. queue.Acquire(chatFolder)                          // serialize per chat
2. skills.PrepareForChat(chatFolder)
     → enumerate container/skills/* → filter by chats/<f>/skills.json
     → enumerate skills/mcp/* → filter by scope + skills.json
     → spawn MCP servers (per-chat lifecycle), reuse daemons
     → returns mounts, mcpServers, allowedToolPrefixes
3. memory.ToolsForChat(chatFolder)
     → returns []ToolDefinition for memory_search, memory_add, …
4. EnsureContainer(chatFolder, mounts)                // start if not running
5. msgs := store.GetMessagesSince(jid, lastTs, MAX)
6. prompt := prompt.Format(msgs, tz)
7. agent := claudeagent.NewClient(claudeagent.Options{
       Command:  "docker", Args: ["exec","-i",containerName,"claude",...],
       Tools:    memoryTools ∪ mcpTools,
       MaxTurns: N,
       SessionID: store.GetSession(chatFolder),
   })
8. for event := range agent.Run(ctx, prompt):
       parse markers → strip <internal> → telegram.Send (chunked)
9. store.SetSession(chatFolder, agent.SessionID)
10. queue.Release(chatFolder)
```

Steps 2 and 3 are the **only** new work compared to NanoClaw's runner. Both
are scoped per chat, both enforce authorization by path, both degrade
gracefully (missing skills/memory just means fewer tools registered).

**Option 2 — one container per message (NanoClaw's current model)**

Simpler, more wasteful. Skip for gopod v0 unless idle-management proves
fiddly.

### 5.3 IPC (container → host)

Same filesystem-based approach as NanoClaw, scoped per chat:

```
data/ipc/<chat_folder>/
  messages/   *.json   {chatJid, text}     written by agent, polled by host
  tasks/      *.json   {op, args, ...}     scheduling ops
  input/      *.json   text                follow-up messages from host → agent
  _close                                   sentinel: agent should exit
```

Authorization is enforced by **path** (which chat folder the file came from),
never trusted from JSON content. The owner chat's IPC dir is the only place
that may target other chats.

`ipc.Watcher` is one goroutine that walks all `data/ipc/*/messages` and
`data/ipc/*/tasks` every ~250ms via `time.Ticker`. Files are renamed to a
`.processed/` subdir (or deleted) after handling, keeping the watcher
idempotent across restarts.

### 5.4 Crash recovery

On startup:

1. Load `router_state.last_timestamp` from SQLite.
2. For each registered chat, load `last_agent_ts:<jid>` (or backfill from the
   last bot message timestamp if missing — same fallback as NanoClaw).
3. Walk `data/ipc/*/messages` and replay any leftover outbound files.
4. `scheduler.RecoverDue()` runs any tasks whose `next_run` is in the past
   (subject to a `MAX_BACKLOG` clamp to avoid stampedes).
5. Container leftovers from a previous run are listed and stopped (label
   `gopod.chat=<folder>`).

### 5.5 Concurrency model

`queue.GroupQueue` mirrors `src/group-queue.ts`:

- Per-chat state: `active`, `pendingMessages bool`, `pendingTasks []TaskRef`,
  `retryCount int`.
- A buffered `chan struct{}` of size `MAX_CONCURRENT_CONTAINERS` enforces the
  global cap.
- One goroutine per active chat; coalesces multiple incoming messages while
  the agent is busy ("the agent will read all unread on its next turn"),
  avoiding queue blowup.
- Failure → exponential backoff (5s, 10s, 20s, 40s, 80s, max 5 retries).

---

## 6. Message flow (end-to-end)

```
1. User: "@gopod what's 2+2?" in Telegram group
2. go-telegram bot's defaultHandler fires
3. telegram.handler stores via store.SaveMessage(...)
4. loop.MessagePoller (2s tick) calls store.GetNewMessages(lastTimestamp)
5. trigger.Match("@gopod ...") → true
6. queue.Enqueue(chatJid, MessageCheck)
7. queue worker spawns runner.Run(chat)
8. runner reads last N messages via store.GetMessagesSince(...)
9. prompt.Format(messages, tz) → optional <messages> XML envelope
10. runner.docker.EnsureContainer(chatFolder) — start if not running
11. runner.docker.ExecAttach(claude, stdinJSON) via character-ai SDK Client
12. Claude CLI runs agent loop, may use Read/Write/Bash inside container
13. Agent emits framed result on stdout: ---GOPOD_OUT_START---{...}---GOPOD_OUT_END---
14. runner parses, applies stripInternalTags
15. telegram.Send(chatID, replyText) — chunked if > 4096 chars
16. store.UpdateLastAgentTs(chatJid, now) → persisted in router_state
17. runner exits, queue worker checks pendingMessages/pendingTasks; loop
```

---

## 7. Per-chat isolation

| Resource              | Per-chat path                                |
| --------------------- | -------------------------------------------- |
| Working directory     | `chats/<folder>/`                            |
| CLAUDE.md             | `chats/<folder>/CLAUDE.md`                   |
| Claude CLI sessions   | `data/sessions/<folder>/.claude/`            |
| IPC namespace         | `data/ipc/<folder>/`                         |
| Conversation archives | `chats/<folder>/conversations/` (optional)   |
| Container             | `gopod-<folder>` (label: gopod.chat=…) |
| Mounts inside         | `/workspace/chat` (RW), `/workspace/ipc` (RW), `/home/node/.claude` (RW). Owner chat additionally gets `/workspace/store` (RW for SQLite) and project root RO. |

Folder name validation: `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`, plus a reserved set
(`global`, `system`, `..`, etc.). Same as NanoClaw `src/group-folder.ts`.

---

## 8. Configuration

Single `.env` (or env vars):

```
TELEGRAM_BOT_TOKEN=...
ANTHROPIC_API_KEY=...
GOPOD_OWNER_CHAT_ID=123456789       # the "main" chat
GOPOD_TRIGGER=@gopod              # default trigger word
GOPOD_TIMEZONE=Europe/Berlin         # falls back to system TZ
GOPOD_POLL_INTERVAL=2s
GOPOD_SCHEDULER_INTERVAL=60s
GOPOD_IDLE_TIMEOUT=30m
GOPOD_CONTAINER_TIMEOUT=30m
GOPOD_MAX_CONCURRENT=5
GOPOD_MAX_MESSAGES_PER_PROMPT=10
GOPOD_DATA_DIR=./data
GOPOD_CHATS_DIR=./chats
GOPOD_CONTAINER_IMAGE=gopod-agent:latest
GOPOD_NO_CONTAINER=0                 # 1 = subprocess mode
```

`GOPOD_OWNER_CHAT_ID` replaces NanoClaw's `is_main` flag. The single owner
chat is the only place that can:

- Register new chats
- Schedule tasks for other chats
- Read the project root inside the container
- Query the SQLite store directly from inside the agent

---

## 9. Implementation notes

The big architectural calls have been promoted to ADRs in
[DECISIONS.md](DECISIONS.md) (D001–D010). What remains here is a list of
implementation details that don't rise to the level of an ADR but that you'll
want to know before writing code in the relevant package.

1. **Prompt envelope: XML.** NanoClaw uses
   `<messages><message ...>...</message></messages>` because multiple senders
   matter in groups. gopod keeps the XML envelope for the same reason.

2. **Recovering "we already replied to this".** Telegram does mark bot
   messages (`from.is_bot`), but our own bot's outgoing messages don't come
   back as updates to the same bot — so we have to log them ourselves at send
   time (`telegram.Send` writes a row to `messages` with `is_from_me=1`).
   Cursor recovery uses `last_agent_ts:<jid>` from `router_state`.

3. **Owner chat & SQLite mount risk.** Owner chat container mounts
   `data/store.sqlite` RW, which means a misbehaving agent in the owner chat
   could corrupt the store. Acceptable for personal use; non-owner chats
   never see the store. If this bites, switch the owner chat to a read-only
   mount and route writes through the host via IPC.

4. **Markdown vs Telegram MarkdownV2.** Claude emits Markdown freely.
   Telegram MarkdownV2 needs escaping of `_*[]()~`>#+-=|{}.!`. v0 ships
   plain-text fallback (lossy but never breaks); upgrade to a goldmark-based
   MarkdownV2 renderer when noise becomes annoying. See
   [INTEGRATIONS.md #7](INTEGRATIONS.md).

5. **Long messages.** Telegram limit is 4096 chars. `telegram.Send` chunks
   at safe boundaries: paragraphs > sentences > hard cut.

6. **Telegram privacy mode.** By default Telegram bots in groups only see
   commands, mentions, and replies. The setup docs need to call this out
   explicitly when M1 lands — either require BotFather privacy-mode disable,
   or document the tradeoff.

7. **`character-ai/claude-agent-sdk-go` Client custom command.** D003
   assumes the SDK lets us override the executable so we can point it at
   `docker exec -i <name> claude`. This is the one library assumption that
   needs to be verified before M6 starts. Note in HANDOFF.md when verified.

---

## 10. v0 milestones

1. **M0 — skeleton**
   `go.mod`, `cmd/gopod/main.go`, `internal/config`, `internal/store` with
   schema + migrations, opens SQLite, starts `slog`.

2. **M1 — Telegram echo**
   `internal/telegram` long-polls, default handler stores every message,
   `/ping` replies. No agent yet.

3. **M2 — direct API agent (no container)**
   `internal/runner` calls `anthropic-sdk-go` directly; one chat folder, no
   tools, just text in/text out. Trigger pattern + per-chat session.

4. **M3 — group queue**
   `internal/queue` with global cap and per-chat serialization. Multiple chats
   in flight.

5. **M4 — scheduler**
   `internal/scheduler` over robfig/cron + interval/once. Owner can `/schedule`
   from chat (parsed by a small command handler).

6. **M5 — container runtime**
   `internal/runner/docker.go`, mounts, exec attach, idle kill, label-based
   recovery. `GOPOD_NO_CONTAINER=1` keeps M2 path alive.

7. **M6 — agent SDK in container**
   Switch from direct API to `character-ai/claude-agent-sdk-go` `Client` over
   `docker exec ... claude`. Real Read/Write/Bash inside container.

8. **M7 — IPC**
   `internal/ipc` watches `data/ipc/<chat>/`. Container can send messages to
   other chats (owner only) and schedule tasks.

9. **M8 — recovery & polish**
   Cursor backfill, leftover-container cleanup, exponential backoff,
   structured logs, README + Compose example.

10. **M9 — native memory** (see [MEMORY.md](MEMORY.md))
    sqlite-vec tables, embedding provider, `memory_*` tools wired into the
    agent, auto-ingestion every N messages, native Anthropic Memory Tool
    backed by `chats/<folder>/memory/`.

---

## 11. Sources

### Chosen libraries

- [anthropics/anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go) — official Go SDK, v1.33.0 (Apr 2026), Go 1.22+
- [Claude API Docs — Go SDK](https://platform.claude.com/docs/en/api/sdks/go)
- [character-ai/claude-agent-sdk-go](https://github.com/character-ai/claude-agent-sdk-go) — idiomatic Go agent SDK, CLI + APIAgent modes ([pkg.go.dev](https://pkg.go.dev/github.com/character-ai/claude-agent-sdk-go))
- [go-telegram/bot](https://github.com/go-telegram/bot) — zero-deps Telegram Bot API framework, Bot API 9.5
- [robfig/cron v3](https://pkg.go.dev/github.com/robfig/cron/v3) — cron parser/runner with `CRON_TZ` and `WithLocation`
- [docker/docker/client](https://pkg.go.dev/github.com/docker/docker/client) — Docker SDK for Go
- [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) — pure-Go (WASM) SQLite driver, full sqlite-vec support
- [asg017/sqlite-vec](https://github.com/asg017/sqlite-vec) — vector search SQLite extension

### Alternatives considered (not chosen — see DECISIONS.md)

- [schlunsen/claude-agent-sdk-go](https://github.com/schlunsen/claude-agent-sdk-go) — alternative port from Python SDK
- [mymmrac/telego](https://github.com/mymmrac/telego) — alternative Telegram lib, fasthttp-based
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — pure-Go SQLite, but no native sqlite-vec support ([D004](DECISIONS.md))
- [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3) — SQLite via CGO ([D004](DECISIONS.md))
- [Go SQLite benchmarks (cvilsmeier)](https://github.com/cvilsmeier/go-sqlite-bench)

### Reference

- [Go Docker SDK raw stdio handling — addshore](https://addshore.com/2021/01/go-docker-sdk-raw-terminal-ctrlc-handling/)
- [Telegram Bot API — bots/features (privacy mode)](https://core.telegram.org/bots/features)
- NanoClaw source: https://github.com/nanocoai/nanoclaw (`src/index.ts`, `src/channels/registry.ts`, `src/router.ts`, `src/ipc.ts`, `src/group-queue.ts`, `src/container-runner.ts`, `src/mount-security.ts`, `src/task-scheduler.ts`, `src/db.ts`, `container/agent-runner/`)
