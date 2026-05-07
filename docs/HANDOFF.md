# gopod — Handoff

**Read this first when picking up the project.** Single source of truth for
"what is the state right now". Always update this file before you stop working.

> Companion docs: [../README.md](../README.md) · [../CLAUDE.md](../CLAUDE.md) · [../ROADMAP.md](../ROADMAP.md) · [ARCHITECTURE.md](ARCHITECTURE.md) · [DECISIONS.md](DECISIONS.md)

---

## Current state

**Phase:** Core complete. gopod is a working personal Telegram
Claude assistant with session continuity, long-term memory, voice,
vision, scheduling, and multi-provider architecture ready for Codex.
**Last updated:** 2026-04-13
**Last working session:** Massive session — 56 commits in one day.

Key work after M9:
- M8: cursor backfill, structured SQLite logs, Docker Compose, README
- I2/I3/I5/I1: image vision, voice+TTS, markdown formatting, reactions
- Session continuity: `--continue` flag, session files persistent via
  bind mount, survive container + gopod restarts
- Session compact: /clear, /compact, auto-compact (turns/interval/daily),
  conversation_summary in memory with reserved Context Compiler slot
- Advanced memory: extraction layer, context compiler, lifecycle
  (status/superseded/dedup), memory HTTP API + container skill
- P1+P2: AgentProvider interface + Claude provider refactor
- WebSearch + standard tools via --allowedTools
- Proactive CLAUDE.md instructions (use tools, store to memory)
- Critical fixes: queue worker context (first msg lost), RunFresh
  --no-session-persistence (session pollution), FTS5 punctuation,
  seccomp, HOME/tmpfs, .claude.json restore, reaction emoji

Build order:
`M0 ✅ → M1 ✅ → M5 ✅ → M6 ✅ → M6.5 ✅ → M3 ✅ → M3.5 ✅ → M4 ✅ → M9 ✅ → M8 ✅ → P1+P2 ✅ → ST1–ST5 ✅ → M7 ✅ → M3.6`

Additional completed work (post-milestone):
- Session compact: /clear, /compact, auto-compact (turns/interval/daily),
  conversation_summary in memory with reserved Context Compiler slot
- Memory HTTP API (`internal/memory/api.go`) + container skill
- Context compiler (`internal/memory/compiler.go`): structured budget
  with 6 slots (summary, pinned, decisions, preferences, facts, fallback)
- Memory extraction layer (`internal/memory/extract.go`)
- AgentProvider interface (`internal/runner/provider.go`) with Claude
  (`provider_claude.go`) and Codex (`provider_codex.go`) implementations

## What's done

- ✅ **M7 — IPC.** internal/ipc package; agent → telegram + scheduler via
  data/ipc/<chat>/{messages,tasks}/. 1s ticker, 3-retry → .failed/,
  retention 200/dir. Spec: docs/superpowers/specs/2026-05-07-m7-ipc-design.md.
- ✅ **M5 — Container runtime landed.** `internal/runner/mountsec`
  subpackage does pure allowlist validation: JSON schema, compiled-in
  blocked patterns (SSH/GPG/AWS/Docker/.env/id_rsa/etc), symlink
  resolution, traversal checks, darwin case-insensitivity, 0600
  file-mode enforcement. `internal/runner/mounts.go` builds the
  three-tier mount list from ISOLATION.md §3: owner gets project root
  RO + store.sqlite RW + .env mask via an EmptyFile bind-mount; non-
  owner gets workspace/memory/ipc/sessions/skills baseline only.
  `internal/runner/docker_args.go` assembles every ISOLATION.md §6
  flag: ReadonlyRootfs, CapDrop=ALL, no-new-privileges, seccomp=default,
  non-root uid, tmpfs trio, resource caps, labels, env allowlist via
  `os.LookupEnv` (D012). `internal/runner/docker.go` wraps the Docker
  SDK with `EnsureRunning`/`Exec`/`Stop`/`Remove`/`ListGopodContainers`
  plus `CleanupLeftovers` in `lifecycle.go` (keeps running containers
  at the current version, stops+removes everything else). Integration
  tests behind `//go:build docker_integration` verified end-to-end
  against Docker Desktop: EnsureRunning idempotency, Exec success +
  non-zero exit, Stop+Remove, and a mixed current-version/stale-version
  cleanup scenario. `container/Dockerfile` builds `gopod-agent:latest`
  from node:22-slim + claude-code + git + ripgrep (Claude Code 2.1.100
  verified inside). `cmd/gopod/main.go` now boots the runner
  subsystem after the store, loads the mount allowlist, runs cleanup,
  and continues gracefully if Docker is unreachable.
- ✅ **M1 — Telegram echo landed.** `internal/telegram` wraps
  `go-telegram/bot` v1.20: `Bot.New` + `Bot.Run(ctx)` with long-poll,
  `defaultHandler` storing every inbound update, `pingHandler`
  registered via `MatchTypeCommand` (catches `/ping@gopodbot` too)
  that stores + replies "pong". Pure helpers (chat JID, display name,
  reply field extraction) are unit-tested. `internal/store/messages.go`
  + `chats.go` provide idempotent ingest:
  `INSERT … ON CONFLICT(chat_jid, tg_message_id) DO NOTHING` for
  re-delivered Telegram updates, and `MAX(...)` semantics on
  `last_message_time` so out-of-order arrivals don't regress it.
  `cmd/gopod/main.go` boots the telegram subsystem in a goroutine
  when `TELEGRAM_BOT_TOKEN` is set; if unset it logs a warning and
  keeps running in store-only mode (preserves M0 behavior). New
  `sync.WaitGroup` drains subsystems before deferred store close.
  `go test ./...` covers config/store/telegram.
- ✅ **M0 — Skeleton landed.** `go.mod` (Go 1.24, autobumped by `go mod tidy`
  to satisfy `tetratelabs/wazero` v1.11; ncruces pinned to v0.20.0
  per [D017](DECISIONS.md)), `internal/config` with env loader + tests,
  `internal/store` opening SQLite via `ncruces/go-sqlite3` with sqlite-vec
  bundled in via the asg017 binding, full schema applied idempotently
  (chats, messages, registered_chats, sessions, scheduled_tasks,
  task_run_logs, router_state, memories, memory_vec vec0, memory_fts5 +
  triggers), runtime check of `vec_version()`, KNN smoke test, and
  `cmd/gopod/main.go` that wires slog (text|json), opens the store,
  blocks on SIGINT/SIGTERM, exits cleanly. `go test ./...` is green.
- ✅ NanoClaw architecture mapped (see [ARCHITECTURE.md §2](ARCHITECTURE.md))
- ✅ Library research: Telegram (`go-telegram/bot`), Claude SDK
  (`character-ai/claude-agent-sdk-go` + `anthropic-sdk-go`), SQLite
  (`ncruces/go-sqlite3` + `sqlite-vec`), cron (`robfig/cron/v3`), Docker
  (`docker/docker/client`)
- ✅ ADRs D001–D018 recorded
- ✅ Memory architecture: 5-layer model, sqlite-vec schema, embedder
  abstraction, auto-ingestion plan ([MEMORY.md](MEMORY.md))
- ✅ Integration triage: Tier 1/2/Skip with effort estimates and an
  implementation order I1–I10 ([INTEGRATIONS.md](INTEGRATIONS.md))
- ✅ Skill ecosystem redesign: container skills + MCP skills + dev skills,
  no branch-merge ([SKILLS.md](SKILLS.md), [D009](DECISIONS.md))
- ✅ Control plane design: `internal/control` Router + Telegram/CLI
  frontends + logs subsystem, slotted as M3.5 with sub-steps C1–C5
  ([CONTROL.md](CONTROL.md), [D011](DECISIONS.md))
- ✅ Secrets policy locked down: env-only, no values in committed files,
  slog redaction ([D012](DECISIONS.md))
- ✅ Container isolation policy: three-tier mounts, allowlist file at
  `${DATA_DIR}/mount-allowlist.json`, compiled-in blocked patterns, RO root
  + dropped caps + non-root uid, `internal/runner/mountsec` subpackage.
  ([ISOLATION.md](ISOLATION.md), [D013](DECISIONS.md))
- ✅ Observability design: opt-in Prometheus `/metrics` + opt-in OTel OTLP
  traces, logs always on via M3.5/C5; slotted as M3.6 with sub-steps O1–O6.
  ([OBSERVABILITY.md](OBSERVABILITY.md), [D014](DECISIONS.md))
- ✅ No socket / HTTP control frontend in v0 (user decision 2026-04-09)
- ✅ Documentation structure: README, CLAUDE.md, ROADMAP, docs/{ARCHITECTURE,
  MEMORY, INTEGRATIONS, SKILLS, CONTROL, ISOLATION, OBSERVABILITY,
  DECISIONS, GLOSSARY, HANDOFF}.md
- ✅ Sanity-check pass on the doc set: stale references, env var prefixes,
  ADR consistency, SKILLS hedge wording all reconciled
- ✅ `.gitignore` for Go + macOS + gopod runtime (data/, *.sqlite, .env)

## What's in progress

**Phase 7 — Streaming output** ([STREAMING.md](STREAMING.md)). ✅ **Done.**

Real-time agent output to Telegram via message editing (~1s updates).
All 5 sub-steps (ST1–ST5) implemented:
- `Docker.ExecStream` + `StreamHandle` (`internal/runner/docker.go`)
- `Runner.RunStream` + `AgentStream`/`RunResult` (`internal/runner/runner.go`)
- Telegram helpers: `sendPlaceholder`/`editMessage`/`editMessageHTML` (`internal/telegram/streaming.go`)
- `runAgentStreaming` with 1s ticker, overflow >4000, fallback to sync (`internal/telegram/streaming.go`)
- Config: `GOPOD_STREAM_DISABLED=1` to disable. Default: streaming on.
- `maybeVoiceReply` extracted for reuse across sync/streaming paths.

**Phase 6 — Secret gateway** ([D019](DECISIONS.md), [GATEWAY.md](GATEWAY.md)).
Design complete. Implementation not started. Second priority.

MITM HTTP CONNECT proxy embedded in gopod + HashiCorp Vault backend.
Replaces env var secret injection — agents never see real keys.
8 sub-steps (G1–G8), G1∥G2 can start in parallel.
See [GATEWAY.md](GATEWAY.md) for full architecture.

## What's next (in order — per [D018](DECISIONS.md))

1. ~~M3 — GroupQueue.~~ ✅ Done.
2. ~~M3.5 — Control plane (C1+C2).~~ ✅ Done. C3/C4/C5 deferred.
3. ~~M4 — Scheduler.~~ ✅ Done.
4. ~~M8 — Recovery & polish.~~ ✅ Done.
5. ~~M9 — Native memory.~~ ✅ Done.
6. ~~P1+P2 — AgentProvider + Claude/Codex providers.~~ ✅ Done.
7. ~~ST1–ST5 — Streaming output (real-time Telegram edits).~~ ✅ Done.
8. ~~**M7 — IPC** (filesystem watcher, container → host messages).~~ ✅ Done.
9. **M3.6 — Observability** (opt-in Prometheus + OTel). Deprioritized —
   personal bot doesn't need metrics/traces yet.

**Optional, off the critical path:** **M2 (Direct API)** auto-enabled
if `ANTHROPIC_API_KEY` is set. Skipped silently otherwise. Operator
currently has no API key, so this stays at ⏸️.

## Right now you can already...

1. Set `TELEGRAM_BOT_TOKEN` + `GOPOD_OWNER_CHAT_ID` in `.env`
2. Build the agent image: `docker build -t gopod-agent:latest -f container/Dockerfile .` (run from repo root; needs submodule `skills/frf-tui` checked out)
3. Run `go run ./cmd/gopod`
4. Send any text message → owner chat auto-registers, container spawns
5. First message gets "not authenticated" reply → tap `/login`
6. Bot sends you the OAuth URL → open it, sign in, copy the code
7. Paste the code back in the chat → bot confirms "Logged in"
8. Send messages → get **real Claude replies** via gopod Telegram bot

No terminal access needed for the entire flow.

Slash commands: `/help` (list all), `/ping` (liveness), `/whoami`
(chat id + registration state), `/login` (interactive OAuth),
`/register <chat_id> <folder>` (owner-only).

Session management: expired tokens produce "session expired, /login
to re-authenticate" instead of the cold-start message. /login is
re-runnable at any time.

Fresh chat workspaces get seeded CLAUDE.md (identity/workspace map) +
memory/MEMORY.md (scratchpad seed). The agent can read and edit both.

The idle watcher kills inactive containers after 30 minutes. Leftover
cleanup on restart removes all containers when running dev builds
(`go run`, version=dev).

## Open questions

- **Control plane open Qs** ([CONTROL.md §13](CONTROL.md)):
  chat-local commands in unregistered chats (silent ignore vs reply),
  `/help` rendering across permission levels, rate-limiting (probably no),
  inline-keyboard confirmations for destructive ops (probably defer).
- ~~**ARCHITECTURE.md drift on mount allowlist location.**~~ Fixed — §3
  now references `${DATA_DIR}/mount-allowlist.json` per ISOLATION.md.
- **`ncruces/go-sqlite3` upgrade path.** Currently pinned to v0.20.0 per
  [D017](DECISIONS.md) because the asg017 sqlite-vec binding hasn't kept
  up with upstream. Watch for either a new asg017 release or a vendored
  sqlite-vec wasm we ship ourselves. Re-bisect when bumping.

### Recently resolved (2026-04-09)

- ✅ **`claude-agent-sdk-go` custom command.** PARTIAL: `Options.CLIPath`
  exists but takes single argv[0], no `Command []string` / `CLIPrefixArgs`.
  Resolution: per-chat wrapper script generated under
  `data/wrappers/<chat>.sh` that `exec`s `docker exec -i <name> claude "$@"`.
  Recorded as [D015](DECISIONS.md). Caveats baked in: explicit `-e VAR`
  allowlist, host-side `Cwd` is ignored (the SDK passes `Options.Cwd` as a
  `--cwd` *flag* interpreted inside the container), `Close()` belt-and-
  suspenders `pkill -INT claude` for Docker's flaky signal forwarding,
  stderr classification by prefix (`Error response from daemon` → docker
  layer, else → claude layer).
- ✅ **Telegram privacy mode.** Confirmed: privacy mode is on by default,
  group-only (1-on-1 and channels are unaffected), toggled via
  `@BotFather` → `/setprivacy`, and **the bot must be removed and re-added
  to every existing group** for the toggle to take effect on those groups.
  gopod setup docs (when M1 lands) will require disabling privacy mode
  because gopod is supposed to see every message in its registered
  chats; the usual user-privacy concern doesn't apply because privileges
  are enforced by `GOPOD_OWNER_CHAT_ID`. No rate-limit penalties for
  disabling; standard Bot API flood limits still apply normally.
- ✅ **Embedding dim default.** Confirmed: OpenAI `dimensions` parameter is
  supported on `text-embedding-3-small` (and `-3-large`, but not on
  `ada-002`). Server-side it does `truncate → L2-renormalize`, so vectors
  are unit-length and cosine == inner product for ranking. MTEB drop from
  1536 → 1024 on `-3-small` is well under one point (MRL training). Locked
  in as default at 1024 dim, recorded as [D016](DECISIONS.md). Client-side
  truncation is banned — always request the target dim from the API. The
  `memories` table stores `(model, dim)` per row so a future migration is
  a clean re-embed pass.

## Blockers

None.

## Nearby work noted but out of scope

(Things you bumped into and want to revisit later — write them here so they
don't get lost.)

_None at the moment. The two big "noted" items (observability, isolation)
both landed as full design docs and ADRs._

## How to update this file

1. **Before starting work:** read this file. Read the docs it points at.
2. **While working:** if you notice something important but unrelated, drop
   it under "Nearby work noted but out of scope".
3. **Before stopping:** update "Current state", "What's done", "What's in
   progress", "What's next", "Open questions", "Blockers". Bump
   `Last updated`. Be specific — "fixed bug" is useless, "added
   `internal/store/messages.go:GetSince` and integration tests" is useful.
4. **After non-trivial decisions:** write an ADR in
   [DECISIONS.md](DECISIONS.md) and link it from here.
