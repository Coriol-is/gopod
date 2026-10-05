# gopod — Handoff

**Read this first when picking up the project.** Single source of truth for
"what is the state right now". Always update this file before you stop working.

> Companion docs: [../README.md](../README.md) · [../CLAUDE.md](../CLAUDE.md) · [../ROADMAP.md](../ROADMAP.md) · [ARCHITECTURE.md](ARCHITECTURE.md) · [DECISIONS.md](DECISIONS.md)

---

## Current state

**Phase:** Core complete. gopod is a working personal Telegram
Claude assistant with session continuity, long-term memory, voice,
vision, scheduling, and multi-provider architecture ready for Codex.
**Last updated:** 2026-10-05
**Last working session (2026-10-05):** production deploy + Codex harness.
`main` (`b254e01`) is deployed on the production host via `docker compose`
(data at `/opt/gopod/data`, live owner workspace at
`/opt/gopod/repo/chats/owner`). Found and fixed in that pass: the
`Dockerfile` build stage was `golang:1.25-alpine` while `go.mod` had moved
to `go 1.26.0` with O2 (`1897c8c`); the agent images were 3 weeks / 5 months
behind npm, so both Dockerfiles now pin the CLI via build args
(`CLAUDE_CODE_VERSION=2.1.289`, `CODEX_VERSION=0.160.0`, `2c18a80`), the
runner logs `agent container ready … cli_version=` once per spawn
(`internal/runner/version.go`, `c352ce8`), and the Docker integration suite
takes `GOPOD_TEST_{AGENT,CODEX}_IMAGE` / `GOPOD_TEST_{CLAUDE,CODEX}_VERSION`.
codex-cli 0.160 removed `--full-auto`; `CodexProvider` now passes
`--dangerously-bypass-approvals-and-sandbox --skip-git-repo-check` to both
`exec` and `exec resume` (`b254e01`), and the streaming error carries the
stderr tail. **Verified live against a real ChatGPT login:** `/login`
persists to `data/sessions/owner/.codex/auth.json`; after `docker rm -f
gopod-owner` the next message was answered first time (turn 4, 2026-10-05
14:00:57 → 14:01:50), so the old "cold start answers only the second
message" symptom is gone (root cause: `~/.codex` on tmpfs + 0.128
`resume --last` failing on an empty session dir; both removed).

**Also this session:** a CP (change proposal) process. Template, lifecycle
and registry in [proposals/README.md](proposals/README.md); seven
proposals CP-001…CP-007 written from the `codex-agent` survey, all
`proposed`, suggested order in ROADMAP "Proposals". `/handoff` now
cross-checks the CP registry (step 4b). Uncommitted: `docs/proposals/`,
`CLAUDE.md`, `ROADMAP.md`, `.claude/skills/handoff/SKILL.md`, this file.

**Previous session:** durable turns (2026-10-03, branch
`worktree-durable-turns`, merged to `main`). Every agent run is now a row
in `turns` before it runs and survives a crash: `internal/queue` persists
through a `TurnStore`, `Recover` replays unfinished rows at boot, and
resumed turns continue the session with an interruption notice. Same
branch also bind-mounts `~/.codex` so Codex sessions and login survive
container recycling. Design:
[D020](DECISIONS.md#d020--durable-turns-the-queue-persists-every-agent-run),
spec `docs/superpowers/specs/2026-10-03-durable-turns-design.md`, plan
`docs/superpowers/plans/2026-10-03-durable-turns-plan.md`. Unit tests and
`go build ./...` green on the merged tree; the manual `kill -9` recovery
test (plan Task 8 step 5) has **not been run yet** (see "What's next");
the schema migration (`turns`, `task_run_logs.turn_id`) applied cleanly on
the production store on 2026-10-04.

**In parallel on `main`:** M3.6/O2 (2026-10-03, branch
`factory/20261003-gopod-b375`): `internal/observability` now has the
full §2.3 metric catalog in `metrics.go` (exported `Counter`/`Gauge`/
`Histogram` wrappers, no-op until bound, `GOPOD_METRICS_DROP_CHAT_LABEL=1`
strips `chat` via one `labelSet` helper), a private Prometheus registry
with Go + process collectors, `gopod_build_info` and `gopod_uptime_seconds`,
and the §2.2 listener in `metrics_handler.go` (GET `<GOPOD_METRICS_PATH>`
+ GET `/healthz`, everything else 404, 5s shutdown grace). `Init` is now
wired from `cmd/gopod/main.go` right after config load (fatal on bind
failure) with a deferred 5s `Shutdown`. `go mod tidy` done. ROADMAP O2 ✅,
M3.6 still ⬜; O3 (instrumenting subsystems) is next.

Before that: public-release prep (2026-10-03, committed as `0832ebd`):
added MIT `LICENSE`, README "Security notes" section, replaced every
`/Users/<name>/...` path in CLAUDE.md/ARCHITECTURE/GLOSSARY/GATEWAY/
DECISIONS/m7 plan with GitHub URLs (NanoClaw canonical is now
`nanocoai/nanoclaw`), scrubbed the deploy host name, LAN IP and ssh
user from docs, untracked `chats/owner/` (runtime state, seeded from
`internal/runner/chattmpl`; `/chats/*` now gitignored, `chats/.gitkeep`
kept for the compose bind mount). Before that: codex streaming deadlock
+ device-flow /login fixes (committed), `/register` moved into
`internal/control` (committed).

**M3.6/O1 landed (2026-10-03, committed on branch `factory/20261003-gopod-5ab2`, PR #1):**
`internal/observability` scaffold — `Config`/`LoadConfig()` reading
`GOPOD_METRICS_ADDR` and `GOPOD_OTLP_ENDPOINT` via `os.Getenv` only
(D012), `Init(cfg) (Provider, error)` installing the otel no-op
TracerProvider plus a no-op metrics facade, `Provider.Shutdown(ctx)`.
No ports, no exporters, no Prometheus registry; a set env var only
logs that the real provider lands in O2/O4. `go.mod` promotes
`go.opentelemetry.io/otel` and `/trace` to direct (already present
as indirect via the Docker SDK). Wiring from `cmd/gopod/main.go`
landed with O2 (see Last working session). ROADMAP O1 row ✅, M3.6 still ⬜.

History was rewritten with `git filter-repo` on 2026-10-03 (a stray
compiled binary and local machine details removed) and force-pushed.
Any clone made before that date shares no ancestry with `main` and must
be re-cloned rather than pulled.

Uncommitted on the working tree (2026-09-25): `/register` now lives in
`internal/control/chats.go` as `RegisterChatCommands(router, store,
ownerChatID)` registering `chats.register` with `PermOwnerOnly`. The
old Telegram-side `registerHandler` and the placeholder Router stub in
`cmd/gopod/main.go` ("register via Router not yet wired") are deleted,
so `/register` finally dispatches through the Router like every other
slash command (closes the last D011 exception besides `/login`). Tests
in `internal/control/chats_test.go` cover success, all argument
rejections, owner self-register refusal, bad folder name via
`store.RegisterChat`, and non-owner denial with no DB write. `go build
./... && go vet ./... && go test ./...` all pass. Not yet deployed to the production host.

Fixes committed 2026-09-25:
- `c362dc7` runner: `RunStream` read stderr only after stdout EOF while
  `ExecStream` demuxes both from one goroutine into unbuffered pipes,
  so the first stderr write blocked the demuxer and the turn hung
  forever ("typing...", queue stalled). Only codex triggered it
  (`codex exec` logs progress to stderr). Read loop extracted to
  `readExecStream`, stderr drained in its own goroutine; regression
  test `internal/runner/stream_read_test.go`.
- `25a1543` telegram: device-flow (codex) `/login` left the login
  session registered, so the next ordinary message was piped to
  `codex login` as an OAuth code and got no reply. Sessions now record
  whether they expect a pasted code (OAuth only); device-flow logins
  complete in the background; completion reply names the chat's
  provider.

Latest fix (2026-09-10): /login URL arrived mangled in Telegram.
Two causes: (1) claude CLI ≥2.1.267 wraps the URL in an OSC-8
hyperlink; extractors now strip CSI+OSC via shared
`internal/runner/ansi.go:stripTerminalEscapes` and cut the URL at the
first whitespace. (2) `markdownToTelegramHTML` italic pass ate
underscores in query params; inline-code spans are now cut out before
emphasis passes, and login messages wrap the URL in backticks
(copyable code entity). Deployed to production as `cb902bd`.

Handoff process is now enforced, not just documented (2026-09-10).
`.claude/hooks/handoff-status.sh` runs on SessionStart via
`.claude/settings.json` and reports how many commits this file is
behind HEAD (plus a warning when the `Last updated` stamp disagrees
with the file's real last commit date). `/handoff`
(`.claude/skills/handoff/SKILL.md`) is the write-side procedure.
Motivation: this file's stamp sat at 2026-04-13 while 18 commits
landed on top of it, so the convention in CLAUDE.md alone was not
holding.

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

- ✅ **Durable turns (DT, D020)** (2026-10-03, branch `worktree-durable-turns`).
  `turns` table + `migrations` entry `task_run_logs.turn_id`
  (`internal/store/schema.go`); `internal/queue` takes `New(ctx, handler, n,
  store, log)`, inserts before dispatch, marks `running/done/failed/
  interrupted`, `Recover` replays unfinished rows with `Resumed=true`
  (attempts cap 3, `ErrCrashLoop`); `internal/telegram` builds the resume
  prompt, reuses the placeholder and memoizes placeholder/reply ids (first
  write wins) and persists outbound replies to `messages`; scheduler
  checkpoints runs through the queue and finalizes `task_run_logs` via the
  queue completion hook, keyed on the scheduled slot (`next_run` as
  `run_at` and `SourceID="<task_id>:<next_run>"`; a duplicate slot still
  advances `next_run`); `cmd/gopod/main.go` calls `Recover` at boot and
  waits on shutdown. On max retries the queue keeps pending items (memory
  and DB) for the next `Enqueue`/`Recover`. `Queue.Close` refuses new work
  and marks in-flight batches `interrupted`; items waiting on the semaphore
  stay `pending`, no hook. `GOPOD_NO_CONTAINER` passes a nil store and
  stays memory-only. Not yet verified by a real crash (see next).

- ✅ **`/register` through the control Router** (2026-09-25, uncommitted).
  `internal/control/chats.go:RegisterChatCommands` + `chats_test.go`.
  `telegram/commands.go` keeps only `/whoami`; `parseSlashArgs` and
  `registerHandler` removed. Docs/CONTROL.md §176 still describes the
  aspirational `/chats register <folder> [--here]` syntax; actual syntax
  is `/register <chat_id> <folder>`.
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

0. **Run the manual crash-recovery test for durable turns** (plan Task 8
   step 5, still not run; the branch is merged and deployed, cold start
   of the agent container is verified, but a `kill -9` of gopod itself
   mid-turn is not): on the production host, send a message that
   triggers a slow agent turn; `docker kill -s KILL gopod-gopod-1` while
   the streaming placeholder is showing; `docker compose up -d`; confirm
   the same placeholder is edited (no second reply), the agent mentions
   the interruption, the `turns` row ends `done` with
   `placeholder_msg_id`/`reply_msg_id` set, and a restart with no
   unfinished rows replays nothing. Optional variant: delete the
   placeholder in Telegram before restarting (checks `reusePlaceholder`).
0b. **CPs in suggested order** (each: accept → spec → plan → execute):
   CP-001 memory API token → CP-005 tools base image → CP-002 Telegram
   outbox → CP-003 typed replies via IPC → CP-004 isolated task sessions
   → CP-006 yielding maintenance + quiet hours → CP-007 store backups.
   See [proposals/README.md](proposals/README.md).

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
if `ANTHROPIC_API_KEY` is set. Skipped silently otherwise. Not
configured in the reference deployment, so this stays at ⏸️.

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
`/register <chat_id> <folder>` (owner-only, via Router since 2026-09-25),
`/provider claude|codex`, `/clear`, `/compact`.

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

- **Durable-turns known gaps (D020).** Voice-only reply mode records no
  reply id, so a crash right after a voice-only reply replays the turn once.
  `Queue.Close` has no timeout: a handler that ignores its context would hang
  shutdown.
- ~~**Codex sessions are lost whenever the container dies.**~~ Fixed
  2026-10-03 on this branch: `data/sessions/<chat>/.codex` is now
  bind-mounted RW to `/home/node/.codex` for every chat
  (`internal/runner/mounts.go`, `standardMounts` + `EnsureChatDirs`), so
  Codex rollouts and `auth.json` survive idle kill, restart and
  `/provider` switches. Verified 2026-10-05 against a real ChatGPT login
  and a recreated container. `codex exec resume --last` still picks "most
  recent" rather than a stored thread id (follow-up: persist the Codex
  thread id per chat in `sessions` and resume by id; `codex-agent` does
  exactly this with `sdk.resumeThread`).
- **Pushes to `main` bypass the repo's "changes via pull request" rule.**
  Three direct pushes on 2026-10-03/05 went through as admin. Either honour
  the rule (PR per change, as the factory's `#2` did) or drop it.
- **`/opt/gopod/repo/container/skills` on the production host is empty**,
  so no container skills are mounted there. `/opt/gopod/repo` is a
  hand-made directory (chats + container), not a checkout; the gopod
  binary image is built from `~/gopod`. Decide whether to point
  `GOPOD_HOST_REPO_DIR` at the checkout or sync skills into it.
- **`.claude/worktrees/` is not gitignored.** Harmless while no worktree
  exists; add a line before the next worktree session.
- **ROADMAP drift found and corrected 2026-09-10.** P3 (Codex provider) and
  P4 (`/provider` command) were listed as ⬜ while both were fully
  implemented — `provider_codex.go` + `container/Dockerfile.codex`, and
  `RegisterProviderCommand` wired in `cmd/gopod/main.go` with the choice
  persisted in `router_state`. Statuses flipped to ✅. Codex is still
  unverified end-to-end against a real login, and `GOPOD_DEFAULT_PROVIDER`
  (named in the original P4 row) does not exist — the default provider comes
  from `GOPOD_CONTAINER_IMAGE`.
- **CONTROL.md command table is stale for `/register`.** It documents
  `/chats register <folder> [--here]` and a `gopod chats register` CLI
  form; neither exists. Real command is `/register <chat_id> <folder>`,
  Telegram only (C3 CLI frontend still deferred). Fix the table when
  touching CONTROL.md next.
- **Memory API has no authorization (found 2026-10-03 during release
  audit).** `internal/memory/api.go` binds `0.0.0.0:9876`, compose
  publishes the port, and handlers take `chat` from the query string.
  Any sibling container or LAN host can read/write any chat's memory,
  including the owner's. Violates the "authorization from path, never
  from LLM data" rule in CLAUDE.md. Documented in README Security notes;
  real fix (per-chat token or bridge-only bind + source-IP → chat map)
  not started.
- **FreeFeed env mismatch.** `cmd/gopod/main.go:166` forwards
  `FREEFEED_BASE_URL/USERNAME/PASSWORD` into every agent container;
  `container/skills/freefeed/SKILL.md` documents `FREEFEED_APP_TOKEN`.
  Neither is in `.env.example`. Password also reaches non-owner chats.
- **S5 (dev-time Claude Code skills) is now partially real** — `.claude/`
  holds one skill (`handoff`) and one hook. Left at ⬜ in ROADMAP because
  the milestone means a maintainer skill set, not a single skill.

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
