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

## D013 — Container isolation policy: blocked patterns + RO root + dropped caps

**Date:** 2026-04-09
**Status:** Accepted
**Refines:** [D003](#d003--container-per-chat-long-lived-exec-per-turn), [D006](#d006--owner-chat-replaces-main-group-privilege)

**Context.** [D003](#d003--container-per-chat-long-lived-exec-per-turn) decided
on container-per-chat with `docker exec` per turn but did not pin down the
*security posture* of those containers. NanoClaw has two pieces of prior art —
`src/mount-security.ts` (allowlist + blocked patterns + symlink resolution) and
`src/container-runner.ts` (the actual `docker run` flag set) — and we need a
single picoclaw-shaped policy that survives the agent doing `cat ~/.ssh/id_rsa`,
the agent following a symlink out of its workspace, and a previous picoclaw
process being killed mid-run leaving a container behind.

A separate hedge in [ARCHITECTURE.md §3](ARCHITECTURE.md) said "compile-time
path policy + a small `mount.json` next to binary". That was a placeholder; we
now need to commit.

**Decision.** Three-tier mount model (owner / registered / unregistered),
single allowlist file at `${PICOCLAW_DATA_DIR}/mount-allowlist.json` (chmod
0600, refused to start if wider), compiled-in blocked-pattern list that even
the owner cannot override, and a fixed Docker spawn flag set: `--read-only`,
`--cap-drop=ALL`, `--security-opt=no-new-privileges:true`, `seccomp=default`,
`--user=<uid>:<gid>` (never root), `--pids-limit=1024`, RAM/CPU caps, three
small `tmpfs` mounts for `/tmp`, `/home/node/.cache`, `/run`. The full policy
including the blocked-pattern list, validation rules, lifecycle (boot
cleanup, idle kill, crash recovery, version-mismatch eviction), and module
layout lives in [ISOLATION.md](ISOLATION.md).

`internal/runner/mountsec` is its own subpackage so the security-critical
allowlist validator can be tested in complete isolation from the Docker SDK,
with `t.TempDir` + `os.Symlink` based fixtures.

**Consequences.** A single, audited mount construction path. `.env`, `~/.ssh`,
`~/.aws`, `~/.gnupg`, `~/.docker`, `~/.config/picoclaw`, `id_rsa*`,
`credentials*`, `/etc/shadow`, `/proc`, `/sys`, `/dev` are unmountable for
**any** chat including the owner. Symlinks are resolved before pattern matching
so a symlink-to-secrets attack fails. The agent never runs as root inside the
container, which keeps bind-mount writes owned by the host user and survives
the next backup. Adding back a Linux capability (e.g. `SYS_PTRACE` for
profiling) requires a new ADR, not a code patch. Supersedes the
ARCHITECTURE.md placeholder of "`mount.json` next to binary".

---

## D014 — Observability: opt-in Prometheus + OTel, logs always on

**Date:** 2026-04-09
**Status:** Accepted

**Context.** picoclaw needs visibility into agent latency, container churn,
queue depth, memory operations, and LLM token usage so the operator can spot
when an agent is stuck or burning tokens. Three signals are on the table:
logs, metrics, traces. The constraint pulling against "instrument everything"
is that picoclaw is a *personal* binary on a *personal* machine — fresh
installs must not open ports the operator did not ask for or send telemetry
anywhere unexpected.

**Decision.** Three signals, three postures:

1. **Logs — always on.** `log/slog` with the `internal/log` SQLite handler
   (already covered by [CONTROL.md §9](CONTROL.md) / M3.5 step C5). stderr +
   `logs` table, with the [D012](#d012--secrets-in-environment-variables-only-never-in-config-files)
   redaction allowlist applied on the way in.
2. **Metrics — opt-in via `PICOCLAW_METRICS_ADDR`.** When unset (default), no
   listener is started. When set (e.g. `127.0.0.1:9090`), a tiny dedicated
   `http.Server` exposes `/metrics` (Prometheus exposition via
   `prometheus/client_golang`) and `/healthz`. No other handlers, no auth —
   bind to loopback or put a reverse proxy in front. The control plane is
   **not** on this listener (per [D011](#d011--unified-command-gateway-internalcontrol)
   the control plane has no HTTP frontend in v0).
3. **Traces — opt-in via `PICOCLAW_OTLP_ENDPOINT`.** When unset, the global
   tracer provider is the OTel no-op implementation; every `tracer.Start` is
   free, every span attribute write is dropped. When set, an OTLP exporter
   (gRPC or HTTP, auto-derived from URL scheme) batches spans to the
   configured collector. W3C `traceparent` is propagated into the agent
   container as an env var on every `docker exec`, so future Claude Code
   versions or MCP skill processes can join the trace.

Cardinality is bounded by labelling only on closed sets (`provider`, `model`,
`tool`, `skill`, `kind`, `result`) plus `chat`; an escape hatch
`PICOCLAW_METRICS_DROP_CHAT_LABEL=1` exists for users with many chats. Span
attribute setters go through `obs.SetAttr`, which applies the same
`(?i)token|key|secret|password|cookie|auth` redaction matcher as the slog
handler.

`internal/observability` is the only package new code touches to add a
metric or a span; `cmd/picoclaw/main.go` calls `Init` once and gets back a
single `shutdown(ctx)` that flushes both subsystems with a 5-second grace
period. Slotted into [ROADMAP.md](../ROADMAP.md) as **M3.6** with sub-steps
**O1–O6** (see [OBSERVABILITY.md §6](OBSERVABILITY.md)).

**Consequences.** Default install opens zero ports beyond Telegram long-poll
outbound. Operators who want a Grafana stack flip one env var; operators who
want Honeycomb flip two. Adding metrics/spans to a new package is a local
change in that package — no central "log this thing" indirection. The OTel
no-op default means picoclaw cannot accidentally hard-depend on a collector
being reachable. Refines [D012](#d012--secrets-in-environment-variables-only-never-in-config-files):
the one observability env var that may carry a secret value
(`PICOCLAW_OTLP_HEADERS`) is read into memory at process start, used to
construct exporter headers, and runs through the same redaction matcher if
ever logged.

---

## D015 — Claude Agent SDK custom command via wrapper script

**Date:** 2026-04-09
**Status:** Accepted
**Refines:** [D003](#d003--container-per-chat-long-lived-exec-per-turn)

**Context.** [D003](#d003--container-per-chat-long-lived-exec-per-turn) chose
container-per-chat with `docker exec` per turn, talked to via the Go
`character-ai/claude-agent-sdk-go` `Client`. That left an open question in
HANDOFF.md: does the SDK actually let us override what binary it spawns so we
can point it at `docker exec -i <container> claude` instead of a host
`claude`? If not, the only alternative is to embed `claude` directly in the
host process, which loses the per-chat container isolation D003 is built on.

**Decision.** Verified against the SDK source: it exposes
`Options.CLIPath string` (defaults to `"claude"`) which is consumed by
`(*Client).runStreaming` as `exec.CommandContext(ctx, cliPath, args...)`.
This is **single argv[0]** — there is no `Command []string` / `CLIPrefixArgs`
option, and `Options.ExtraArgs` appends *after* the SDK args (so it goes to
Claude, not to docker). `exec.LookPath` runs on the raw `cliPath`, so a
multi-word string doesn't work either.

picoclaw uses a tiny per-chat wrapper script as `CLIPath`:

```sh
#!/bin/sh
# data/wrappers/<chat>.sh — generated at chat-folder creation
exec docker exec -i \
  -e ANTHROPIC_API_KEY \
  -e OTHER_VAR_FROM_ALLOWLIST \
  "picoclaw-<chat>" \
  claude "$@"
```

`internal/runner` generates one of these per registered chat (idempotent,
chmod 0755), and points the SDK Client at it:

```go
client := claude.NewClient(claude.Options{
    CLIPath:        wrapperPath,            // /path/to/data/wrappers/<chat>.sh
    PermissionMode: claude.PermissionAcceptEdits,
    Cwd:            "/workspace/chat",      // interpreted INSIDE the container
    // ...
})
```

`PICOCLAW_NO_CONTAINER=1` (dev fallback) sets `CLIPath = "claude"` to talk to
host-installed Claude directly.

**Consequences.** D003 holds — container isolation is preserved, no host
embedding of `claude`. The wrapper file is the per-chat trust boundary in
addition to the mount allowlist: it bakes in the `-e VAR` set the chat is
allowed to receive (per [D012](#d012--secrets-in-environment-variables-only-never-in-config-files)
allow-list), and a chat with no entries gets none. `Options.Cwd` is sent to
Claude as a `--cwd` *flag* interpreted inside the container — host cwd
doesn't matter, the path must exist in the container (i.e. the bind-mounted
chat folder). `Close()` sends SIGINT to the wrapper which propagates to
`docker exec`; because Docker's signal forwarding is historically flaky, the
runner additionally calls `docker exec <name> pkill -INT claude` on close as
a belt-and-suspenders measure. Stderr from the wrapper carries both Claude's
own errors and Docker's "Error response from daemon" lines; the runner
classifies them by prefix matching before logging. Upstream issue/PR for
first-class `Command []string` support: none filed; if it ever lands we
revisit and possibly retire the wrapper script.

---

## D016 — Default embedding model: OpenAI `text-embedding-3-small` at 1024 dim

**Date:** 2026-04-09
**Status:** Accepted

**Context.** [MEMORY.md §6](MEMORY.md) sketches three embedding providers
(OpenAI, Voyage, Ollama) and proposes OpenAI `text-embedding-3-small`
truncated from its native 1536 to 1024 dimensions as the default. HANDOFF.md
flagged this as an open question pending verification of the OpenAI
`dimensions` API parameter and its retrieval-quality impact, because changing
the dimension after the fact is a re-embed pass and a `sqlite-vec` schema
churn.

**Decision.** Default embedder = OpenAI `text-embedding-3-small` with
`dimensions=1024`, server-side. Verified against the OpenAI announcement
post, the embeddings guide, the API reference, and the dev-forum empirical
analysis:

1. The `dimensions` parameter is officially supported on `text-embedding-3-small`
   and `text-embedding-3-large` (but not on `text-embedding-ada-002`, which is
   fixed at 1536).
2. OpenAI returns **unit-length** vectors regardless of `dimensions`, i.e.
   server-side `dimensions=N` performs `truncate → L2-renormalize`. Cosine
   distance and inner product give identical rankings.
3. Both `-3-small` and `-3-large` are trained with Matryoshka Representation
   Learning, so earlier dimensions carry more signal. OpenAI's own claim:
   `-3-large` shortened to 256 still beats `ada-002` at 1536. The drop from
   1536 → 1024 on `-3-small` is well under one MTEB point — not measurable
   for a personal-assistant memory layer.
4. `sqlite-vec`'s `vec0` virtual table requires a fixed dimension per column,
   so picking one and committing matters. 1024 is ~33% smaller on disk than
   1536 (4 KiB vs 6 KiB per vector at float32) with no meaningful retrieval
   loss.

The picoclaw `memories` table stores `(model, dim)` next to every vector so
a future migration to e.g. `-3-large` at 1024 or 1536 is a clean re-embed
pass rather than a silent corruption: the runner refuses to mix vectors from
different `(model, dim)` tuples in the same query.

**Consequences.** `PICOCLAW_EMBEDDING_MODEL=text-embedding-3-small` and
`PICOCLAW_EMBEDDING_DIM=1024` are baked as the defaults; the schema in
[MEMORY.md §3](MEMORY.md) (`embedding float[1024]`) is now load-bearing.
Operators can switch providers via `PICOCLAW_EMBEDDING_PROVIDER` but doing
so on an existing store requires a re-embed pass. Client-side truncation is
**banned** — if the runner ever needs a smaller vector it must request it
from the API with a new `dimensions` value; truncating a stored vector
locally would skip the L2-renormalization and silently degrade recall.

---

## D017 — Pin `ncruces/go-sqlite3` to v0.20.0 for sqlite-vec compatibility

**Date:** 2026-04-09
**Status:** Accepted
**Refines:** [D004](#d004--sqlite-driver-ncrucesgo-sqlite3-wasm-pure-go)

**Context.** [D004](#d004--sqlite-driver-ncrucesgo-sqlite3-wasm-pure-go)
chose `ncruces/go-sqlite3` paired with the `asg017/sqlite-vec-go-bindings`
ncruces variant. M0 implementation surfaced a real compatibility wrinkle:

1. The asg017 binding works by setting `sqlite3.Binary = wasmBinary` in its
   `init()`. The pre-built `sqlite3.wasm` it ships uses the WebAssembly
   threads/atomics feature (`i32.atomic.store`).
2. Starting at `ncruces/go-sqlite3 v0.21.0` and continuing through v0.32.x,
   the embedded wazero runtime instantiation does not enable threads, so
   the binding's wasm fails to load with
   `i32.atomic.store invalid as feature "" is disabled`.
3. At `ncruces/go-sqlite3 v0.33.0` the entire `Binary` variable and the
   `embed` subpackage were removed in favor of a compile-time-bundled
   wasm via `github.com/ncruces/go-sqlite3-wasm`, breaking the binding's
   `init()` at compile time.
4. The asg017 binding has not been updated to follow either change
   (latest tag `v0.1.7-alpha.2` still references `sqlite3.Binary` and
   ships the same atomics-using wasm).

A bisect against asg017 v0.1.6 found exactly two ncruces versions that work:
**v0.19.0** and **v0.20.0**. v0.18.0 has a separate `go_busy_timeout` ABI
mismatch. v0.17.1 and v0.21.0+ all hit the atomics error.

**Decision.** Pin `github.com/ncruces/go-sqlite3` to **v0.20.0** in `go.mod`
and use the asg017 ncruces binding for side-effect import in
`internal/store/store.go`. Do **not** also import
`github.com/ncruces/go-sqlite3/embed` — the asg017 init() already populates
`sqlite3.Binary`, and the two would race.

**Consequences.** picoclaw runs on a year-old SQLite WASM build until either
(a) asg017 publishes a binding compatible with current `ncruces/go-sqlite3`,
or (b) we vendor our own sqlite-vec-bundled wasm. The pin is invisible to
consumers — `sql.Open("sqlite3", …)` still works exactly as documented.
Verified at runtime: M0 binary opens the store, queries `vec_version()`
which returns `v0.1.6`, creates the `vec0` virtual table, and idempotently
re-applies the schema on restart.

When upgrading: re-run the bisect, update this ADR with a new pinned
version, and re-test `internal/store/store_test.go` end-to-end (it covers
both `vec_version()` and a real KNN query against `memory_vec`).

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
