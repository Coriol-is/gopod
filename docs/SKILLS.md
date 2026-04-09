# picoclaw — Skill ecosystem

> Companion docs: [ARCHITECTURE.md](ARCHITECTURE.md) · [MEMORY.md](MEMORY.md) · [INTEGRATIONS.md](INTEGRATIONS.md) · [DECISIONS.md](DECISIONS.md)
> Decision: [D009](DECISIONS.md)

picoclaw initially planned to drop NanoClaw's skill ecosystem entirely. After
revisiting, **we keep it** — but in a form that fits Go and the 2026 Anthropic
ecosystem rather than NanoClaw's TypeScript-shaped four-tier system.

The single discriminating principle:

> **A skill never requires recompiling picoclaw.**
> If it does, it is a *feature* (a regular `internal/` package conditionally
> enabled by env var), not a skill.

Everything below follows from that line.

---

## 1. Type 1 — Container skills (Claude Code SKILL.md)

The simplest tier. Drop a directory, restart the chat's container, the agent
has a new skill.

### Anatomy

```
container/skills/<skill-name>/
├── SKILL.md             # required: YAML frontmatter + markdown instructions
├── scripts/             # optional: helper scripts the agent can call
│   └── do-thing.sh
├── references/          # optional: reference docs the agent can read
│   └── api-spec.md
└── assets/              # optional: static files the agent can read/copy
    └── template.tex
```

### SKILL.md format

The same standardized format Claude Code already uses (`~/.claude/skills/<name>/SKILL.md`):

```markdown
---
name: pdf-extract
description: Extract text from PDFs in /workspace using pdftotext. Use when
  the user uploads a PDF or asks about a PDF in the chat.
---

# PDF extraction

When you see a PDF in `/workspace/chat/attachments/`, run:

    scripts/extract.sh <path-to-pdf>

This produces `<basename>.txt` next to the PDF. Read the .txt file for content.
For multi-page PDFs, prefer page-by-page extraction with `pdfinfo` first.
```

### How they get loaded

At chat container startup, picoclaw bind-mounts:

```
container/skills/  →  /home/node/.claude/skills/   (read-only)
```

Claude Code's **native skill discovery** picks them up automatically — there
is no picoclaw-specific loader code. The frontmatter `description` field is
how Claude decides when to invoke a skill, exactly as in standalone Claude
Code.

### Per-chat enable/disable

By default all container skills are visible in every chat. To restrict:

```
chats/<folder>/skills.json
{
  "container": {
    "allow": ["pdf-extract", "image-describe"],
    "deny": ["dangerous-thing"]
  }
}
```

picoclaw filters the mount at container start: only allowed skill directories
are bind-mounted into that chat's container.

### Source compatibility

`anthropics/skills` and the broader Claude Code skill marketplace publish
skills in this exact format. picoclaw's `container/skills/` directory can
contain a `git submodule` of any public skill repo, no conversion needed.

### Hot reload

Not in v0. Skill changes require a chat container restart (cheap — idle kill
or `picoclaw restart-chat <folder>`). Hot reload is straightforward to add
later via fsnotify if it becomes a real annoyance.

---

## 2. Type 2 — MCP skills (process-based tool plugins)

The most powerful tier. An MCP server is a separate process that exposes
tools to the agent via JSON-RPC over stdio. Adding a new MCP skill gives the
agent new tools without recompiling picoclaw and without modifying any
existing code.

This is also how the broader Claude ecosystem distributes integrations
(GitHub MCP, Postgres MCP, Filesystem MCP, etc.). picoclaw consumes them
directly.

### Anatomy

```
skills/mcp/<skill-name>/
├── manifest.json        # required
├── README.md            # optional, human docs
└── (any bundled binary or script)
```

### Manifest format

```json
{
  "name": "github",
  "description": "GitHub MCP server: repos, issues, PRs",
  "transport": "stdio",
  "command": "npx",
  "args": ["-y", "@modelcontextprotocol/server-github"],
  "env": {
    "GITHUB_TOKEN": "${GITHUB_TOKEN}"
  },
  "scope": "owner",
  "auto_install": {
    "type": "npm",
    "package": "@modelcontextprotocol/server-github"
  },
  "lifecycle": "per-chat"
}
```

Field reference:

| Field | Required | Meaning |
|---|---|---|
| `name` | ✅ | Short identifier, used as a tool prefix and in `skills.json` |
| `description` | ✅ | One-line summary; logged on load |
| `transport` | ✅ | `stdio` or `http` |
| `command` / `args` | for stdio | Process to spawn |
| `url` | for http | Base URL of an existing MCP HTTP server |
| `env` | optional | Env vars passed to the spawned process. `${VAR}` substitutes from picoclaw's environment. |
| `scope` | ✅ | `chat` (every chat gets its own instance), `owner` (only the owner chat sees it), `global` (one instance shared by all chats) |
| `auto_install` | optional | If set, picoclaw installs the package on first use (`npm`, `pip`, `go install`, `uvx`) — analogous to how `claude mcp add` registers servers |
| `lifecycle` | optional | `per-chat` (spawn at chat container start, kill at idle), `daemon` (start with picoclaw, never killed) |

### How they get wired

`internal/skills/mcp` runs at chat container startup:

1. Read every `skills/mcp/*/manifest.json`.
2. Apply `chats/<folder>/skills.json` allow/deny filter.
3. For each enabled, in-scope manifest:
   - If `lifecycle=per-chat`: spawn the process.
   - If `lifecycle=daemon` and not already running: spawn it.
   - If `transport=http`: just record the URL.
4. Use `character-ai/claude-agent-sdk-go`'s MCP support to connect the agent
   to each server and register its tools with the agent's `ToolRegistry`.
5. Tools are exposed to the agent as `<name>__<tool>` (prefixed) so two
   skills can have a same-named tool without colliding.

### Authorization

- A `chat`-scoped MCP skill is spawned per chat. The `${VAR}` placeholders
  in its `manifest.json` `env` field are substituted from picoclaw's
  process environment ([D012](DECISIONS.md)) — never from any committed
  config file. Owner chats see the full environment; non-owner chats see
  only the variables whose **names** appear in `chats/<folder>/secrets.allow`
  (a list of names, never values).
- An `owner`-scoped skill is only registered for the owner chat. Even if a
  non-owner chat enables it via `skills.json`, picoclaw refuses.
- A `global`-scoped skill runs once and is shared. Use sparingly — its tool
  outputs cross chat boundaries.

### Provenance

picoclaw treats `auto_install` packages with the same trust as any other
`npm`/`pip` install: it does it, it logs it, it doesn't sandbox it. Skills
that need sandboxing should run inside the per-chat container the host
already isolates. Document this expectation in `skills/mcp/README.md`.

### Examples we'd ship

| Skill | What it gives the agent |
|---|---|
| `github` | Repo browsing, issues, PRs, code search |
| `filesystem-extra` | Read/write outside `/workspace/chat/` (owner only) |
| `web-search` | Brave/Tavily/Perplexity-backed search |
| `gmail-mcp` | Read/send mail (replaces Tier 2 Gmail tool from INTEGRATIONS.md if MCP version is good enough) |
| `ollama` | Local model queries (replaces Tier 2 #11 by being a generic MCP) |

Anything in the public MCP server registry is one manifest away.

---

## 3. Type 3 — Dev skills (Claude Code skills for picoclaw maintainers)

The smallest tier. Pure Claude Code slash commands for the *developer*
working on picoclaw via Claude Code in this repo. Not loaded at runtime by
picoclaw itself — they live in `.claude/skills/` and exist only for the
human + Claude pair-programming session.

### Examples

| Skill | What it does |
|---|---|
| `/release` | Cuts a release: bump version, generate changelog, build container, push image |
| `/migrate-schema` | Scaffolds a new SQL migration file under `internal/store/schema/` |
| `/add-mcp-skill <name>` | Scaffolds `skills/mcp/<name>/manifest.json` from a template, opens the file |
| `/add-container-skill <name>` | Scaffolds `container/skills/<name>/SKILL.md` from a template |
| `/handoff` | Updates `docs/HANDOFF.md` with a structured "what changed" summary |
| `/check` | Runs `go vet`, `go test ./...`, `golangci-lint run` and reports |

These are added the same way any Claude Code skill is added — drop a
directory under `.claude/skills/` with a `SKILL.md`. They are committed to
the repo so every contributor's Claude session has them.

---

## 4. Mapping from NanoClaw skills

The four NanoClaw skill types collapse:

| NanoClaw type | NanoClaw mechanism | picoclaw equivalent |
|---|---|---|
| **Feature skill** (e.g. `add-whatsapp`) | Merge a `skill/*` git branch that adds source code | **Not a skill.** Becomes a regular `internal/` package, conditionally enabled by env var. The boundary is sharp: anything that needs `go build` is a feature. |
| **Utility skill** (e.g. `claw` CLI) | Code files alongside SKILL.md, runs on dev machine | **Dev skill** (Type 3) if it's a dev tool, or a small standalone Go binary in `cmd/` |
| **Operational skill** (e.g. `/setup`, `/debug`) | Instruction-only, always on main branch | **Dev skill** (Type 3) |
| **Container skill** (e.g. browser tools) | `container/skills/` mounted into the agent container | **Container skill** (Type 1), mechanism unchanged |

NanoClaw's biggest source of complexity — the `skill/*` branch merge model
with external git remotes — does **not** exist in picoclaw. Adding a feature
to picoclaw means writing Go code in a feature branch and merging a normal
PR. Skills are reserved for things you can install without touching the Go
source.

---

## 5. CLI surface

The skill commands are part of the unified control plane
([CONTROL.md](CONTROL.md)) — `picoclaw skills *` and `/skills *` (Telegram)
share the same handlers and the same auth model.

```
picoclaw skills list                    # list all installed skills
picoclaw skills enable <name> [--chat <folder>]
picoclaw skills disable <name> [--chat <folder>]
picoclaw skills test <name>             # spawn the MCP server, list its tools, exit
picoclaw skills install <git-url>       # git clone into container/skills/ or skills/mcp/
```

`install` detects the type from the directory contents:

- Has `SKILL.md` at root → container skill, clone into `container/skills/`
- Has `manifest.json` at root → MCP skill, clone into `skills/mcp/`
- Otherwise → reject with a helpful error

---

## 6. Discovery & registry

v0: no managed registry. Users install skills by `git clone` or
`picoclaw skills install <url>`. The format is the standard Claude Code
SKILL.md and the standard MCP manifest, so anything in
`anthropics/skills` or the broader MCP server lists works without
conversion.

v1+ (deferred): a small `picoclaw skills search <query>` that hits a
known list of registries (`anthropics/skills`, MCP server registry).
Not before the core works.

---

## 7. Module layout

```
internal/skills/
├── skills.go            # Loader interface, types, allow/deny filtering
├── container.go         # Type 1: bind-mount enumeration + filter
├── mcp.go               # Type 2: manifest parsing, lifecycle, env substitution
├── mcp_stdio.go         # stdio transport: spawn/attach/teardown
├── mcp_http.go          # http transport
├── registry.go          # In-memory map of loaded skills per chat
├── cli.go               # picoclaw skills * command implementations
└── skills_test.go
```

Wired from `internal/runner/runner.go` immediately before each agent
invocation: `skills.PrepareForChat(chatFolder)` returns the set of mounts
(for the container) and the set of MCP tool registrations (for the SDK).

---

## 8. Status — designed, not built

This is a **design**, not implementation. Tracked as Phase 3 in
[ROADMAP.md](../ROADMAP.md):

| ID | Step |
|----|------|
| S1 | Container skills: bind-mount + per-chat filter |
| S2 | MCP skills: manifest parser, stdio transport, lifecycle, env substitution |
| S3 | MCP skills: per-chat allow/deny + scope enforcement |
| S4 | `picoclaw skills` CLI subcommand |
| S5 | Dev-time skills: `.claude/skills/{release, check, add-mcp-skill, add-container-skill, handoff, migrate-schema}` |

Phase 3 is **independent of Phase 2 (integrations)**. Some Tier-1
integrations from [INTEGRATIONS.md](INTEGRATIONS.md) — particularly Ollama
(I8) and Gmail tool mode (I9) — may end up shipping as MCP skills instead
of as Go packages, in which case they move to Phase 3 and out of
INTEGRATIONS.md. Decide per-skill at implementation time, document the
choice in HANDOFF.md.

---

## 9. Sources

- [Claude Code — Connect to MCP servers](https://code.claude.com/docs/en/mcp)
- [Connect to local MCP servers — modelcontextprotocol.io](https://modelcontextprotocol.io/docs/develop/connect-local-servers)
- [Claude Code — Extend Claude with skills](https://code.claude.com/docs/en/skills)
- [Agent Skills overview — platform.claude.com](https://platform.claude.com/docs/en/agents-and-tools/agent-skills/overview)
- [anthropics/skills](https://github.com/anthropics/skills) — public skill repository
- NanoClaw `container/skills/` and `.claude/skills/` for the prior art
