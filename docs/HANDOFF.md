# picoclaw — Handoff

**Read this first when picking up the project.** Single source of truth for
"what is the state right now". Always update this file before you stop working.

> Companion docs: [../README.md](../README.md) · [../CLAUDE.md](../CLAUDE.md) · [../ROADMAP.md](../ROADMAP.md) · [ARCHITECTURE.md](ARCHITECTURE.md) · [DECISIONS.md](DECISIONS.md)

---

## Current state

**Phase:** Design. No code yet.
**Last updated:** 2026-04-09
**Last working session:** initial setup — research, architecture, memory design,
integration triage, skill ecosystem design, doc structure bootstrap, control
plane design, secrets policy lockdown.

## What's done

- ✅ NanoClaw architecture mapped (see [ARCHITECTURE.md §2](ARCHITECTURE.md))
- ✅ Library research: Telegram (`go-telegram/bot`), Claude SDK
  (`character-ai/claude-agent-sdk-go` + `anthropic-sdk-go`), SQLite
  (`ncruces/go-sqlite3` + `sqlite-vec`), cron (`robfig/cron/v3`), Docker
  (`docker/docker/client`)
- ✅ ADRs D001–D012 recorded
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
- ✅ No socket / HTTP control frontend in v0 (user decision 2026-04-09)
- ✅ Documentation structure: README, CLAUDE.md, ROADMAP, docs/{ARCHITECTURE,
  MEMORY, INTEGRATIONS, SKILLS, CONTROL, DECISIONS, GLOSSARY, HANDOFF}.md
- ✅ Sanity-check pass on the doc set: stale references, env var prefixes,
  ADR consistency, SKILLS hedge wording all reconciled

## What's in progress

Nothing actively in flight.

## What's next (in order)

1. **M0 — Skeleton.** Initialize `go.mod`, scaffold `cmd/picoclaw/main.go`,
   `internal/config`, `internal/store` (schema + open). Pick Go 1.22+. Wire
   `log/slog`. Verify `ncruces/go-sqlite3` + `sqlite-vec` registers correctly.
2. **M1 — Telegram echo.** Wire `go-telegram/bot` long polling, default
   handler stores every message in `messages` table, `/ping` slash command
   replies. No agent yet.
3. **M2 — Direct API agent.** Use `anthropic-sdk-go` directly for the first
   end-to-end response. One chat folder, no tools, plain text in/out.
4. (continue with ROADMAP)

## Open questions

- **`character-ai/claude-agent-sdk-go` Client custom command.** D003 assumes
  the SDK lets us override the executable so we can point it at
  `docker exec -i <name> claude`. Verify this in the SDK source before M6.
  If not, alternative is to embed `claude` directly in the host process and
  drop the container layer for that path (loses isolation — undesirable).
- **Telegram privacy mode.** Bots in groups by default only see commands,
  mentions, and replies. Setup docs need to call this out explicitly when M1
  lands. Decide whether to require BotFather privacy-mode disable, or
  document the tradeoff.
- **Embedding dim default.** MEMORY.md proposes OpenAI
  `text-embedding-3-small` truncated to 1024. Confirm OpenAI's
  `dimensions` parameter behaves as expected before freezing M9.
- **Control plane open Qs** ([CONTROL.md §13](CONTROL.md)):
  chat-local commands in unregistered chats (silent ignore vs reply),
  `/help` rendering across permission levels, rate-limiting (probably no),
  inline-keyboard confirmations for destructive ops (probably defer).
- **Telemetry / tracing stack.** New ask: add OTel tracing + Prometheus
  metrics. Not yet designed. See "Nearby work noted but out of scope".
- **Container isolation policy doc.** New ask: write down NanoClaw-style
  Docker isolation (RO mounts, mount allowlist, blocked patterns, --user
  uid:gid, .env redaction). ARCHITECTURE.md only sketches it. See "Nearby
  work noted but out of scope".

## Blockers

None.

## Nearby work noted but out of scope

(Things you bumped into and want to revisit later — write them here so they
don't get lost.)

- **Telemetry / tracing layer.** User asked for OTel + Prometheus. Need a
  `docs/OBSERVABILITY.md` design doc + ADR + a metrics endpoint decision
  (likely a localhost:9090/metrics for Prometheus pull, OTLP exporter for
  traces). Not yet started.
- **Container isolation deep doc.** ARCHITECTURE.md §7 has a per-chat
  isolation table but the full security policy (mount allowlist outside
  the repo, blocked-path patterns, symlink resolution, --user uid:gid, RO
  project mount, .env → /dev/null) isn't written down. Need a dedicated
  `docs/ISOLATION.md` mirroring NanoClaw's `src/mount-security.ts` and
  `src/container-runner.ts`. Not yet started.

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
