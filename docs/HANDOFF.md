# picoclaw — Handoff

**Read this first when picking up the project.** Single source of truth for
"what is the state right now". Always update this file before you stop working.

> Companion docs: [../README.md](../README.md) · [../CLAUDE.md](../CLAUDE.md) · [../ROADMAP.md](../ROADMAP.md) · [ARCHITECTURE.md](ARCHITECTURE.md) · [DECISIONS.md](DECISIONS.md)

---

## Current state

**Phase:** M0 done. Code: skeleton only (config, store, main).
**Last updated:** 2026-04-09
**Last working session:** initial setup + M0 — research, architecture, memory
design, integration triage, skill ecosystem design, doc structure bootstrap,
control plane design, secrets policy lockdown, container isolation policy,
observability design, ADR sync (D013–D016), `.gitignore`, then M0 skeleton:
`go.mod`, `internal/config`, `internal/store` (with full schema including
`memory_vec` vec0 virtual table), `cmd/picoclaw/main.go`, smoke-tested
end-to-end. ncruces version compatibility wrinkle pinned in [D017](DECISIONS.md).

## What's done

- ✅ **M0 — Skeleton landed.** `go.mod` (Go 1.24, autobumped by `go mod tidy`
  to satisfy `tetratelabs/wazero` v1.11; ncruces pinned to v0.20.0
  per [D017](DECISIONS.md)), `internal/config` with env loader + tests,
  `internal/store` opening SQLite via `ncruces/go-sqlite3` with sqlite-vec
  bundled in via the asg017 binding, full schema applied idempotently
  (chats, messages, registered_chats, sessions, scheduled_tasks,
  task_run_logs, router_state, memories, memory_vec vec0, memory_fts5 +
  triggers), runtime check of `vec_version()`, KNN smoke test, and
  `cmd/picoclaw/main.go` that wires slog (text|json), opens the store,
  blocks on SIGINT/SIGTERM, exits cleanly. `go test ./...` is green.
- ✅ NanoClaw architecture mapped (see [ARCHITECTURE.md §2](ARCHITECTURE.md))
- ✅ Library research: Telegram (`go-telegram/bot`), Claude SDK
  (`character-ai/claude-agent-sdk-go` + `anthropic-sdk-go`), SQLite
  (`ncruces/go-sqlite3` + `sqlite-vec`), cron (`robfig/cron/v3`), Docker
  (`docker/docker/client`)
- ✅ ADRs D001–D017 recorded
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
- ✅ `.gitignore` for Go + macOS + picoclaw runtime (data/, *.sqlite, .env)

## What's in progress

Nothing actively in flight.

## What's next (in order)

1. **M1 — Telegram echo.** Wire `go-telegram/bot` long polling, default
   handler stores every message in the `messages` table (which already
   exists from M0), `/ping` slash command replies. No agent yet. New env
   var: `TELEGRAM_BOT_TOKEN` (required from M1 onward — extend
   `internal/config` accordingly).
2. **M2 — Direct API agent.** Use `anthropic-sdk-go` directly for the first
   end-to-end response. One chat folder, no tools, plain text in/out.
   New env var: `ANTHROPIC_API_KEY`.
3. **M3 — GroupQueue.** Per-chat serialization + global cap + backoff.
4. **M3.5 — Control plane** (`internal/control` Router + Telegram + CLI
   frontends + logs subsystem). M4+ depend on this.
5. (continue with ROADMAP M3.6, M4, …)

## Open questions

- **Control plane open Qs** ([CONTROL.md §13](CONTROL.md)):
  chat-local commands in unregistered chats (silent ignore vs reply),
  `/help` rendering across permission levels, rate-limiting (probably no),
  inline-keyboard confirmations for destructive ops (probably defer).
- **ARCHITECTURE.md drift on mount allowlist location.** §3 still says
  "compile-time path policy + a small `mount.json` next to binary". D013 /
  ISOLATION.md superseded that with `${DATA_DIR}/mount-allowlist.json`.
  Reword §3 the next time you touch ARCHITECTURE.md (low priority — the
  authoritative location is ISOLATION.md).
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
  picoclaw setup docs (when M1 lands) will require disabling privacy mode
  because picoclaw is supposed to see every message in its registered
  chats; the usual user-privacy concern doesn't apply because privileges
  are enforced by `PICOCLAW_OWNER_CHAT_ID`. No rate-limit penalties for
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
