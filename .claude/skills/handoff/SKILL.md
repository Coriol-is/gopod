---
name: handoff
description: Update docs/HANDOFF.md (and ROADMAP.md if a milestone moved) at the end of a gopod working session. Use when wrapping up work, when the SessionStart hook reports HANDOFF is behind HEAD, or when the user says "handoff", "update handoff", "закрой сессию".
---

# gopod handoff

`docs/HANDOFF.md` is the only file a fresh session reads to learn where the
project actually is. It is worth exactly as much as its last update. This
skill is the write half of that contract; the SessionStart hook
(`.claude/hooks/handoff-status.sh`) is the read half.

## When to run

- Before you stop working, after any non-trivial change.
- When the session opened with `handoff: docs/HANDOFF.md is N commit(s)
  behind HEAD`.
- Never as a substitute for doing the work — a handoff describing work you
  did not finish is worse than a stale one.

## Procedure

### 1. Find out what actually changed

```bash
.claude/hooks/handoff-status.sh --text
last=$(git log -1 --format=%h -- docs/HANDOFF.md)
git log --oneline "$last..HEAD"
git diff --stat "$last..HEAD"
git status --short
```

Read the commits, not just their subjects. The handoff has to say what the
code does now, which a subject line usually does not carry.

### 2. Read the current file before editing it

Read `docs/HANDOFF.md` end to end. You are editing live state, not appending
to a log — stale claims must be corrected or deleted, not left standing
beside new ones.

### 3. Rewrite the sections that moved

| Section | What goes in it |
|---|---|
| `Current state` | One paragraph: what gopod *is* right now. Bump `**Last updated:**` to today's date (absolute, `YYYY-MM-DD`). |
| `What's done` | Move finished items here with enough specificity that a stranger could verify the claim. `internal/runner/ansi.go:stripTerminalEscapes strips CSI+OSC`, not `fixed login`. |
| `What's in progress` | Only things genuinely mid-flight. If nothing is, say so. |
| `What's next` | Ordered. Strike through what shipped rather than silently dropping it. |
| `Open questions` / `Blockers` | Add what you hit; resolve what you answered. `None.` is a valid, useful answer. |
| `Nearby work noted but out of scope` | Anything you bumped into and deliberately did not do. This is where scope creep goes to be recorded instead of executed. |

### 4. Cross-check ROADMAP.md

Open `ROADMAP.md` and confirm the status emoji of every milestone you
touched. Drift here is common and silent: in September 2026 the table still
listed P3 and P4 as ⬜ while both were fully implemented in
`cmd/gopod/main.go`. If a status is wrong, fix it in the same pass.

### 4b. Cross-check the CP registry

Open `docs/proposals/README.md`. Every CP you touched this session must
have the right status in its own file *and* in the index table
(`proposed` → `accepted` → `in-progress` → `done`), and an `in-progress`
or `done` CP must link its spec. A CP that shipped gets a ROADMAP row in
the same pass.

### 5. ADR if you decided something

A real architectural choice — one a future session could plausibly reverse
by accident — gets an entry in `docs/DECISIONS.md` as `D{NNN}: <title>`, and
a link from HANDOFF. Never edit an existing ADR to change its meaning;
append a superseding one.

### 6. Stop

Do not commit. `CLAUDE.md` forbids committing without an explicit request
from the user — report what you changed and let them ask.

## Quality bar

The test is: could a session with an empty context window read only
HANDOFF.md and pick up your work without reading the diff? If a line would
leave that reader guessing, it is not specific enough.
