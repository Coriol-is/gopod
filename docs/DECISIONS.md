# picoclaw — Architectural Decision Log

ADR-style. Append-only. To overturn an old decision, write a new entry that
references and supersedes it (`Supersedes: D###`); never delete or rewrite
the old one in place. Each entry: **context → decision → consequences**.

> Companion docs: [ARCHITECTURE.md](ARCHITECTURE.md) · [MEMORY.md](MEMORY.md) · [INTEGRATIONS.md](INTEGRATIONS.md) · [SKILLS.md](SKILLS.md)

---

## D001 — Telegram-only, no channel registry

**Date:** 2026-04-09
**Status:** Accepted

**Context.** NanoClaw supports many channels (WhatsApp, Telegram, Slack,
Discord, Gmail, Signal, Emacs, X) via a self-registering channel registry.
This is the largest source of architectural complexity in NanoClaw.

**Decision.** picoclaw supports exactly one channel — Telegram — wired
statically into `internal/telegram`. No registry, no abstraction over
messaging providers, no JID prefix system.

**Consequences.** Trivial code paths in `cmd/picoclaw` and `internal/runner`.
Adding a second channel later would be a real refactor; that is acceptable
because we don't intend to.

---

## D002 — Go, no TypeScript in repo

**Date:** 2026-04-09
**Status:** Accepted

**Context.** NanoClaw is TypeScript end-to-end. We want a smaller, faster
host process. Go gives us static binaries, easy concurrency, and a great
Docker SDK.

**Decision.** Host process is Go. The agent inside the container is the
`claude` CLI itself, talked to over `docker exec` via the
`character-ai/claude-agent-sdk-go` `Client` type with a custom command. No
TypeScript shim, no `agent-runner/` source folder.

**Consequences.** End-to-end Go in this repo. The agent's tool ecosystem
(Read, Write, Bash, Edit, Grep, MCP) is still available because Claude Code
provides it natively inside the container.

---

## D003 — Container-per-chat, long-lived, exec-per-turn

**Date:** 2026-04-09
**Status:** Accepted

**Context.** NanoClaw spawns a fresh container per message. That works but
wastes the heavy `claude` CLI startup. We want isolation per chat without
paying startup on every turn.

**Decision.** Container is long-lived per chat (label
`picoclaw.chat=<folder>`). Each message triggers a fresh `docker exec` of
`claude` against the running container. Idle-killed after
`PICOCLAW_IDLE_TIMEOUT` (default 30 min). On startup, leftover containers are
listed and stopped.

**Consequences.** Cheaper turn latency. State recovery for orphaned containers
is straightforward (label-based listing). If `docker exec` attach proves flaky
in practice, fall back to NanoClaw's per-message model — note in HANDOFF.md.

---

## D004 — SQLite driver: `ncruces/go-sqlite3` (WASM, pure Go)

**Date:** 2026-04-09
**Status:** Accepted
**Supersedes:** initial draft choice of `modernc.org/sqlite` in early
ARCHITECTURE.md notes (never recorded as an ADR)

**Context.** picoclaw needs vector search for the long-term memory layer.
`sqlite-vec` is the only actively maintained vector extension for SQLite. Its
official Go bindings cover three drivers: `mattn/go-sqlite3` (CGO),
`ncruces/go-sqlite3` (WASM, no CGO), and a third-party `viant/sqlite-vec`
adapter for `modernc.org/sqlite` that exposes only the cosine/l2 helpers and
no `vec0` virtual table.

**Decision.** Use `ncruces/go-sqlite3`. It is pure Go (no CGO), maintains
full feature parity for our needs, and has first-class `sqlite-vec` WASM
binary support including `vec0`.

**Consequences.** Slightly higher per-call overhead than `modernc` due to
WASM, but personal-assistant scale doesn't notice. Vector search is a
first-class feature, not bolted on.

---

## D005 — Layered memory model (5 layers)

**Date:** 2026-04-09
**Status:** Accepted

**Context.** Honcho is a strong dedicated memory product but it's an external
service. We can do better than NanoClaw's "agent writes notes into the
workspace" approach without taking a network dependency.

**Decision.** Five layers: working (SQL `messages`), identity (`CLAUDE.md`),
scratchpad (Anthropic Memory Tool), semantic (`sqlite-vec`), cross-chat
(`_global` scope). Honcho is designed-in as Layer 5 but **deferred** until
the local stack proves insufficient. Full design in
[MEMORY.md](MEMORY.md).

**Consequences.** Memory is built-in, no external account needed. If the
identity-reasoning use case becomes critical, Honcho slots in cleanly.

---

## D006 — Owner chat replaces "main group" privilege

**Date:** 2026-04-09
**Status:** Accepted

**Context.** NanoClaw distinguishes a privileged "main group" with extra
filesystem access and the ability to schedule tasks across other groups.

**Decision.** picoclaw uses a single `PICOCLAW_OWNER_CHAT_ID` env var. The
chat with that ID is the only one that can register new chats, schedule
tasks for other chats, see the project root inside its container, and
mount the SQLite store RW.

**Consequences.** Simpler and less footgun-prone than NanoClaw's per-chat
`is_main` flag in the DB.

---

## D007 — Authorization by path, not by data

**Date:** 2026-04-09
**Status:** Accepted

**Context.** Inherited from NanoClaw. The IPC layer (container → host) reads
JSON files written by the agent. Trusting `chatJid` from JSON would let an
agent in chat X target chat Y.

**Decision.** Authorization is determined by which chat folder the request
came from (filesystem path), never from JSON content. Same rule applies to
memory tools, scheduler IPC, and any future cross-chat API.

**Consequences.** All cross-chat handlers must look up the chat from the
calling path before doing anything else.

---

## D008 — No OneCLI; secrets via env vars

**Date:** 2026-04-09
**Status:** Accepted

**Context.** NanoClaw uses OneCLI as a credential gateway, intercepting HTTPS
requests inside the container and injecting tokens. Secure but heavy.

**Decision.** picoclaw injects `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, etc.
into the container as environment variables on `ContainerCreate`. Owner chat
gets the full set; non-owner chats get only what they need.

**Consequences.** Acceptable downgrade for personal use. Revisit if/when
multi-credential isolation becomes necessary.

---

## D009 — Skill ecosystem: container skills + MCP, no branch-merge

**Date:** 2026-04-09
**Status:** Accepted
**Supersedes:** initial draft "no skill ecosystem" position in early notes
(never recorded as an ADR)

**Context.** NanoClaw has a four-tier skill ecosystem (feature, utility,
operational, container) where feature skills are added by merging
`skill/*` branches from external git remotes. This works but is brittle
and TypeScript-shaped — it doesn't translate to Go at all.

**Decision.** picoclaw ships a smaller, idiomatic-for-Go skill ecosystem:

1. **Container skills** — drop a directory into `container/skills/<name>/`
   containing a `SKILL.md` plus optional scripts. The directory is mounted
   into the agent container at `/home/node/.claude/skills/<name>/` exactly
   like NanoClaw's container skills. Zero Go code, zero rebuild.
2. **MCP skills** — drop a directory into `skills/mcp/<name>/` containing
   `manifest.json` (command, args, env, scope). picoclaw spawns the MCP
   server (or HTTP-connects to it) per chat at agent startup and registers
   its tools with the agent SDK. Adds new agent capabilities without
   recompiling picoclaw.
3. **Dev-time skills** — `.claude/skills/<name>/` with Claude Code slash
   commands for picoclaw *maintainers* (e.g. `/release`, `/migrate-schema`).
   Not loaded at runtime.

NanoClaw's "feature skills" (which add Go code) become **regular Go packages**
in `internal/`, conditionally enabled by env vars. They are not skills.

Full design in [SKILLS.md](SKILLS.md).

**Consequences.** picoclaw has an extension story without inheriting
NanoClaw's branch-merge complexity. The line between "skill" and "core
feature" is sharp: anything that requires recompiling picoclaw is a feature,
anything that doesn't is a skill.

---

## D010 — Drop macOS status-bar tray

**Date:** 2026-04-09
**Status:** Accepted

**Context.** NanoClaw has a macOS menubar tray skill. We considered porting
it via `getlantern/systray` as a separate `cmd/picoclaw-tray` binary.

**Decision.** Skip. User explicitly does not want it.

**Consequences.** No CGO ever needed in any picoclaw binary. Service
management stays CLI-only (launchd plist on macOS, systemd unit on Linux).

---

## D011 — Unified command gateway (`internal/control`)

**Date:** 2026-04-09
**Status:** Accepted

**Context.** picoclaw has a growing set of admin operations: scheduled task
control, chat registration, container restart, skill management, log
inspection, memory inspection, system health. Without a single dispatch
layer these end up scattered across `internal/telegram` slash handlers and
ad-hoc CLI subcommands, with duplicated authorization logic and divergent
output formats between Telegram and the CLI. NanoClaw exhibited exactly
this drift between its `claw` CLI and its in-chat slash commands.

**Decision.** All admin/diagnostic operations go through a single
`internal/control` package: `Router` + `Command` + `Caller` + `Perm` +
`Response`. Two frontends — Telegram and CLI — translate their input into
`Command` and call `Router.Dispatch`. Authorization is enforced by the
Router based on the handler's declared `Perm` (`Public`, `ChatLocal`,
`OwnerOnly`), so handlers cannot forget to check. Full design in
[CONTROL.md](CONTROL.md).

**Consequences.** One auth invariant. Telegram and CLI share handlers and
output rendering. New handlers are 1 file + 1 line of registration. Future
frontends (web UI, MCP-server-mode picoclaw, etc.) plug in as new
translators without touching handlers. The scheduler, skills loader, memory
layer, and queue all register their commands through the Router as part of
their own setup, so M4 onwards depends on M3.5 being in place.

---

## D012 — Secrets in environment variables only; never in config files

**Date:** 2026-04-09
**Status:** Accepted
**Refines:** [D008](#d008--no-onecli-secrets-via-env-vars)

**Context.** [D008](#d008--no-onecli-secrets-via-env-vars) decided to drop
OneCLI and pass credentials as environment variables on `ContainerCreate`.
That left several details unresolved: where do secrets actually live on
disk? What is the format of the per-chat allow-list? Can a chat have its
own credentials? Earlier draft notes in [SKILLS.md](SKILLS.md) hedged with
"a per-chat keychain if we ever add one", which is vague and would invite
secrets sneaking into committed config files.

**Decision.** Hard rule, applied uniformly:

1. **Source of truth = process environment.** `os.Getenv` is the only API
   that returns secret values. picoclaw itself never opens, parses, or
   writes a file containing secret values.
2. **`.env` is allowed only as a developer convenience.** It is in
   `.gitignore`, loaded by `godotenv` at process start to populate
   `os.Environ()`, and from that point on it does not exist as far as the
   rest of picoclaw is concerned. Production deployments use systemd
   `EnvironmentFile=` or launchd `EnvironmentVariables`.
3. **No secrets in any committed file.** Not in `chats/<folder>/skills.json`,
   not in `skills/mcp/<name>/manifest.json`, not in `groups/<name>/CLAUDE.md`,
   not anywhere under version control. The MCP `manifest.json` `env`
   field uses `${VAR}` placeholders that are substituted from
   `os.Environ()` at spawn time — placeholders only, never values.
4. **Per-chat allow-lists are name-only.** `chats/<folder>/secrets.allow`
   (when introduced) is a list of *environment variable names* a non-owner
   chat is permitted to receive. Owner chat receives the full set
   unconditionally. There is no "per-chat secret store".
5. **Logging redaction.** The slog handler in `internal/log` carries an
   allow-list of attribute keys; everything else with a name matching
   `(?i)token|key|secret|password|cookie|auth` is redacted to `<redacted>`
   on the way to both stderr and the `logs` SQLite table.

**Consequences.** One source of truth, no syncing problem, no on-disk
secret material to leak via backup tools or `git status`. Operators who
want OS keychain integration use a wrapper that exports env vars before
launching picoclaw — picoclaw itself stays minimal. Per-chat credential
isolation is coarse (allow-list of variable names) and that is acceptable
for v0; revisit only if a real use case demands per-chat *values*.

---

## Template for new ADRs

```
## D### — <short imperative title>

**Date:** YYYY-MM-DD
**Status:** Proposed | Accepted | Superseded by D###
**Supersedes:** D### (optional)

**Context.** Why are we deciding this now? What forces are at play?

**Decision.** What we are doing. Imperative voice.

**Consequences.** What changes as a result. What becomes easier, what becomes harder.
```
