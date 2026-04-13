# gopod — Roadmap

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
**they do not encode build order**. Per [D018](docs/DECISIONS.md), gopod
is built in this sequence so that the first end-to-end agent reply uses
Claude Code's web-auth (Pro/Max) inside a container, not a paid API key:

`M0 ✅ → M1 ✅ → M5 ✅ → M6 ✅ → M6.5 ✅ → M3 ✅ → M3.5 ✅ → M4 ✅ → M9 ✅ → M8 ✅ → P1+P2 ✅ → ST1–ST5 ✅ → M7 → M3.6`

M2 (Direct API) is no longer on the critical path; it is opt-in if and only
if `ANTHROPIC_API_KEY` is set in the environment.

## Phase 1 — Core (M0–M9)

| ID | Milestone | Status | Notes |
|----|-----------|--------|-------|
| M0 | Skeleton: `go.mod`, `cmd/gopod/main.go`, `internal/config`, `internal/store` schema, slog wiring | ✅ | sqlite-vec verified at startup (`vec_version=v0.1.6`); ncruces pinned to v0.20.0 per [D017](docs/DECISIONS.md) |
| M1 | Telegram echo: long-poll, default handler stores every message, `/ping` replies | ✅ | `internal/telegram` wraps `go-telegram/bot`; `internal/store/messages.go` + `chats.go` ingest. End-to-end with a real bot token works without an agent |
| M2 | Direct API agent (no container): `anthropic-sdk-go`, single chat, trigger pattern, per-chat session | ⏸️ | **Optional, off the critical path per [D018](docs/DECISIONS.md).** Auto-enabled if `ANTHROPIC_API_KEY` is set; otherwise skipped silently |
| M3 | GroupQueue: per-chat serialization + global concurrency cap + backoff | ✅ | `internal/queue` with per-chat worker, buffered-chan cap (default 3), coalescing, exponential backoff (5s→80s, 5 retries). Telegram default handler enqueues; queue worker runs agent async |
| M3.5 | **Control plane** (`internal/control` Router + Telegram frontend). C1 (Router scaffold) + C2 (fold handlers) done. C3 (CLI frontend), C4 (extended handlers), C5 (logs subsystem) deferred. | ✅ | Router with auth enforcement, all slash commands dispatch through it, setMyCommands built from Router.List(). /login stays as Telegram-side special case (stateful) |
| M3.6 | **Observability** (`internal/observability`: opt-in Prometheus `/metrics` + opt-in OTel OTLP traces; logs are already covered by M3.5/C5). Sub-steps O1–O6 below. See [docs/OBSERVABILITY.md](docs/OBSERVABILITY.md) and [D014](docs/DECISIONS.md). | ⬜ | Deprioritized to end of backlog — personal bot doesn't need metrics/traces yet |
| M4 | Scheduler: cron/interval/once via `robfig/cron/v3`, `/tasks` commands via Router, poller goroutine | ✅ | store CRUD, 60s poller, /tasks add/list/pause/resume/cancel, tasks push through queue same as messages |
| M5 | Container runtime: Docker SDK, mounts, exec attach, idle kill, label-based recovery | ✅ | `internal/runner` has `mountsec` subpackage, three-tier `BuildMounts`, full `BuildContainerArgs` flag assembly, Docker client with `EnsureRunning`/`Exec`/`Stop`/`Remove`/`CleanupLeftovers`, plus the `gopod-agent:latest` image (node:22-slim + claude-code 2.1.100 + git + ripgrep). Integration tests behind `//go:build docker_integration` verified end-to-end. Idle watcher intentionally deferred to M6 wiring |
| M6 | Agent SDK in container: synchronous `claude -p` via `docker exec`, registered_chats CRUD, /register + /whoami slash commands, owner auto-register, auth detection, idle watcher | ✅ | Verified end-to-end: real Pro/Max reply on Telegram. SDK migration (per [D015](docs/DECISIONS.md) wrapper script) deferred until streaming/tools/MCP land. Three runtime fixes: drop bogus `seccomp=default` SecurityOpt, add /home/node tmpfs + HOME env, skip .env mask when RepoRoot/.env absent |
| M6.5 | **Telegram-mediated `/login`.** Interactive OAuth proxy: spawns `claude auth login` with PTY+stdin via Docker SDK, captures URL from stdout, forwards to Telegram, intercepts user's next message as the OAuth code, pipes it to claude's stdin, confirms. Session-expired vs never-logged-in distinction in error replies. 10-min timeout with auto-cleanup. | ✅ | Full onboarding fits inside Telegram — no terminal access needed |
| M7 | IPC: filesystem watcher, container → host messages, task ops, owner gating | ⬜ | |
| M8 | Recovery & polish: cursor backfill, structured SQLite logs + /logs command, Docker Compose production deployment, README rewrite | ✅ | Update offset persisted for restart replay, slog → SQLite handler with D012 redaction, multi-stage Dockerfile + compose with docker.sock mount |
| M9 | Native memory: OpenAI embedder, hybrid FTS5+vec0 search, /remember + /recall, auto-injection into agent prompt via --append-system-prompt | ✅ | Agent gets cross-session memory context automatically. User manages memories via /remember + /recall. Auto-summarize + container skill deferred |

### M3.5 sub-steps (control plane)

| ID | Step | Status |
|----|------|--------|
| C1 | Router scaffold: `Command`, `Caller`, `Perm`, `Response`, `Router.Register/Dispatch`, auth enforcement, unit tests | ✅ |
| C2 | Telegram frontend: `parseSlash`, dispatch wiring, `/ping`, `/whoami`, `/version`, `/help` | ✅ |
| C3 | CLI frontend: argv parser, `serve`/`migrate` reserved subcommands, `--json`, mirror of public commands | ⬜ | Deferred |
| C4 | First batch of real handlers: chats, queue, container, system | ⬜ | Deferred |
| C5 | Logs subsystem: `internal/log/sqlite_handler.go`, retention task, `/logs` and `gopod logs` | ✅ | SQLite slog handler with D012 redaction landed in M8 |

### M3.6 sub-steps (observability)

| ID | Step | Status |
|----|------|--------|
| O1 | `internal/observability` scaffold: `Init`, no-op providers, config loader, shutdown | ⬜ |
| O2 | Metric definitions in one place, registry, `127.0.0.1:9090/metrics` listener gated by `GOPOD_METRICS_ADDR` | ⬜ |
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
| I1 | Reactions (👀👍👎) + typing indicator + reply threading | 1 | ✅ |
| I2 | Image vision + document handling | 1 | ✅ |
| I3 | Voice transcription (Whisper) + TTS replies + `/voice` mode | 1 | ✅ |
| I4 | PDF reader | 1 | ⬜ |
| I5 | Markdown → Telegram HTML formatting + smart chunking | 1 | ✅ |
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
| S1 | Container-skills directory + mount into agent container | ✅ | Mechanism works: `container/skills/memory/SKILL.md` mounted. Per-chat `skills.json` filter deferred |
| S2 | MCP tool server: gopod as MCP stdio server proxying memory/tasks/control HTTP APIs → natively registered Claude tools via `--mcp-config`. Agent sees tools as first-class (no curl). Hybrid approach: HTTP API as backend, thin MCP wrapper as frontend. | ⬜ | |
| S3 | Skill manifest format + validation | ⬜ | |
| S4 | Per-chat skill enable/disable (`/skills enable/disable`) | ⬜ | |
| S5 | Dev-time Claude Code skills in `.claude/skills/` for gopod maintainers | ⬜ | |

---

## Phase 4 — Multi-provider agent support

| ID | Step | Status |
|----|------|--------|
| P1 | AgentProvider interface: refactor runner to call provider methods instead of hardcoded `claude` commands | ✅ | `internal/runner/provider.go` — stateless interface with Name/Image/RunCmd/LoginCmd/etc. Runner calls provider methods everywhere |
| P2 | Claude provider: extract current claude-specific code into provider implementation | ✅ | `internal/runner/provider_claude.go` — `--continue`/`--no-session-persistence`, `.claude.json` restore, auth parsing. Codex provider also landed (`provider_codex.go`) |
| P3 | Codex provider: OpenAI Codex CLI support (separate Docker image, codex-specific flags/auth/sessions) | ⬜ |
| P4 | Per-chat provider config: `/provider claude\|codex` command + `GOPOD_DEFAULT_PROVIDER` env var | ⬜ |
| P5 | Gemini CLI provider: `gemini -p` with Google OAuth (browser link auth like Claude), `--resume latest` for sessions, `~/.gemini/` persisted via bind mount | ⬜ |
| P6 | Goose provider: `goose run -t` — model-agnostic (15+ providers via env), MCP extensible, Rust binary, named sessions | ⬜ |
| P7 | Cline CLI provider: `cline -y` — multi-provider (Anthropic/OpenAI/Google/Bedrock/Azure), gRPC API, standalone since 2.0 | ⬜ |
| P8 | Aider provider: `aider --message --yes` — code editing specialist, 20+ models, official Docker image | ⬜ |
| P9 | API providers (Tier 2): OpenAI API / Ollama / any OpenAI-compatible — same interface, no container, HTTP calls | ⬜ |

## Phase 7 — Streaming output

Real-time agent output to Telegram. Replaces buffered wait-then-wall-of-text.
See [docs/STREAMING.md](docs/STREAMING.md) for full design.

| ID | Step | Status | Notes |
|----|------|--------|-------|
| ST1 | `Docker.ExecStream` — streaming exec with `io.Pipe` + `stdcopy` demux, `StreamHandle` struct | ✅ | `internal/runner/docker.go` |
| ST2 | `Runner.RunStream` — streaming run with `AgentStream` (chunks channel + result channel) | ✅ | `internal/runner/runner.go` |
| ST3 | Telegram helpers — `sendPlaceholder`, `editMessage`, `editMessageHTML` | ✅ | `internal/telegram/streaming.go` |
| ST4 | `runAgentStreaming` — streaming handler loop with 1s ticker, overflow, fallback | ✅ | `internal/telegram/streaming.go` |
| ST5 | Wire into queue handler, config flag (`GOPOD_STREAM_DISABLED`), `maybeVoiceReply` extracted | ✅ | `cmd/gopod/main.go`, `internal/config/config.go` |

**Build order:** ST1 ∥ ST3 → ST2 → ST4 → ST5

ST1 and ST3 are independent and can be built in parallel.

---

## Phase 6 — Secret gateway ([D019](docs/DECISIONS.md))

MITM HTTP proxy + HashiCorp Vault. Agents never see real keys.
See [docs/GATEWAY.md](docs/GATEWAY.md) for full design.

| ID | Step | Status | Notes |
|----|------|--------|-------|
| G1 | `internal/vault` — Vault client: KV v2 read/write, AppRole auth, in-memory cache with TTL, connection health check | ⬜ | Pure Go, no CGO. Uses `hashicorp/vault/api` SDK. Path convention: `secret/data/gopod/{shared,<chat>}/<name>` |
| G2 | `internal/gateway/ca` — Certificate authority: self-signed root CA generation + per-host leaf certs (ECDSA P-256), cert cache (24h TTL), root CA persisted to `${DATA_DIR}/gateway/` | ⬜ | `crypto/x509` + `crypto/ecdsa`, no CGO. CA cert mounted into containers for trust |
| G3 | `internal/gateway` scaffold — HTTP CONNECT proxy: TCP listener, Proxy-Authorization Basic extraction, agent_token lookup, plain TCP tunnel for unknown hosts. No MITM yet | ⬜ | Depends on G1 for token→chat resolution. Test: proxy curl through it, verify tunnel works |
| G4 | MITM TLS interception + header injection — terminate agent TLS with leaf cert from G2, parse HTTP request, apply injection rules (SetHeader/ReplaceHeader by host+path pattern), forward to upstream over fresh TLS, stream response back | ⬜ | Depends on G2+G3. Core of the gateway. Test: inject a fake header, verify upstream receives it |
| G5 | Container wiring — `agent_token` column on `registered_chats`, `ProxyEnvVars()` on AgentProvider, CA cert bind mount, `update-ca-certificates` / `NODE_EXTRA_CA_CERTS` in Dockerfile, remove secret env vars when gateway active | ⬜ | Depends on G4. Modifies `docker_args.go`, `mounts.go`, `container/Dockerfile` |
| G6 | `internal/gateway/policy` — scoped secret resolution: per-chat overrides shadow shared secrets, resolution cache (60s TTL), rate limiting (optional token bucket per agent+host), block rules | ⬜ | Depends on G4. Test: per-chat override shadows shared, unknown host tunnels |
| G7 | Control plane — `/secrets list\|add\|remove\|rotate-token\|test\|status` commands via Router, all OwnerOnly. `/secrets add` prompts for value interactively (like /login flow) | ⬜ | Depends on G5+G6. Telegram-mediated secret management |
| G8 | SQLite fallback backends — Vault Transit encryption (Vault provides encrypt/decrypt, SQLite stores ciphertext), local AES-256-GCM fallback (dev only, key from `GOPOD_SECRET_KEY`). `secrets` table in schema | ⬜ | Depends on G6. For deployments where full Vault KV is overkill |

**Build order:** G1 ∥ G2 → G3 → G4 → G5 ∥ G6 → G7 → G8

G1 and G2 are independent and can be built in parallel.
G5 and G6 are independent once G4 lands.
G8 is optional — full Vault mode (G1–G7) is the primary path.

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
