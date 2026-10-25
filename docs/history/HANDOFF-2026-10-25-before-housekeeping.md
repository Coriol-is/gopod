> Historical snapshot from main `11eaabf`, retained for provenance and the
> original detailed acceptance procedures. It contains superseded statuses and
> operational assumptions. Current truth is ROADMAP.md, docs/HANDOFF.md and
> docs/ACCEPTANCE.md; do not execute an old operation solely from this snapshot.

# gopod — Handoff

**Read this first when picking up the project.** Single source of truth for
"what is the state right now". Always update this file before you stop working.

> Companion docs: [../README.md](../../README.md) · [../CLAUDE.md](../../CLAUDE.md) · [../ROADMAP.md](../../ROADMAP.md) · [ARCHITECTURE.md](../ARCHITECTURE.md) · [DECISIONS.md](../DECISIONS.md)

---

## Current state

**Phase:** Core complete. gopod is a working personal Telegram
Claude assistant with session continuity, long-term memory, voice,
vision, scheduling, and multi-provider architecture ready for Codex.
**Last updated:** 2026-10-25
**Current deployment (2026-10-25 16:42 UTC):** reference Pi runs `8df259d`
(PR #40), including PR #39 task panel and prior voice/scheduling/provider/IPC
fixes. Main/PR CI, independent reviews, full tests and relevant races passed.
Running application version/image, startup, SQLite integrity, restart count 0
and OOMKilled false verified. Agent runtime tags now contain Claude Code 2.1.310
and Codex CLI 0.164.0; both versions verified in hardened ephemeral ARM64
containers on the Pi. Old images and an online backup retained. Session/history/
task counts preserved (7/54/1); the existing digest remains paused. Native
Claude's V8/RAM cap is not qualified merely by accepted NODE_OPTIONS.
[Deployment report](../deployments/2026-10-25-8df259d.md) separates real deployment
proof from fake-API panel tests and untested authenticated CLI/session acceptance.
Earlier IPC delivery proof is in the 7bfd994 report; earlier zero-task and
both-image scheduling probes are historical evidence in the 1e0a21b report.

**IPC delivery correction (2026-10-25, deployed):** the owner's live
`delivery-test-1792943974.json` failed because IPCSink parsed canonical
`tg:89160926` as an integer. The adapter now removes the transport prefix at
its API boundary (legacy numeric ids retained), splits long plain text into
UTF-8-safe parts and reports partial sends without retrying the whole digest.
Messages produce scoped diagnostic receipts in `results/messages/` for success,
terminal errors and pending retries. These are not an exactly-once outbox;
network/crash delivery ambiguity remains explicit. Terminal outcomes are retained
in process while archive moves fail, preventing deterministic resend loops. Request reads are bounded
and reject nonregular/symlink files. The guide documents receipt paths and
failure semantics. The user-created `freefeed-daily-digest` changed to paused through another
actor during investigation; its 09:00 Europe/Belgrade cron is unchanged. This
fix does not create, pause, resume or change a task.
Local build/vet/full tests, scoped races, independent review and CI passed.
PR #37 is merged and deployed. A real canonical-JID message returned ok and
.processed; an invalid JID returned an error receipt and .failed. No model/digest
run was invoked; user-visible reading and real recurring execution remain open.

**Task control panel (2026-10-25, deployed):** /tasks no-argument replies failed in production
because byte truncation split a Cyrillic/emoji prompt; persisted logs show
Telegram's UTF-8 rejection for both reported commands. Rune-safe truncation and
a three-task page with scoped Pause/Resume/Refresh/navigation now have control
and Telegram callback tests. State actions are explicit, validated under the
scheduler lock, stale-safe and cannot revive completed tasks. Code review, build/vet/full tests, scoped races and CI passed. PR #39 is
merged and deployed in 8df259d. Real user callback taps remain untested.

**New accepted design-only specifications:** CP-016 private-topic deletion and
session retirement, CP-017 Telegram capability diagnostics, CP-018 per-thread
provider overrides. Specs under docs/superpowers/specs define acceptance gates;
none of these three features is implemented. Bot API has no documented deleted
forum-topic event; closed topics must not lose sessions. Current getMe reports
Guest and private topics enabled, but gopod has no guest update subscription or
handler. Inline/business are disabled; the pinned SDK's inline boolean JSON tag
is wrong (official true fixture decodes false), so diagnostics must correct it.
Bot-to-bot is researched separately; its current enablement is unknown, no peers
were contacted or modes toggled. Sender trust and loop budgets are prerequisites.

**Ephemeral command replies (CP-019):** separate design-only spec saved after
research. Owner scope is explicit: ordinary conversational output stays public;
only group service/command/error replies use private delivery. Includes actor-
bound controls, eligibility/expiry, message_id=0/reused ephemeral ids, isolated
persistence and zero public fallback. SDK primitives exist; gopod does not use
them yet, and no settings/command privacy flags were changed during research.

**Extensibility specs saved (design only):** CP-011 sandbox runtime and CP-012
provider registry separate agent behaviour from execution backend lifecycle.
Both specs are saved under `docs/superpowers/specs/`; proposed D032 changes no
current runtime support. Docker remains the sole backend; no additional agent,
remote service or implementation was added. `GOPOD_NO_CONTAINER=1` is now
accurately documented as disabling the runner, not a working subprocess fallback.

**Speech transcript provenance (CP-013):** successful voice/audio/video-note
transcripts carry fixed source kind, automatic-ASR marker and positive duration
when known. Typed audio captions stay separate; commands/login see the original
raw text. The queued text carries this metadata into retries/recovery and the
D030 journal/handoff. No filename/id, speaker, language, emotion or confidence
is inferred. Implementation and tests are complete; live voice acceptance remains
open. Deployed in `1e0a21b`; no old transcripts were backfilled.

**Media planning (CP-014):** the owner approved three queues in
[the media spec](../superpowers/specs/2026-10-25-telegram-media-input.md) and
[plan](../superpowers/plans/2026-10-25-telegram-media-input.md): (1) card/link/input
fidelity and truthful unsupported notices; (2) document/image/speech analysis;
(3) bounded video timelines from speech and selected frames, then exports/search.
This is planning only. Actual hidden URL/update fields from the supplied Guest
card are not captured; do not infer them from the screenshot. Gopod's own guest
query participation requires a separate authorization/reply namespace.

**Agent scheduling (CP-015):** generic recurring-task capability, not a manually
created digest. Agents read `/workspace/chat/SCHEDULING.md` (seeded if missing
for existing/new chats, without replacing custom identity files) and use exact
IPC operations with source-scoped result receipts. Initial `next_run` is now
computed; explicit IANA cron zones work in Alpine via embedded tzdata. Agents
can inspect/update/pause/resume/cancel/delete their tasks; cancel is scoped.
Task mutations and host-trusted replay results commit atomically. Dispatch
refresh and task mutations are coordinated, and nonregular IPC files are
rejected without blocking. Full task ids are usable from `/tasks list`.
Schedule stdout remains silent; explicit IPC message delivery is documented.
Owner DB-file RW does not provide a writable WAL directory or a supported
scheduling API. Source/test work, independent review and CI passed; PR #35 is
merged and deployed in `1e0a21b`. Live nonmutating receipt/guide checks passed; no live
schedules were created for this investigation.
See [the capability spec](../superpowers/specs/2026-10-25-agent-scheduling.md).

**Earlier working session (2026-10-24):** Telegram chat-UI parity, built
autonomously through worktrees and PRs #3–#10 (agent implementer + separate
reviewer per task, CI green, admin squash-merge). Now included in deployed
`a9146f0`, with detailed live acceptance still open: native streaming via
`sendMessageDraft`, Stop
button, and parallel conversations per private-chat thread (one agent session
per `(chat, thread)`). Design and open items: [D021](../DECISIONS.md#d021--stop-and-native-streaming),
[D022](../DECISIONS.md#d022--conversations-one-agent-session-per-chat-thread),
spec `docs/superpowers/specs/2026-10-24-telegram-threads-and-drafts-design.md`,
plan `docs/superpowers/plans/2026-10-24-telegram-threads-and-drafts.md`.
Also: CI workflow `.github/workflows/ci.yml` and the PR-based git flow in
CLAUDE.md (main's ruleset needs 1 approval; PRs are merged with
`gh pr merge --squash --admin`).
**2026-10-25 incident:** the Pi voice message was downloaded and transcribed
successfully; repeated Claude exits were an account session usage limit (429,
reset reported as 13:50 UTC), not container restarts or OOM. Checked live:
gopod restart count 0, OOMKilled=false. PR #30 is merged on main
(`5a9af2a`). The runner now classifies the known
session/usage-limit CLI response separately on failed streaming and sync runs;
Telegram explains the quota rather than a generic agent failure. No automatic
retry or provider switch without confirmation. A real Claude turn completed
after the later deployment; voice/quota-specific acceptance remains open.

**2026-10-25:** confirmed quota fallback (CP-010, D031). A recognized
provider usage-limit rejection offers Switch and continue / Cancel in the
failed turn's thread. Only confirmation changes the chat-wide provider and
queues one retry with the original thread/text/voice/file metadata. Existing
command-token single-use/expiry applies; chat/folder/thread and expected-provider
checks prevent foreign or stale actions. The target's own quota error reports
without another switch offer. Ordinary agent/network/login errors do not offer
a switch. Native sessions and bounded context use D030. Deployed in `a9146f0`; real
quota-confirmation acceptance is still pending. No unattended fallback or
switch-back is implemented.

**Live acceptance checklist remains open** (next section, item 00 and 0X–0Q).
**2026-10-25:** extracted memories are now scoped per conversation
([D023](../DECISIONS.md#d023--extracted-memories-are-scoped-per-conversation)):
a thread sees main/manual/pinned rows plus its own, main sees all; existing
rows stay shared (thread 0).
**2026-10-25:** lifecycle notices
([D027](../DECISIONS.md#d027--lifecycle-notices)): on SIGTERM gopod tells the
owner and chats with unfinished turns "🔄 Перезапускаюсь…" (3 s bound), on
startup tells the owner "✅ Снова на связи" or "⚠️ Был неожиданный перезапуск"
with the replay count (one notice per 2 minutes). `GOPOD_NOTIFY_LIFECYCLE=0`
disables; compose `stop_grace_period: 30s`. Startup notice after a graceful
restart was verified during the 2026-10-25 deploy; visible shutdown-notice and
crash/replay variants remain open. On the Pi a
`docker compose up -d` recreate should show both messages, and
`docker kill -s KILL <container>` then `docker compose up -d` the crash one.
**2026-10-25:** dynamic UX phase 4 (PR #29 merged at `d6f0e13`, deployed in
`a9146f0`; detailed live checklist 0X remains open): `control.Response.Buttons`
([D028](../DECISIONS.md)), `/model` and `/provider` pickers, confirmations for
`/clear` and `/provider <name>` (`control.Confirm`, `CodeConfirm`), paged
long command replies. Command buttons are in-memory tokens (`cc:`/`cf:`/
`cx:`/`pg:`), dispatched through the Router (`internal/telegram/cmdbuttons.go`,
`pages.go`). Review fixes resumed after the Claude session limit:
pages are budgeted at 3800 rendered HTML bytes (including fence balancing),
so Cyrillic/escaped output stays one message. Slow command callbacks are
answered before dispatch; failed/spent confirmations lose their buttons.
Unknown providers fail before confirmation and selecting the current provider
is a no-op. Topic-gone picker/page fallbacks retain their buttons in main. Risky
confirmations instead ask the user to rerun the command in the desired
conversation, without buttons, so a deleted-topic `/clear` cannot clear main.
**2026-10-25:** dynamic Telegram UX phase 6 (input context, branch
`feat/ux-phase6`, PR #28 merged on main at `51cc974`): replied, quoted and forwarded
text reaches the prompt as a header (`internal/telegram/inbound_context.go`),
albums become one turn after a 1 s window, audio files and video notes
are transcribed like voice. Review fixes reject media before downloading or
transcribing for unregistered chats, sanitize and distinguish upload names,
and drain inbound handlers plus pending/in-flight album downloads on graceful
shutdown. Obsolete timer callbacks cannot shorten a restarted album window.
Deployed in `a9146f0`; detailed media live checklist 0Y remains open.
**Previous session (2026-10-05):** production deploy + Codex harness.
`main` (`b254e01`) was deployed on the production host via `docker compose`
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

**Change proposals:** the CP process, registry and `/handoff` cross-check
are committed. Current proposal statuses live in
[proposals/README.md](../proposals/README.md); do not infer acceptance from this
historical handoff.

**Previous session:** durable turns (2026-10-03, branch
`worktree-durable-turns`, merged to `main`). Every agent run is now a row
in `turns` before it runs and survives a crash: `internal/queue` persists
through a `TurnStore`, `Recover` replays unfinished rows at boot, and
resumed turns continue the session with an interruption notice. Same
branch also bind-mounts `~/.codex` so Codex sessions and login survive
container recycling. Design:
[D020](../DECISIONS.md#d020--durable-turns-the-queue-persists-every-agent-run),
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
  per [D017](../DECISIONS.md)), `internal/config` with env loader + tests,
  `internal/store` opening SQLite via `ncruces/go-sqlite3` with sqlite-vec
  bundled in via the asg017 binding, full schema applied idempotently
  (chats, messages, registered_chats, sessions, scheduled_tasks,
  task_run_logs, router_state, memories, memory_vec vec0, memory_fts5 +
  triggers), runtime check of `vec_version()`, KNN smoke test, and
  `cmd/gopod/main.go` that wires slog (text|json), opens the store,
  blocks on SIGINT/SIGTERM, exits cleanly. `go test ./...` is green.
- ✅ NanoClaw architecture mapped (see [ARCHITECTURE.md §2](../ARCHITECTURE.md))
- ✅ Library research: Telegram (`go-telegram/bot`), Claude SDK
  (`character-ai/claude-agent-sdk-go` + `anthropic-sdk-go`), SQLite
  (`ncruces/go-sqlite3` + `sqlite-vec`), cron (`robfig/cron/v3`), Docker
  (`docker/docker/client`)
- ✅ ADRs D001–D018 recorded
- ✅ Memory architecture: 5-layer model, sqlite-vec schema, embedder
  abstraction, auto-ingestion plan ([MEMORY.md](../MEMORY.md))
- ✅ Integration triage: Tier 1/2/Skip with effort estimates and an
  implementation order I1–I10 ([INTEGRATIONS.md](../INTEGRATIONS.md))
- ✅ Skill ecosystem redesign: container skills + MCP skills + dev skills,
  no branch-merge ([SKILLS.md](../SKILLS.md), [D009](../DECISIONS.md))
- ✅ Control plane design: `internal/control` Router + Telegram/CLI
  frontends + logs subsystem, slotted as M3.5 with sub-steps C1–C5
  ([CONTROL.md](../CONTROL.md), [D011](../DECISIONS.md))
- ✅ Secrets policy locked down: env-only, no values in committed files,
  slog redaction ([D012](../DECISIONS.md))
- ✅ Container isolation policy: three-tier mounts, allowlist file at
  `${DATA_DIR}/mount-allowlist.json`, compiled-in blocked patterns, RO root
  + dropped caps + non-root uid, `internal/runner/mountsec` subpackage.
  ([ISOLATION.md](../ISOLATION.md), [D013](../DECISIONS.md))
- ✅ Observability design: opt-in Prometheus `/metrics` + opt-in OTel OTLP
  traces, logs always on via M3.5/C5; slotted as M3.6 with sub-steps O1–O6.
  ([OBSERVABILITY.md](../OBSERVABILITY.md), [D014](../DECISIONS.md))
- ✅ No socket / HTTP control frontend in v0 (user decision 2026-04-09)
- ✅ Documentation structure: README, CLAUDE.md, ROADMAP, docs/{ARCHITECTURE,
  MEMORY, INTEGRATIONS, SKILLS, CONTROL, ISOLATION, OBSERVABILITY,
  DECISIONS, GLOSSARY, HANDOFF}.md
- ✅ Sanity-check pass on the doc set: stale references, env var prefixes,
  ADR consistency, SKILLS hedge wording all reconciled
- ✅ `.gitignore` for Go + macOS + gopod runtime (data/, *.sqlite, .env)

## What's in progress

**Provider continuity (CP-009, D030):** manual switching implemented with
independent review, build/vet/full tests and targeted race tests. Native sessions
are preserved, each `(chat folder, thread)` gets its own bounded recent
user/assistant transcript in the new provider's prompt, and switching waits
for an active run. `/clear` invalidates history and both provider session
mappings in that thread only. Full transcript/tool-state migration and automatic
quota fallback are out of scope. Tests use real SQLite and fake CLI processes;
deployed in `a9146f0`, but live Claude/Codex switch acceptance remains open.

**Dynamic Telegram UX phases 4 and 6:** PR #29 (`d6f0e13`) and PR #28
(`51cc974`) are merged after independent review and green CI. Live checklists
0X/0Y remain open; PR #30 quota classification is also merged (`5a9af2a`).
A real post-deploy Claude turn completed; detailed voice/quota recovery remains open.

**Phase 7 — Streaming output** ([STREAMING.md](../STREAMING.md)). ✅ **Done.**

Real-time agent output to Telegram via message editing (~1s updates).
All 5 sub-steps (ST1–ST5) implemented:
- `Docker.ExecStream` + `StreamHandle` (`internal/runner/docker.go`)
- `Runner.RunStream` + `AgentStream`/`RunResult` (`internal/runner/runner.go`)
- Telegram helpers: `sendPlaceholder`/`editMessage`/`editMessageHTML` (`internal/telegram/streaming.go`)
- `runAgentStreaming` with 1s ticker, overflow >4000, fallback to sync (`internal/telegram/streaming.go`)
- Config: `GOPOD_STREAM_DISABLED=1` to disable. Default: streaming on.
- `maybeVoiceReply` extracted for reuse across sync/streaming paths.

**Coding output, parts 1-2** ([D024](../DECISIONS.md#d024--coding-output-output-policy-and-tool-progress-in-the-draft), 2026-10-25, branch `feat/coding-progress-clean`): output policy in every system prompt; Claude streamed turns run `stream-json` and the draft shows the last five tool steps. Unit-tested with sanitised fixtures from Claude Code 2.1.310 whose event kinds match 2.1.289; **not verified live** (check: progress lines appear, final answer short, Stop mid-tool, `GOPOD_PROGRESS=0`, expired-login reply). Part 3 is D025.

**Phase 6 — Secret gateway** ([D019](../DECISIONS.md), [GATEWAY.md](../GATEWAY.md)).
Design complete. Implementation not started. Second priority.

MITM HTTP CONNECT proxy embedded in gopod + HashiCorp Vault backend.
Replaces env var secret injection — agents never see real keys.
8 sub-steps (G1–G8), G1∥G2 can start in parallel.
See [GATEWAY.md](../GATEWAY.md) for full architecture.

**Long code in final replies** ([D025](../DECISIONS.md), spec part 3, branch
`feat/collapse-long-code`): final replies collapse fenced blocks over 12
lines into an expandable quote, send blocks over 60 lines / 3000 chars as
documents (`internal/telegram/longcode.go`, `document.go`) with a note in
the text, and the HTML chunker is tag-aware (`chunkhtml.go`). Live check
pending: a 20-line and a 100-line block in a private chat and in a topic.

## What's next (in order — per [D018](../DECISIONS.md))

00. **Live checklist for the 2026-10-24 work** (nothing below has been run
   against a real bot): (1) enable *Threaded mode* for the bot in @BotFather
   (Telegram may charge an extra fee on Star purchases for it) and create two
   topics; (2) main chat + two topics hold three distinct contexts, across
   `docker rm -f gopod-<chat>` and a gopod restart, on both `claude` and
   `codex`; (3) the main chat still has its pre-upgrade history (one-time
   adoption of the latest legacy session); (4) streaming shows a draft and one
   final reply; Stop sends the partial text, the `turns` row ends `stopped`,
   the agent process is gone (`docker top`), and the next message resumes the
   session without error (resume after a killed turn is unverified for both
   CLIs); Stop updates arrive at all (bot `allowed_updates`); (5) `kill -9`
   gopod mid-turn in a thread: exactly one final reply in that thread, row
   `done`; (6) `sendMessageDraft` accepts the string draft id and the final
   `sendMessage` clears the draft. Then optionally: set `HostConfig.Init = true`
   (zombies), and run a concurrency spike for parallel threads (two agents in
   one container; needs logged-in claude and codex).
   Open items from review (see D021/D022): compaction summarises without the
   conversation history (pre-existing; `/compact` effectively clears);
   `/provider` still clears main; a `/login` code is accepted from any thread;
   `ClearSession` during a Codex first turn is a no-op; the first adoption
   marker lives in `router_state`.
   Added after the final review: (7) check `--ephemeral` exists on the pinned
   Codex 0.160 (`codex exec --help` in the image; if rejected, every Codex
   extraction/compact/task-parse run fails silently); (8) after two turns in
   one thread the stored session id matches the single growing Claude
   `.jsonl`, and a Codex `sessions` row exists (a Warn "agent announced no
   session id" means the 0.160 stderr header differs from the 0.164 one the
   code was checked against); (9) after enabling Threaded mode, a reply in
   the main chat stays in thread 0 (`inboundThreadID` does not check
   `is_topic_message`). **Known Codex upgrade caveat:** production ran memory
   extraction through `RunFresh` without `--ephemeral`, so on Codex the newest
   rollout in the container is an extraction session and the one-time
   adoption will make that main's session (the old `resume --last` already
   resumed the same file, so main had no real continuity before either); step
   (3) will look like an upgrade bug on Codex. Claude is not affected.
   A deleted/closed topic mid-turn now diverts the reply to the main chat
   with a note (`internal/telegram/threadgone.go`; the matched error texts
   come from reported real errors, not the Bot API docs). Minors from the
   final review: after `kill -9` the Codex first turn of a thread restarts as
   a new conversation; `/memory/list` and `/memory/search` return every
   thread's `conversation_summary`; `/compact` after a restart answers
   "nothing to compact (0 turns)" because turn counters are in memory;
   `/clear` and auto-compact are not serialized against a running turn of the
   same conversation; one extra `find` exec per turn (session existence check).
0U. **Live checklist for dynamic Telegram UX, phase 1** (branch
   `feat/ux-phase1`; spec `docs/superpowers/specs/2026-10-25-telegram-dynamic-ux-design.md`
   "Verify live", plan Phase 1; nothing run on a real bot yet): (1) a button
   press is answered in under 1 s and its toast shows; `allowed_updates` is now
   explicit (`message`, `callback_query`, `stopped_message_generation`): check
   messages and draft Stop still arrive after the restart that applies it;
   (2) in a registered **group**, a long turn shows `⏹ Стоп` on the
   placeholder (style `danger` renders or degrades); pressing it stops the
   turn, the placeholder keeps the text so far without the button, the
   `turns` row ends `stopped`, the agent process is gone (`docker top`); a
   second press toasts «Уже завершено»; the same in a private chat whose
   drafts are refused and on a gate-wait placeholder (`GOPOD_MAX_AGENTS=1`,
   two chats at once); (3) a forced agent failure (e.g. stop the container
   mid-turn) replies in Russian with `🔁 Повторить` and no error text; Retry
   answers in the same topic with the original text, a double tap makes one
   turn, the button disappears; a second failure within 60 s only sets the error
   reaction (😱 since phase 3, 👎 before); an
   expired login replies with `🔑 Войти`, which starts the `/login` flow in
   that topic. Known limits: `errBusy` is never produced yet (the D026 gate
   waits without a timeout); a Retry pressed twice across a restart can make
   two turns (the press set is in memory; each press has its own query id).
0V. **Live checklist for dynamic Telegram UX, phase 2** (branch
   `feat/ux-phase2`; spec F3 and "Verify live" items 3 and 4, plan Phase 2;
   nothing run on a real bot yet; run 0U first so regressions are
   attributable): (1) a reply in a private chat (draft path) and in a group
   (edit path) ends with one row `🔁 Заново`, `📋 Копировать` (only for a
   reply of at most 256 characters, or whose only code block fits), `⋯`;
   on the edit path the row replaces `⏹ Стоп` in the final edit without a
   flicker; a long reply has the row on its last message only, never on a
   `📎 snippet` document; (2) the next reply in the same conversation takes
   the row off the previous reply; a reply in another topic leaves it;
   a stopped reply has no row and also takes the previous one off; (3)
   `🔁 Заново` in topic 7 while main runs: waits its turn, answers in topic 7
   as a new reply, the pressed row disappears, a double tap makes one turn;
   restart gopod between the reply and the press: Regenerate still works;
   check that the agent really gives a different answer in the same session
   (prefix `regeneratePrefix` in `internal/telegram/actions.go`); (4)
   `📋 Копировать` copies on iOS, Android and Desktop (check the real limit
   and which text lands: the rendered text, markup removed); (5) `⋯` swaps
   the row for `🧭 Шаги`, `📄 Файлом`, `‹ Назад` in place, `‹ Назад` restores
   it; `🧭 Шаги` after a tool turn sends the steps as a collapsed quote in
   the reply's topic, with secrets masked like the draft lines; `📄 Файлом`
   sends `reply.md` in the topic; after a restart or an hour both toast
   «Шаги этого ответа не сохранились» / «Текст этого ответа больше
   недоступен». Known limits: which message holds the row is in memory, so
   after a restart the previous reply keeps its row (its buttons still
   work); the topic-gone fallback reply in main has no row; Back after a
   restart restores the row without Copy; Regenerate pressed twice across a
   restart can make two turns; the regenerated turn is not linked to the
   original in the store.
0W. **Live checklist for dynamic Telegram UX, phase 3** (branch
   `feat/ux-phase3`; spec "Verify live" item 6; nothing run on a real bot
   yet): (1) every status reaction is accepted (👀 on receipt, 🤔 when the
   agent starts, 👨‍💻 on tool steps, 👍/😱/👌 at done/error/stop, ✍ on
   `/compact`), no 429 in the log during a 20-step tool turn, and the final
   reaction is always the last one; (2) a slow turn (a `sleep 70` tool
   call) shows 🥱 after ~25 s and 😨 after ~60 s, and goes back to 🤔/👨‍💻
   when output resumes; (3) in a registered group where the bot has no
   reaction rights nothing breaks and the log has only debug lines; (4) the
   draft shows `⏳ m:ss · N шагов` above the steps, ticking every second,
   and the header disappears when the answer streams; on the edit path
   (group) it updates at most every 5 s and the `⏹ Стоп` button stays on
   every edit; (5) in a private chat with topics, a message in topic B
   while topic A runs shows `в очереди: 1-й` in B, replaced by the turn's
   own draft when it starts. Known limits: the queue position is shown only
   on the draft path and only when another thread is running (a group, or a
   second message in the same thread, keeps 👀); earlier messages of a
   batched turn keep 👀; elapsed time counts from the agent start, not from
   receipt; stall timers are not stopped by a shutdown mid-turn.
0R. **Generic agent scheduling (CP-015).** Guide/read-only list/invalid-op
   probes passed on `1e0a21b` (deployment report). For future releases, verify the additive
   guide is readable in an existing chat without overwriting its CLAUDE/AGENTS.
   Submit a read-only IPC list request and inspect its result receipt; submit an
   invalid operation and verify a machine-readable reason and failed archive.
   These probes must not create any schedule. Local integration/race tests
   cover create/manage, positive nextRun, IANA zones/DST, replay after archive
   failure/restart, malformed/FIFO input and cross-chat denial. A real user task
   can subsequently validate execution and explicitly requested delivery; do
   not create a FreeFeed or other recurring job just to complete this audit.

0Q. **Confirmed quota fallback (CP-010, D031).** After deployment, trigger a
   known quota rejection in a private topic: verify the named-provider question
   says the whole chat changes, no change before Yes, Cancel leaves it alone,
   and Yes edits to the switch/queued notice and retries the original request
   once in that topic. Check a voice input, context and native-session reuse,
   double tap, 5-minute expiry and restart-expired buttons. A quota rejection
   on the target must be shown even within 60 seconds, with no reverse switch
   offer. Change provider manually before pressing an old offer: it must refuse
   the stale choice. A target without login should ask for /login normally.
   No real quota-triggered switch or Pi deployment has yet been verified.

0Z. **Manual provider continuity (CP-009, D030).** After deployment, establish
   distinct facts/tasks in main and two private topics. Switch Claude → Codex,
   ask each topic to continue and verify its own recent details and shared
   files without sibling/main leakage. Switch back; verify native session ids
   are retained and work performed with Codex is included. Restart gopod and
   repeat. `/clear` topic A, switch both ways, verify A stays cleared while main
   and topic B continue. During a slow turn, confirm a provider switch waits
   for completion. A quota rejection must not require the old model to produce
   a summary. Record the 48-message/3000-character/48000-byte bound: this is
   recent-context continuity, not complete native tool transcript migration.

0X. **Live checklist for dynamic Telegram UX, phase 4** (branch
   `feat/ux-phase4`; spec F7-F9, plan Tasks 9-11,
   [D028](../DECISIONS.md), CONTROL.md §6.1; nothing run on a real bot yet;
   run 0U first): (1) `/model` with no args shows buttons `fable opus
   sonnet / haiku default` on a Claude chat (only `default` on Codex), the
   current one `✓` and `primary`; a press edits the same message to "Model
   set to …" without buttons and the next turn uses that model; (2)
   `/provider` with no args shows `claude`/`codex`; a press turns the same
   message into "Switch this chat to …?" with `Yes` (`danger`) / `Cancel`;
   `Yes` switches (main conversation cleared), `Cancel` edits to
   «Отменено.»; (3) `/clear` asks first, in the topic it was sent in;
   `Yes` clears that conversation only, a double tap clears once, `Cancel`
   then toasts «Кнопка устарела»; after 5 minutes `Yes` toasts expired;
   (4) in a registered **non-owner** chat a command button for an
   owner-only command toasts "Not authorized." and the log has the
   Router's `permission denied` warn; (5) `/logs` (or `/recall` with many
   hits) over 3800 rendered HTML bytes arrives as one message with `‹ 1/N ›`; `›`
   and `‹` edit the same message, a code block cut by a page stays
   formatted; after a restart or 10 minutes a press toasts «Страницы
   устарели, повторите команду»; (6) after a restart every old picker and
   confirmation toasts expired; (7) unknown `/provider xyz` fails without
   a question and selecting the current provider changes nothing; failed
   Yes removes its buttons; a slow command stops the spinner before work;
   a deleted-topic picker/page fallback retains its buttons in main; a
   deleted-topic `/clear` offers no confirmation and asks to rerun in the
   desired conversation, leaving main untouched. Include Cyrillic memories, escaped JSON logs,
   and fenced code in paging checks. Known limits: button labels and texts from
   `internal/control` are English (Phase 5 moves strings); buttons in an
   unregistered chat toast «Этот чат не подключён» (slash commands work
   there, callbacks need a registered chat); `/chats unregister` and
   `/container restart|stop` are on the Q6 list but do not exist yet; the
   CLI frontend (C3) is still not built, only `control.FormatText` and
   `ParseConfirmFlag` for it; a picker press re-sends the whole result,
   so a picker pressed after its model was changed elsewhere still applies.
0Y. **Live checklist for dynamic Telegram UX, phase 6** (branch
   `feat/ux-phase6`; spec F10, F11 and "Verify live" items 8 and 9, plan
   Phase 6; nothing run on a real bot yet): (1) reply to one of gopod's
   answers with "что я процитировал?" / "explain this": the agent answers
   about that answer; select part of it with Telegram's quote and ask
   again: it names exactly the quoted part; (2) forward a message from a
   user, a channel, and a user who hides forwards: the agent sees
   `[forwarded from <name>]`, `[forwarded from channel <title>]`,
   `[forwarded from a hidden user]` (ask "откуда это?"), never an id or a
   username of a named user; (3) in a private-chat topic, a plain message
   (no reply) gets no `[in reply to …]` header, so check that Telegram
   really stamps the topic root as `reply_to_message` with
   `forum_topic_created` or `is_topic_message` (the matching rule is from
   the Bot API docs, not a recorded update); (4) an album of 3 photos with
   a caption makes one turn, one 👀 on the first photo, one reply that
   sees all three (`[Album attached: …]`); the same in a topic answers in
   the topic; (5) an audio file (mp3/m4a) and a video note are transcribed
   and answered in text, a failed transcription replies "Failed to
   transcribe audio."; a sticker gets no reply and no reaction; (6) a long
   code reply shows "sending a file…" before the `📎` snippet and a voice
   reply shows "recording audio…" while TTS runs; (7) stop gopod
   (`docker compose restart`) right after sending an album: the album
   still becomes one turn after the restart (flushed at shutdown, replayed
   by Recover); repeat while the album's download is in progress. Known limits:
   abrupt termination (`SIGKILL`, power loss, or the 30 s container stop deadline)
   can still lose an album before its turn is persisted; downloads are bounded
   at 2 minutes, which can exceed that deadline; video and audio items inside
   an album are skipped silently (photos and documents only); an item arriving more
   than 1 s after the previous one starts a second turn; an album caption
   starting with `/` is sent to the agent, not to the Router, and an
   album never feeds a waiting `/login` code; audio files and video notes
   do not switch the reply to voice; Whisper rejects `.opus`, `.aac` and
   `.amr` audio files ("Failed to transcribe audio."); the header is part
   of the turn text, so `extractAndIngest` sees replied/forwarded text and
   may store it as a fact about the user; a caption-less single photo's
   prompt still does not name its upload path (pre-existing, noted, not
   changed here). Media from unregistered chats is no longer downloaded
   or transcribed; document names keep only their last path element
   after a unique prefix.
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
   See [proposals/README.md](../proposals/README.md).

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

- **Control plane open Qs** ([CONTROL.md §13](../CONTROL.md)):
  chat-local commands in unregistered chats (silent ignore vs reply),
  `/help` rendering across permission levels, rate-limiting (probably no),
  inline-keyboard confirmations for destructive ops (probably defer).
- ~~**ARCHITECTURE.md drift on mount allowlist location.**~~ Fixed — §3
  now references `${DATA_DIR}/mount-allowlist.json` per ISOLATION.md.
- **`ncruces/go-sqlite3` upgrade path.** Currently pinned to v0.20.0 per
  [D017](../DECISIONS.md) because the asg017 sqlite-vec binding hasn't kept
  up with upstream. Watch for either a new asg017 release or a vendored
  sqlite-vec wasm we ship ourselves. Re-bisect when bumping.

### Recently resolved (2026-04-09)

- ✅ **`claude-agent-sdk-go` custom command.** PARTIAL: `Options.CLIPath`
  exists but takes single argv[0], no `Command []string` / `CLIPrefixArgs`.
  Resolution: per-chat wrapper script generated under
  `data/wrappers/<chat>.sh` that `exec`s `docker exec -i <name> claude "$@"`.
  Recorded as [D015](../DECISIONS.md). Caveats baked in: explicit `-e VAR`
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
  in as default at 1024 dim, recorded as [D016](../DECISIONS.md). Client-side
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

- **Telegram native streaming + Stop (Phase A, 2026-10-24, PR in review).**
  `sendMessageDraft` replaces the placeholder-and-edit loop in private chats
  (edit path stays for groups and as a sticky per-chat fallback); Stop button
  via `can_stop` + `MessageGenerationStopped` ends the turn `stopped`
  (never replayed) and the runner kills the agent process group in the
  container. **Not yet verified live** (spec Task 4): `kill -9` of gopod
  mid-draft (exactly one final reply, `turns` row `done`), Stop on a real
  claude and codex turn (is the session resumable after a killed turn?),
  and whether the draft id is accepted as a string by the live API. Known
  follow-ups: set `HostConfig.Init = true` in `internal/runner/docker_args.go`
  (PID 1 is `sleep infinity`, killed children stay zombies and count against
  the pids limit); tool processes started with `setsid`/detached escape the
  process-group kill (snapshot descendants from `/proc` before TERM and check
  on the real image); crash between `sendFormatted` and `recordReply` on the
  draft path sends a second reply on resume; `draftSeq` restarts at 0 on
  restart, so a stale Stop could match the first new draft (seed it with a
  time-based value); `runner.Run`/`RunFresh` are not covered by the
  per-chat agent slot that makes the next turn wait for a kill.
  Also open (final-send retry, D021): a 429 on chunk 2+ of a multi-chunk
  reply ends the turn `done` with a truncated reply; a send that Telegram
  delivered but whose response was lost makes the whole-turn retry post a
  second reply; `finishStopped` has no retry for an undelivered partial.
  Pre-deploy live checks: resume after a killed turn on claude and codex,
  and that Stop updates arrive (bot's `allowed_updates` default).
- **Conversation threads (Phase B, plan
  `docs/superpowers/plans/2026-10-24-telegram-threads-and-drafts.md`).**
  Landed on main: per-conversation `sessions` store (Task 5), provider
  session ids `SessionRef` (Task 6), per-thread `conversation_summary`
  (Task 7). Not wired to callers until Task 9.

- **Future option: one container per thread** (owner, 2026-10-25). Record only,
  not designed. Alternative to running two agents in the same container
  (ROADMAP T5): each `(chat, thread)` gets its own agent container with the
  chat workspace and memory mounted shared. Gains real parallelism and separate
  `$HOME`/session dirs; costs RAM, more lifecycle (idle kill, cleanup, the
  per-chat `/provider` and `ProviderForChat` assumptions, the per-chat agent
  slot). Decide after the concurrency spike (HANDOFF item 00). See ROADMAP T5b.

- **Backlog: enable the memory cgroup on the deploy host** (owner, 2026-10-25,
  not implemented). On the Raspberry Pi 4B the container RAM limit set in
  code (4 GiB) is not enforced: `docker inspect gopod-owner` shows
  `Memory=0` because the memory cgroup is off. Fix is a boot cmdline change
  (`cgroup_enable=memory cgroup_memory=1`) and a reboot of a host that also
  runs other services; afterwards lower the per-agent cap to ~1 GB. Until
  then the agent gate (ROADMAP T8) is the only protection. Measured: one
  agent turn with a tool peaks at ~0.5-0.6 GB RSS; the host has ~1.8 GB
  available.

- **Deploy housekeeping (2026-10-25).** The deploy host (Raspberry Pi 4B,
  117 GB disk) had 24.6 GB of Docker build cache, growing with every
  `docker compose build`. After each deploy run `docker image prune -f` and
  `docker builder prune -f --filter until=168h` (README, "Production
  deployment"). Other projects' images on that host (nanoclaw, codex
  assistant, honcho) are not gopod's: do not prune them.

- **Agent admission control landed (ROADMAP T8, D026, branch
  feat/agent-gate, 2026-10-25).** Global agent gate in
  `internal/runner/gate.go` (Run/RunStream interactive, RunFresh
  background; `GOPOD_MAX_AGENTS`=2), MemAvailable check
  (`GOPOD_MIN_AVAILABLE_MB`=900, 60 s bound on an idle system), Claude
  `NODE_OPTIONS=--max-old-space-size` (`GOPOD_AGENT_NODE_HEAP_MB`=768) in
  `agentenv.go`, Telegram wait text in `internal/telegram/agentwait.go`.
  Also fixed: a Stop while a turn waited to start used to fall back to the
  sync path and run the agent anyway. Not verified live on the Pi: the
  wait draft/placeholder in Telegram, Stop on the wait draft, the
  MemAvailable value read inside the gopod container, and that claude
  honours the heap flag (`ps`/`/proc/<pid>/cmdline` and `environ` inside
  an agent container). `gopod_agents_running/waiting` metrics skipped
  (catalog is spec-checked; runner not instrumented yet).

## How to update this file

1. **Before starting work:** read this file. Read the docs it points at.
2. **While working:** if you notice something important but unrelated, drop
   it under "Nearby work noted but out of scope".
3. **Before stopping:** update "Current state", "What's done", "What's in
   progress", "What's next", "Open questions", "Blockers". Bump
   `Last updated`. Be specific — "fixed bug" is useless, "added
   `internal/store/messages.go:GetSince` and integration tests" is useful.
4. **After non-trivial decisions:** write an ADR in
   [DECISIONS.md](../DECISIONS.md) and link it from here.
