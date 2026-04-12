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

## Build order (reordered by [D018](docs/DECISIONS.md))

The numerical IDs below are stable identifiers cited from many places —
**they do not encode build order**. Per [D018](docs/DECISIONS.md), picoclaw
is built in this sequence so that the first end-to-end agent reply uses
Claude Code's web-auth (Pro/Max) inside a container, not a paid API key:

`M0 ✅ → M1 ✅ → M5 ✅ → M6 ✅ → M6.5 ✅ → M3 ✅ → M3.5 ✅ → M4 ✅ → M3.6 → M7 → M8 → M9`

M2 (Direct API) is no longer on the critical path; it is opt-in if and only
if `ANTHROPIC_API_KEY` is set in the environment.

## Phase 1 — Core (M0–M9)

| ID | Milestone | Status | Notes |
|----|-----------|--------|-------|
| M0 | Skeleton: `go.mod`, `cmd/picoclaw/main.go`, `internal/config`, `internal/store` schema, slog wiring | ✅ | sqlite-vec verified at startup (`vec_version=v0.1.6`); ncruces pinned to v0.20.0 per [D017](docs/DECISIONS.md) |
| M1 | Telegram echo: long-poll, default handler stores every message, `/ping` replies | ✅ | `internal/telegram` wraps `go-telegram/bot`; `internal/store/messages.go` + `chats.go` ingest. End-to-end with a real bot token works without an agent |
| M2 | Direct API agent (no container): `anthropic-sdk-go`, single chat, trigger pattern, per-chat session | ⏸️ | **Optional, off the critical path per [D018](docs/DECISIONS.md).** Auto-enabled if `ANTHROPIC_API_KEY` is set; otherwise skipped silently |
| M3 | GroupQueue: per-chat serialization + global concurrency cap + backoff | ✅ | `internal/queue` with per-chat worker, buffered-chan cap (default 3), coalescing, exponential backoff (5s→80s, 5 retries). Telegram default handler enqueues; queue worker runs agent async |
| M3.5 | **Control plane** (`internal/control` Router + Telegram frontend). C1 (Router scaffold) + C2 (fold handlers) done. C3 (CLI frontend), C4 (extended handlers), C5 (logs subsystem) deferred. | ✅ | Router with auth enforcement, all slash commands dispatch through it, setMyCommands built from Router.List(). /login stays as Telegram-side special case (stateful) |
| M3.6 | **Observability** (`internal/observability`: opt-in Prometheus `/metrics` + opt-in OTel OTLP traces; logs are already covered by M3.5/C5). Sub-steps O1–O6 below. See [docs/OBSERVABILITY.md](docs/OBSERVABILITY.md) and [D014](docs/DECISIONS.md). | ⬜ | Both subsystems are no-op by default; later milestones add their own metrics/spans using helpers from M3.6 |
| M4 | Scheduler: cron/interval/once via `robfig/cron/v3`, `/tasks` commands via Router, poller goroutine | ✅ | store CRUD, 60s poller, /tasks add/list/pause/resume/cancel, tasks push through queue same as messages |
| M5 | Container runtime: Docker SDK, mounts, exec attach, idle kill, label-based recovery | ✅ | `internal/runner` has `mountsec` subpackage, three-tier `BuildMounts`, full `BuildContainerArgs` flag assembly, Docker client with `EnsureRunning`/`Exec`/`Stop`/`Remove`/`CleanupLeftovers`, plus the `picoclaw-agent:latest` image (node:22-slim + claude-code 2.1.100 + git + ripgrep). Integration tests behind `//go:build docker_integration` verified end-to-end. Idle watcher intentionally deferred to M6 wiring |
| M6 | Agent SDK in container: synchronous `claude -p` via `docker exec`, registered_chats CRUD, /register + /whoami slash commands, owner auto-register, auth detection, idle watcher | ✅ | Verified end-to-end: real Pro/Max reply on Telegram. SDK migration (per [D015](docs/DECISIONS.md) wrapper script) deferred until streaming/tools/MCP land. Three runtime fixes: drop bogus `seccomp=default` SecurityOpt, add /home/node tmpfs + HOME env, skip .env mask when RepoRoot/.env absent |
| M6.5 | **Telegram-mediated `/login`.** Interactive OAuth proxy: spawns `claude auth login` with PTY+stdin via Docker SDK, captures URL from stdout, forwards to Telegram, intercepts user's next message as the OAuth code, pipes it to claude's stdin, confirms. Session-expired vs never-logged-in distinction in error replies. 10-min timeout with auto-cleanup. | ✅ | Full onboarding fits inside Telegram — no terminal access needed |
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

### M3.6 sub-steps (observability)

| ID | Step | Status |
|----|------|--------|
| O1 | `internal/observability` scaffold: `Init`, no-op providers, config loader, shutdown | ⬜ |
| O2 | Metric definitions in one place, registry, `127.0.0.1:9090/metrics` listener gated by `PICOCLAW_METRICS_ADDR` | ⬜ |
| O3 | Wire counters/gauges/histograms into `store`, `queue`, `runner`, `telegram`, `control` | ⬜ |
| O4 | OTel scaffold: `Init`, no-op tracer when env unset, exporter selection, redaction wrapper | ⬜ |
| O5 | `runner.run` root span tree + propagation env vars on `docker exec` + MCP spawn | ⬜ |
| O6 | Smoke tests: hit a local Prom, hit a local OTel collector, verify cardinality | ⬜ |

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
