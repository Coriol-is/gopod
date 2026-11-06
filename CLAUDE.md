# gopod — agent instructions

You are working on **gopod**, a Go reimplementation of NanoClaw with reduced
scope. This file is loaded into your context on every session. Treat it as the
source of truth for *how* to work in this repo. The *what* lives in `docs/`.

## Read first, every session

> Codex and other harnesses enter through [AGENTS.md](AGENTS.md), which points back here.

1. **[docs/HANDOFF.md](docs/HANDOFF.md)** — current state, what's in flight,
   what's blocked, what to pick up next. **Always read this before doing
   anything else.** Update it before you stop working — run `/handoff`
   (`.claude/skills/handoff/`) for the procedure. A SessionStart hook
   (`.claude/hooks/handoff-status.sh`) reports at startup how many commits
   the file is behind HEAD; if it says the gap is large, trust ROADMAP.md
   and the git log over HANDOFF until you have reconciled them.
2. **[ROADMAP.md](ROADMAP.md)** — milestone status. Cross-check what HANDOFF
   says against the milestone you're touching. Read [docs/BACKLOG.md](docs/BACKLOG.md)
   for code-backed implementation gaps before selecting work. Status changes must
   distinguish definitions/tests from production callers, enabled configuration
   and live acceptance; refresh the backlog when those boundaries change.
3. **[docs/DECISIONS.md](docs/DECISIONS.md)** — already-decided questions.
   Don't re-litigate them; if you genuinely need to overturn one, append a
   superseding ADR rather than editing the old one in place.

## What this project is

A small personal Claude assistant on Telegram, written in Go. One channel,
one language, per-chat container isolation, native sqlite-vec memory.

A focused descendant of [NanoClaw](https://github.com/nanocoai/nanoclaw)
(clone it locally if you need the reference
implementation). gopod cherry-picks the architectural ideas that matter and
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
  with `GOPOD_NO_CONTAINER=1` disabling the runner. The originally planned
  dev subprocess fallback is not implemented.
- **Single SQLite file** for everything: chats, messages, tasks, sessions,
  state, memories, vectors. `data/store.sqlite`.
- **Per-chat isolation by path.** Authorization is enforced from the path of
  the file/folder/IPC namespace, never from data the LLM produced. If you need
  authorization, look up which chat folder the request came from and use that.
- **Owner chat is the only privileged chat.** Identified by
  `GOPOD_OWNER_CHAT_ID`. There is no `groups/main` analogue.
- **All admin/diagnostic commands go through `internal/control`.** Both
  Telegram slash commands and CLI subcommands. Authorization is enforced
  by the Router via `Perm` (`Public`/`ChatLocal`/`OwnerOnly`), never inside
  handlers. See [docs/CONTROL.md](docs/CONTROL.md) and [D011](docs/DECISIONS.md).
- **Secrets only in env vars.** No secret values in any committed config
  file — not `chats/<folder>/skills.json`, not `manifest.json`, not
  `.env.example`, nowhere. `os.Getenv` is the only source of truth. The
  slog redactor strips anything matching `(?i)token|key|secret|password|cookie|auth`
  from logs. See [D012](docs/DECISIONS.md).
- **Skills exist, but they never require recompiling gopod.** Three
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

### Commands with parameters

Owner convention (2026-10-30): a command that takes a parameter from a small known set must, whenever
possible, offer **inline-button pickers** when called without the parameter (the `/model`, `/provider`
and `/trigger` pattern: `Response.Buttons` / `pickerButtons`, the press re-dispatches the same command through
the Router, the current choice is marked, the message is edited in place). The text form
`/cmd <value>` keeps working. Authorization stays in the Router (`Perm`), never in the button. Free-form
parameters (ids, text) and destructive or credential-bearing actions use text arguments or the confirmation
buttons ([D028](docs/DECISIONS.md)). See [docs/CONTROL.md §6.1](docs/CONTROL.md).

### Layout

The package layout is fixed in [docs/ARCHITECTURE.md §5.1](docs/ARCHITECTURE.md).
Don't shuffle packages without an ADR.

### Docs

`docs/` is for designs that are meant to be stable. `ROADMAP.md` and
`docs/HANDOFF.md` are for live state. README is human-facing intro.
Anything ADR-worthy goes into `docs/DECISIONS.md` as `D{NNN}: <title>`.
Candidate changes that are not yet designed go into `docs/proposals/` as
`CP-NNN-<slug>.md` (template and lifecycle in
[docs/proposals/README.md](docs/proposals/README.md)); a CP is accepted by
the user before any spec or code exists for it.

### Commit messages

`<area>: <imperative summary>` where area is `runner`, `telegram`, `memory`,
`store`, `docs`, etc. Body explains *why*. No "fix bug", say which bug.

## Workflow rules

- **Permanent development cycle (owner-approved 2026-11-06).** For each completed
  implementation batch, continue through independent review and green required CI,
  squash merge, Pi controller deployment and acceptance, backlog/documentation/
  handoff reconciliation, then bounded housekeeping. These follow-through steps
  are part of the authorized development task; do not ask the owner to repeat
  `deploy / update backlog / housekeeping` after every batch. The latest explicit
  scope wins: Pi controller only by default, no worker or paused-feature activation.
  Check live idleness, retain rollback evidence, create and verify the required
  backup before rollout, and verify health/version/state afterwards. If a gate or
  connectivity blocks deployment, record the exact blocker and next action; never
  report merged code as deployed. Keep intermediate documentation checkpoints.
  Archive completed/cancelled backlog slices while retaining parent acceptance
  gaps. Housekeeping removes only proven-integrated clean task branches/worktrees
  and reviewed disposable artifacts; preserve others' dirty work, named backups,
  credentials, rollback evidence and unrelated services. No broad prune or deletion
  of user data is implied. Documentation-only batches do not require a runtime
  redeploy; use a docs PR and focused hygiene/link checks, keeping mandatory checks
  green before merge. Never push directly to main.

- **Autonomous Telegram acceptance (owner-approved 2026-11-03).** Agents may
  access Telegram themselves to perform acceptance checks for gopod, without
  asking the owner to send each message or approve each check. This includes
  reading the bot conversation, sending test messages, commands and attachments,
  exercising buttons where supported, and checking replies through the available
  Telegram CLI or client. Use the owner's bot conversation or designated test
  chats and keep actions scoped to the feature under acceptance. Record what was
  actually observed; API delivery alone does not prove visual client rendering.
  This permission does not authorize unrelated messages to other people,
  production deployment, host changes, or deletion of existing user data.
- **Before doing work:** read HANDOFF.md, the relevant doc(s), and the part
  of the code you're about to touch.
- **While doing work:** keep the change focused. If you hit something out of
  scope, write it down in HANDOFF.md as "noted" and keep moving.
- **Intermediate checkpoints (owner-requested 2026-11-03):** update HANDOFF.md
  and the affected documentation after each completed work stage, including
  acceptance findings, fixes and verification. Do not defer all state updates
  until the end of a multi-stage session.
- **After non-trivial changes:** run `/handoff` — it updates HANDOFF.md
  (state) and, if applicable, ROADMAP.md (milestone status), and prompts for
  an ADR in DECISIONS.md when you made a real architectural choice.
- **Git flow (owner-approved 2026-10-24).** Work happens on a feature branch
  in a git worktree, never directly on `main`. Commit there freely, open a
  PR, have it reviewed, wait for CI (`.github/workflows/ci.yml`) to go green,
  then squash-merge. Squash title follows `<area>: <imperative summary>`.
  Work is autonomous unless it needs the owner's approval or an
  architectural decision; those stop and ask.
- **Sonar is a soft gate (owner-approved 2026-11-03).** Build, vet, tests and
  Docker isolation in `.github/workflows/ci.yml` remain mandatory for source
  changes, merge and deployment. SonarCloud is advisory: a failed quality gate,
  cancelled/failed analysis, unavailable result or stale baseline does not block
  those actions or fail the CI/CD acceptance decision. Keep the Sonar check and
  findings visible; report its actual status and analysis freshness, and do not
  suppress findings or mark a failed gate successful. Use Sonar CLI for review.
  Wait for the `ci` workflow (`test` and `isolation-spike`) rather than requiring
  every external check returned by `gh pr checks` to succeed. This exception
  applies only to Sonar; it does not waive independent review or other checks.
- **Sonar production scope (owner-approved 2026-11-06).** Test files, fixtures, `scripts/**`, shell/hooks and all Python files
  are excluded from Sonar analysis entirely, not only from duplication metrics.
  Keep `.sonarcloud.properties` (automatic analysis) and `sonar-project.properties`
  (CLI/CI scanner) aligned: production source exclusions plus exclusion of all
  test scope. Verify actual analyzed file components after configuration changes.
  This does not change Go tests, race tests or mandatory CI. Existing historical
  findings remain evidence; do not individually suppress them to change the gate.
- **Deadcode pre-commit (owner-requested 2026-11-06).** Install the repository
  hook with `git config --local core.hooksPath .githooks`. Go-affecting commits
  analyze the staged snapshot with pinned deadcode for Linux/ARM64 including test
  roots. New unreachable functions and stale baseline entries fail. The explicit
  16-symbol baseline is an audit queue, not deletion approval; never auto-expand
  it to pass a commit. See [development checks](docs/DEVELOPMENT-CHECKS.md).
- **Documentation-only validation.** Use a feature branch and docs PR. Check `git diff --check`, changed local links and consistency with current project state; no additional local full test run is needed for prose alone. Required PR checks remain green before merge. Source, dependency, workflow and executable configuration changes retain full validation. This replaces the earlier direct-push exception.
- **Don't add features the user didn't ask for.** This project's discipline is
  *smaller than NanoClaw*. Resist scope creep aggressively.

## When you're stuck

The reference implementation is [NanoClaw](https://github.com/nanocoai/nanoclaw); clone it locally.
Read its source before guessing — it's the closest thing to authoritative
prior art for the architecture. Map TS files to gopod's Go packages using
the table in [docs/ARCHITECTURE.md §2](docs/ARCHITECTURE.md).

## What to never do

- Add a TypeScript file to this repo
- Add CGO to the main binary
- Add a second messaging channel
- Conflate skills with features (rebuild = feature, no rebuild = skill)
- Bypass `internal/control` to wire a slash command directly to a handler
- Put secret values in any committed file
- Skip reading HANDOFF.md
- Use destructive git operations (force-push, reset --hard, history
  rewrite, branch deletion on shared branches) without explicit user
  permission
- Push straight to `main` or merge a PR with red CI
- "Improve" code that the current task didn't ask you to touch
