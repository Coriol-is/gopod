# picoclaw — agent instructions

You are working on **picoclaw**, a Go reimplementation of NanoClaw with reduced
scope. This file is loaded into your context on every session. Treat it as the
source of truth for *how* to work in this repo. The *what* lives in `docs/`.

## Read first, every session

1. **[docs/HANDOFF.md](docs/HANDOFF.md)** — current state, what's in flight,
   what's blocked, what to pick up next. **Always read this before doing
   anything else.** Update it before you stop working.
2. **[ROADMAP.md](ROADMAP.md)** — milestone status. Cross-check what HANDOFF
   says against the milestone you're touching.
3. **[docs/DECISIONS.md](docs/DECISIONS.md)** — already-decided questions.
   Don't re-litigate them; if you genuinely need to overturn one, append a
   superseding ADR rather than editing the old one in place.

## What this project is

A small personal Claude assistant on Telegram, written in Go. One channel,
one language, per-chat container isolation, native sqlite-vec memory.

A focused descendant of [NanoClaw](https://github.com/spaceinvaderz/nanoclaw)
(see `/Users/<user>/_code/gh-public/nanoclaw` if you need the reference
implementation). picoclaw cherry-picks the architectural ideas that matter and
drops everything else.

## Hard constraints (don't break these without an ADR)

- **Language: Go**, no TypeScript inside this repo. The agent inside the
  container is the `claude` CLI, talked to over `docker exec`.
- **No CGO** in the main binary. The chosen SQLite driver is
  `ncruces/go-sqlite3` (WASM, pure-Go) specifically because it supports
  `sqlite-vec` without CGO.
- **One channel: Telegram.** Library: [`go-telegram/bot`](https://github.com/go-telegram/bot).
  No channel registry. No abstraction over messaging providers.
- **One runtime: Docker** via the official `docker/docker/client` Go SDK,
  with a `PICOCLAW_NO_CONTAINER=1` subprocess fallback for dev only.
- **Single SQLite file** for everything: chats, messages, tasks, sessions,
  state, memories, vectors. `data/store.sqlite`.
- **Per-chat isolation by path.** Authorization is enforced from the path of
  the file/folder/IPC namespace, never from data the LLM produced. If you need
  authorization, look up which chat folder the request came from and use that.
- **Owner chat is the only privileged chat.** Identified by
  `PICOCLAW_OWNER_CHAT_ID`. There is no `groups/main` analogue.
- **All admin/diagnostic commands go through `internal/control`.** Both
  Telegram slash commands and CLI subcommands. Authorization is enforced
  by the Router via `Perm` (`Public`/`ChatLocal`/`OwnerOnly`), never inside
  handlers. See [docs/CONTROL.md](docs/CONTROL.md) and [D011](docs/DECISIONS.md).
- **Secrets only in env vars.** No secret values in any committed config
  file — not `chats/<folder>/skills.json`, not `manifest.json`, not
  `.env.example`, nowhere. `os.Getenv` is the only source of truth. The
  slog redactor strips anything matching `(?i)token|key|secret|password|cookie|auth`
  from logs. See [D012](docs/DECISIONS.md).
- **Skills exist, but they never require recompiling picoclaw.** Three
  types: container skills (`container/skills/<name>/SKILL.md`), MCP skills
  (`skills/mcp/<name>/manifest.json`), dev skills (`.claude/skills/<name>/`).
  Anything that requires `go build` is a *feature*, not a skill — it goes
  into `internal/` as a regular package. See [docs/SKILLS.md](docs/SKILLS.md)
  and [docs/DECISIONS.md D009](docs/DECISIONS.md).

## Conventions

### Code

- Idiomatic Go. Small files. `internal/<package>/<file>.go` over giant monoliths.
- `log/slog` for logs, structured. Never `fmt.Println` in non-CLI code.
- `context.Context` first parameter on anything that might block, do I/O, or
  outlive the immediate call.
- Errors: wrap with `fmt.Errorf("doing X: %w", err)`. No silent swallowing.
- Tests live next to code: `foo.go` + `foo_test.go`. Use `testing` stdlib;
  add `testify/require` only if it materially helps.
- DB access goes through `internal/store`. No raw `database/sql` calls
  outside that package.
- One exported `New(...)`/`Open(...)` per package, returns a struct value
  (not a pointer to interface). Interfaces are declared at the consumer side.

### Layout

The package layout is fixed in [docs/ARCHITECTURE.md §5.1](docs/ARCHITECTURE.md).
Don't shuffle packages without an ADR.

### Docs

`docs/` is for designs that are meant to be stable. `ROADMAP.md` and
`docs/HANDOFF.md` are for live state. README is human-facing intro.
Anything ADR-worthy goes into `docs/DECISIONS.md` as `D{NNN}: <title>`.

### Commit messages

`<area>: <imperative summary>` where area is `runner`, `telegram`, `memory`,
`store`, `docs`, etc. Body explains *why*. No "fix bug", say which bug.

## Workflow rules

- **Before doing work:** read HANDOFF.md, the relevant doc(s), and the part
  of the code you're about to touch.
- **While doing work:** keep the change focused. If you hit something out of
  scope, write it down in HANDOFF.md as "noted" and keep moving.
- **After non-trivial changes:** update HANDOFF.md (state) and, if applicable,
  ROADMAP.md (milestone status). If you made a real architectural choice,
  append an ADR to DECISIONS.md.
- **Never commit unless the user explicitly asks.** This applies even if a
  task feels "complete".
- **Don't add features the user didn't ask for.** This project's discipline is
  *smaller than NanoClaw*. Resist scope creep aggressively.

## When you're stuck

The reference implementation is at `/Users/<user>/_code/gh-public/nanoclaw`.
Read its source before guessing — it's the closest thing to authoritative
prior art for the architecture. Map TS files to picoclaw's Go packages using
the table in [docs/ARCHITECTURE.md §2](docs/ARCHITECTURE.md).

## What to never do

- Add a TypeScript file to this repo
- Add CGO to the main binary
- Add a second messaging channel
- Conflate skills with features (rebuild = feature, no rebuild = skill)
- Bypass `internal/control` to wire a slash command directly to a handler
- Put secret values in any committed file
- Skip reading HANDOFF.md
- Use destructive git operations without explicit user permission
- Commit without explicit user permission
- "Improve" code that the current task didn't ask you to touch
