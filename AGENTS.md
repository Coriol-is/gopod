# gopod — agent entry point (Codex and other harnesses)

`CLAUDE.md` is the single source of truth for project rules; this file exists
because Codex reads `AGENTS.md`, not `CLAUDE.md`. Do not duplicate rules here.

## Start every session

1. Read [`CLAUDE.md`](CLAUDE.md) in full: hard constraints, conventions,
   workflow rules and the "What to never do" list apply to every harness.
2. Read [`docs/HANDOFF.md`](docs/HANDOFF.md), starting with **Resume here**.
3. Cross-check [`ROADMAP.md`](ROADMAP.md) and [`docs/DECISIONS.md`](docs/DECISIONS.md)
   before touching a milestone or revisiting a decision.

## Harness notes

- Skills in `.claude/skills/` (e.g. `handoff`) are Claude Code skills. Codex: follow
  the same procedure by hand — update `docs/HANDOFF.md` (and `ROADMAP.md` when a
  milestone moved) before stopping; `.claude/skills/handoff/SKILL.md` lists the steps.
- `git -c core.fsmonitor=false ...` if fsmonitor fails.
- Git flow is identical for every agent: feature branch in a separate worktree
  (`git worktree add ../gopod-<topic> -b <branch> main`), never edit `main` directly,
  PR, green CI (`.github/workflows/ci.yml`), squash-merge with `<area>: <summary>`.
  No force-push, no pushes to `main`, no remote branch deletion without owner approval.
- Verify with `go build ./... && go vet ./... && go test ./...` (add `-race` for
  concurrency changes). CI also runs the Docker isolation job; it needs Docker.
- Env var names the agent must see must not contain KEY/SECRET/TOKEN (Codex strips them).
- Do not add attribution lines to commits or PR bodies.
- Never put secrets in committed files; secrets live in env vars only.
- Owner-facing chat language is Russian; repository artifacts are English.
