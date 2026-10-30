# gopod — Architectural Decision Log

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

**Decision.** gopod supports exactly one channel — Telegram — wired
statically into `internal/telegram`. No registry, no abstraction over
messaging providers, no JID prefix system.

**Consequences.** Trivial code paths in `cmd/gopod` and `internal/runner`.
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
`gopod.chat=<folder>`). Each message triggers a fresh `docker exec` of
`claude` against the running container. Idle-killed after
`GOPOD_IDLE_TIMEOUT` (default 30 min). On startup, leftover containers are
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

**Context.** gopod needs vector search for the long-term memory layer.
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

**Decision.** gopod uses a single `GOPOD_OWNER_CHAT_ID` env var. The
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

**Decision.** gopod injects `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, etc.
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

**Decision.** gopod ships a smaller, idiomatic-for-Go skill ecosystem:

1. **Container skills** — drop a directory into `container/skills/<name>/`
   containing a `SKILL.md` plus optional scripts. The directory is mounted
   into the agent container at `/home/node/.claude/skills/<name>/` exactly
   like NanoClaw's container skills. Zero Go code, zero rebuild.
2. **MCP skills** — drop a directory into `skills/mcp/<name>/` containing
   `manifest.json` (command, args, env, scope). gopod spawns the MCP
   server (or HTTP-connects to it) per chat at agent startup and registers
   its tools with the agent SDK. Adds new agent capabilities without
   recompiling gopod.
3. **Dev-time skills** — `.claude/skills/<name>/` with Claude Code slash
   commands for gopod *maintainers* (e.g. `/release`, `/migrate-schema`).
   Not loaded at runtime.

NanoClaw's "feature skills" (which add Go code) become **regular Go packages**
in `internal/`, conditionally enabled by env vars. They are not skills.

Full design in [SKILLS.md](SKILLS.md).

**Consequences.** gopod has an extension story without inheriting
NanoClaw's branch-merge complexity. The line between "skill" and "core
feature" is sharp: anything that requires recompiling gopod is a feature,
anything that doesn't is a skill.

---

## D010 — Drop macOS status-bar tray

**Date:** 2026-04-09
**Status:** Accepted

**Context.** NanoClaw has a macOS menubar tray skill. We considered porting
it via `getlantern/systray` as a separate `cmd/gopod-tray` binary.

**Decision.** Skip. User explicitly does not want it.

**Consequences.** No CGO ever needed in any gopod binary. Service
management stays CLI-only (launchd plist on macOS, systemd unit on Linux).

---

## D011 — Unified command gateway (`internal/control`)

**Date:** 2026-04-09
**Status:** Accepted

**Context.** gopod has a growing set of admin operations: scheduled task
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
frontends (web UI, MCP-server-mode gopod, etc.) plug in as new
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
   that returns secret values. gopod itself never opens, parses, or
   writes a file containing secret values.
2. **`.env` is allowed only as a developer convenience.** It is in
   `.gitignore`, loaded by `godotenv` at process start to populate
   `os.Environ()`, and from that point on it does not exist as far as the
   rest of gopod is concerned. Production deployments use systemd
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
launching gopod — gopod itself stays minimal. Per-chat credential
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
single gopod-shaped policy that survives the agent doing `cat ~/.ssh/id_rsa`,
the agent following a symlink out of its workspace, and a previous gopod
process being killed mid-run leaving a container behind.

A separate hedge in [ARCHITECTURE.md §3](ARCHITECTURE.md) said "compile-time
path policy + a small `mount.json` next to binary". That was a placeholder; we
now need to commit.

**Decision.** Three-tier mount model (owner / registered / unregistered),
single allowlist file at `${GOPOD_DATA_DIR}/mount-allowlist.json` (chmod
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
`~/.aws`, `~/.gnupg`, `~/.docker`, `~/.config/gopod`, `id_rsa*`,
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

**Context.** gopod needs visibility into agent latency, container churn,
queue depth, memory operations, and LLM token usage so the operator can spot
when an agent is stuck or burning tokens. Three signals are on the table:
logs, metrics, traces. The constraint pulling against "instrument everything"
is that gopod is a *personal* binary on a *personal* machine — fresh
installs must not open ports the operator did not ask for or send telemetry
anywhere unexpected.

**Decision.** Three signals, three postures:

1. **Logs — always on.** `log/slog` with the `internal/log` SQLite handler
   (already covered by [CONTROL.md §9](CONTROL.md) / M3.5 step C5). stderr +
   `logs` table, with the [D012](#d012--secrets-in-environment-variables-only-never-in-config-files)
   redaction allowlist applied on the way in.
2. **Metrics — opt-in via `GOPOD_METRICS_ADDR`.** When unset (default), no
   listener is started. When set (e.g. `127.0.0.1:9090`), a tiny dedicated
   `http.Server` exposes `/metrics` (Prometheus exposition via
   `prometheus/client_golang`) and `/healthz`. No other handlers, no auth —
   bind to loopback or put a reverse proxy in front. The control plane is
   **not** on this listener (per [D011](#d011--unified-command-gateway-internalcontrol)
   the control plane has no HTTP frontend in v0).
3. **Traces — opt-in via `GOPOD_OTLP_ENDPOINT`.** When unset, the global
   tracer provider is the OTel no-op implementation; every `tracer.Start` is
   free, every span attribute write is dropped. When set, an OTLP exporter
   (gRPC or HTTP, auto-derived from URL scheme) batches spans to the
   configured collector. W3C `traceparent` is propagated into the agent
   container as an env var on every `docker exec`, so future Claude Code
   versions or MCP skill processes can join the trace.

Cardinality is bounded by labelling only on closed sets (`provider`, `model`,
`tool`, `skill`, `kind`, `result`) plus `chat`; an escape hatch
`GOPOD_METRICS_DROP_CHAT_LABEL=1` exists for users with many chats. Span
attribute setters go through `obs.SetAttr`, which applies the same
`(?i)token|key|secret|password|cookie|auth` redaction matcher as the slog
handler.

`internal/observability` is the only package new code touches to add a
metric or a span; `cmd/gopod/main.go` calls `Init` once and gets back a
single `shutdown(ctx)` that flushes both subsystems with a 5-second grace
period. Slotted into [ROADMAP.md](../ROADMAP.md) as **M3.6** with sub-steps
**O1–O6** (see [OBSERVABILITY.md §6](OBSERVABILITY.md)).

**Consequences.** Default install opens zero ports beyond Telegram long-poll
outbound. Operators who want a Grafana stack flip one env var; operators who
want Honeycomb flip two. Adding metrics/spans to a new package is a local
change in that package — no central "log this thing" indirection. The OTel
no-op default means gopod cannot accidentally hard-depend on a collector
being reachable. Refines [D012](#d012--secrets-in-environment-variables-only-never-in-config-files):
the one observability env var that may carry a secret value
(`GOPOD_OTLP_HEADERS`) is read into memory at process start, used to
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

gopod uses a tiny per-chat wrapper script as `CLIPath`:

```sh
#!/bin/sh
# data/wrappers/<chat>.sh — generated at chat-folder creation
exec docker exec -i \
  -e ANTHROPIC_API_KEY \
  -e OTHER_VAR_FROM_ALLOWLIST \
  "gopod-<chat>" \
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

`GOPOD_NO_CONTAINER=1` (dev fallback) sets `CLIPath = "claude"` to talk to
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

The gopod `memories` table stores `(model, dim)` next to every vector so
a future migration to e.g. `-3-large` at 1024 or 1536 is a clean re-embed
pass rather than a silent corruption: the runner refuses to mix vectors from
different `(model, dim)` tuples in the same query.

**Consequences.** `GOPOD_EMBEDDING_MODEL=text-embedding-3-small` and
`GOPOD_EMBEDDING_DIM=1024` are baked as the defaults; the schema in
[MEMORY.md §3](MEMORY.md) (`embedding float[1024]`) is now load-bearing.
Operators can switch providers via `GOPOD_EMBEDDING_PROVIDER` but doing
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

**Consequences.** gopod runs on a year-old SQLite WASM build until either
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

## D018 — Telegram-mediated web-auth onboarding; drop M2 from critical path

**Date:** 2026-04-09
**Status:** Accepted
**Refines:** [D002](#d002--go-no-typescript-in-repo), [D003](#d003--container-per-chat-long-lived-exec-per-turn), [D008](#d008--no-onecli-secrets-via-env-vars)

**Context.** gopod needs Anthropic credentials. There are two surfaces:

1. **API key** (`ANTHROPIC_API_KEY`) — issued via console.anthropic.com,
   pay-per-token. Used by [Path A](ARCHITECTURE.md) (`anthropic-sdk-go` direct
   API calls), originally slotted as M2 in the roadmap.
2. **Pro/Max session** (`claude /login`) — OAuth/session via claude.ai, billed
   against the operator's Anthropic subscription. Used by Claude Code CLI,
   which is what [Path B](ARCHITECTURE.md) (M5+M6) talks to via `docker exec`.

The original roadmap (M2 → M3 → M3.5 → M3.6 → M4 → M5 → M6) put the API-key
path first. Two problems showed up while planning M2:

- **API key is a meaningful adoption tax.** Pro/Max subscribers should not
  have to provision a separate billing surface to use gopod.
- **Anthropic does not publish an OAuth client API for third parties.**
  gopod cannot legitimately implement the Pro/Max login flow itself —
  the endpoints are an implementation detail of Claude Code, the contract
  is unstable, and reverse-engineering is fragile and ToS-grey. Whatever
  gopod does about web-auth has to **proxy Claude Code's own login
  flow**, not replicate it.

There is, however, a clean primitive available: `claude /login` running
**inside the agent container**, with credentials persisted into the
already-mounted `data/sessions/<chat>/.claude/` directory ([ISOLATION.md §3](ISOLATION.md)).
A `/login` slash command in Telegram can spawn that flow inside the
container, intercept the verification URL Claude prints to stdout, forward
it to the user via the bot, wait for `claude` to report success on stdout,
and never touch a credential value itself. The trust boundary stays at the
container; gopod stays a thin orchestrator.

**Decision.** Three coupled changes:

1. **Drop M2 (Direct API agent) from the critical path.** It is downgraded
   to **optional**: if `ANTHROPIC_API_KEY` is set in the environment, the
   direct-API path is available as an opt-in mode for cheap-chat use cases.
   If unset (the new default), gopod simply doesn't expose Path A and
   nothing fails. M2 status changes from ⬜ to ⏸️ in the roadmap.

2. **Promote M5 + M6 to the critical path** as the first agent
   implementation. The new build order is:

   `M0 ✅ → M1 ✅ → M5 → M6 → M6.5 → M3 → M3.5 → M3.6 → M4 → M7 → M8 → M9`

   The numerical ID order in [ROADMAP.md](../ROADMAP.md) stays the same
   (IDs are stable identifiers cited from many places); the **build order**
   is documented as a separate callout at the top of the file.

3. **Add M6.5: Telegram-mediated `/login`.** A new milestone that wires the
   `/login` slash command to a small interactive proxy:

   - User sends `/login` in their gopod chat.
   - Bot ensures the agent container for that chat is running (boots one
     per M5 if needed).
   - Bot runs `docker exec -i <name> claude /login` and reads the CLI's
     stdout line by line.
   - When `claude` prints the verification URL, the bot forwards it to the
     user as a Telegram message.
   - When `claude` prints the success line, the bot forwards "logged in
     as <email>" (or whatever Claude prints) and exits the proxy.
   - Credentials live in `/home/node/.claude/` inside the container, which
     is bind-mounted from `data/sessions/<chat>/.claude/` on the host
     ([ISOLATION.md §3](ISOLATION.md)). gopod never reads, parses, or
     transmits the credential bytes.

   M6.5 has no host-side OAuth code, no callback listener, no public URL
   requirement, and depends only on `claude /login` printing a stable
   URL+success contract on stdout. If that contract changes upstream, M6.5
   needs a small parser bump but no architectural rework.

**Consequences.**

- **Faster time to first real agent reply.** Pro/Max users get a working
  gopod the moment M6.5 lands; no API-key provisioning step.
- **CLI subcommands stay deferred.** The earlier ask "do we need a CLI
  frontend for onboarding before M3.5?" answers no — onboarding goes
  through Telegram. The CLI continues to slot in at M3.5 alongside the
  Telegram frontend, both behind the same `internal/control` Router.
- **gopod never holds Pro/Max credentials in process memory.** They
  live exclusively in the container-mounted directory and Claude Code
  manages their lifecycle. This is strictly stronger than the API-key
  path under [D012](#d012--secrets-in-environment-variables-only-never-in-config-files):
  there is no env var, no file gopod reads, no log line that could
  ever leak them.
- **M3 (GroupQueue), M3.5 (Control plane), M3.6 (Observability) ship
  later than originally planned.** Tradeoffs:
  - Per-chat serialization that M3 provides becomes a stub `sync.Mutex`
    map keyed by chat folder when M5+M6 land. Replaced by GroupQueue at
    M3 for backoff and the global concurrency cap.
  - The `/login` and `/ping` slash commands land in M1/M6.5 wired
    directly via go-telegram/bot's `RegisterHandler`, not through the
    not-yet-existing `internal/control` Router. M3.5 then folds them
    into the Router as part of its own scope. Acceptable churn — both
    are tiny handlers.
  - Logs (`internal/log` SQLite handler, M3.5/C5) ship later. Until
    then `slog` writes to stderr only. The `logs` table exists from M0
    but stays empty.
  - Observability metrics/spans land later. M5/M6 code emits no
    spans/metrics initially; they get added in M3.6 by editing the
    relevant packages, not by changing their public API.
- **M7 (IPC), M8 (recovery), M9 (memory) keep their relative order at
  the tail of the build.**
- **D015 (claude SDK custom command via wrapper) is unaffected.** M6
  still uses the per-chat wrapper script for the agent loop. M6.5 talks
  to `claude /login` directly via `docker exec` since the SDK isn't in
  the picture for that one-shot interactive command.

**Open questions for M6.5 (track in HANDOFF):**

- Exact stdout contract of `claude /login` (URL line format, success line
  format, error format). Verify against the installed `claude` version
  before implementing M6.5.
- What if the user starts `/login` and never completes it? Pick a
  timeout (10 min default), kill the `docker exec` on timeout, send the
  user a "session expired, try /login again" message.
- Per-chat vs owner-only login: each chat has its own session dir, so in
  principle each chat could have its own Pro/Max account. v0 should
  probably restrict `/login` to the owner chat and reuse the owner's
  credential dir for non-owner chats; revisit if a real use case wants
  per-chat accounts.

---

## D019 — Secret gateway: MITM proxy + HashiCorp Vault replaces env var injection

**Date:** 2026-04-13
**Status:** Accepted
**Supersedes:** [D008](#d008--no-onecli-secrets-via-env-vars) (partial — env var
fallback preserved as backwards-compatible mode)
**Refines:** [D012](#d012--secrets-in-environment-variables-only-never-in-config-files)

**Context.** [D008](#d008--no-onecli-secrets-via-env-vars) decided to drop OneCLI
and pass API keys as container environment variables. This was an acceptable
trade-off for v0 personal use. Now that the multi-provider architecture is in
place (P1+P2), containers may need credentials for multiple services
(Anthropic, OpenAI, GitHub, Google, Slack) and the env var approach has real
weaknesses: secrets are visible to any process inside the container via
`/proc/1/environ`, there is no per-service scoping, no rotation without
restart, and no audit trail. OneCLI
(an earlier, unpublished project) demonstrated that an HTTP
CONNECT proxy with MITM TLS interception solves all of these. gopod adopts
the same architecture, reimplemented in Go with HashiCorp Vault as the secret
backend instead of PostgreSQL+Bitwarden.

**Decision.** Add an embedded HTTP CONNECT proxy (`internal/gateway`) that:

1. Listens on `GOPOD_GATEWAY_ADDR` (default `127.0.0.1:10255`).
2. Authenticates agent containers via a per-chat `agent_token` in the
   Proxy-Authorization header.
3. Resolves injection rules from HashiCorp Vault (KV v2): per-chat secrets
   shadow shared secrets, matched by host+path pattern.
4. For hosts with injection rules: MITM-terminates TLS (self-signed CA
   trusted by the container), injects credentials into request headers,
   forwards upstream.
5. For hosts without rules: plain TCP tunnel (no interception).
6. Containers receive only `GOPOD_AGENT_TOKEN` + `HTTP_PROXY`/`HTTPS_PROXY`
   + the CA certificate. No real API keys in the container environment.

Three deployment modes: full Vault (recommended), SQLite + Vault Transit
(lightweight), SQLite + local AES key (dev-only). Env var passthrough
(D008 behavior) remains as backwards-compatible fallback when no gateway is
configured.

Full design in [GATEWAY.md](GATEWAY.md).

**Consequences.**
- Agents never see real credentials — strongest isolation short of hardware
  enclaves.
- Per-service scoping: an agent calling `api.anthropic.com` cannot
  exfiltrate the GitHub token.
- Key rotation is instant (Vault update + cache flush, no container restart).
- Audit log in `gateway_log` table.
- New external dependency: HashiCorp Vault (can be a dev server in Docker,
  HCP Cloud, or any Vault deployment).
- MITM adds latency (~1–5ms per request for cert generation + TLS
  re-termination). Acceptable for LLM API calls that take seconds.
- D012's "source of truth = process environment" still applies to
  *gopod itself* (Vault token, gateway config). The change is that agent
  containers no longer receive secret env vars — the gateway is their
  source of truth.

---

## D020 — Durable turns: the queue persists every agent run

**Date:** 2026-10-03
**Status:** Accepted
**Refines:** M3 GroupQueue design (ROADMAP.md, no ADR)

**Context.** The GroupQueue mirrored NanoClaw: in memory, workers on
`context.Background()`. A crash mid-turn lost the user's message, the
pending backlog, and left the streaming placeholder dangling. The
scheduler logged `success` at enqueue time, so a crash could mark a
task done that never ran. Pi Durable's checkpoint model (every
submission is a row before it runs; idempotent request ids; the model
is told when its turn was interrupted) fits gopod's single-SQLite rule.

**Decision.** `internal/queue` owns durability through a consumer-side
`TurnStore` interface backed by a `turns` table. `Enqueue` inserts
first; workers mark `running`/`done`/`failed`/`interrupted`; `Recover`
replays unfinished rows at boot with `Resumed=true`. The Telegram
chat id + message id (`"<chat_id>:<message_id>"`) is the request id. Resumed turns are not re-run blindly and
not dropped: the session continues with an interruption notice and the
agent decides what to redo, because gopod does not own the tool set and
cannot classify side effects. Placeholder and reply message ids are
memoized on the row (first write wins) so recovery never double-replies.
The scheduler finalizes `task_run_logs` through a queue completion hook;
its idempotency key is the task's scheduled slot (`next_run`), used both
as `task_run_logs.run_at` and in `SourceID = "<task_id>:<next_run>"`, and a
duplicate slot still advances `next_run`. Max attempts across restarts is
a constant, 3. When a batch exhausts its in-process retries the queue keeps
the pending items (in memory and as `pending` rows) rather than dropping
them; the next `Enqueue` for that chat, or `Recover`, picks them up.
`Queue.Close` refuses new work and marks in-flight batches `interrupted`;
items still waiting on the concurrency semaphore stay `pending` and fire
no completion hook.

**Rejected.** DB-as-queue polling (rewrites M3, adds latency, needs row
locking); per-producer outboxes (recovery logic in two places, scheduler
still blind to completion); conversation forks, multiplayer and tool
replay flags from Pi (no second user, no owned tools).

**Consequences.** `queue.New` takes the app context and a store; the
`GOPOD_NO_CONTAINER` dev path passes nil and keeps memory-only
behaviour. Outbound bot replies are now persisted to `messages`. First
schema migration via the existing `migrations` list
(`task_run_logs.turn_id`). Known gaps: voice-only reply mode records no
reply id, so a crash right after a voice-only reply replays the turn once;
`Queue.Close` has no timeout, so a handler that ignores its context can
hang shutdown. Spec:
[superpowers/specs/2026-10-03-durable-turns-design.md](superpowers/specs/2026-10-03-durable-turns-design.md).

---

## D021 — Stop and native streaming

**Date:** 2026-10-24
**Status:** Accepted
**Extends:** D020 (durable turns)

**Context.** Bot API 10.3 adds `sendMessageDraft` (an ephemeral, animated preview in private chats) with a Stop button (`can_stop`, `keep_on_stop`, `Update.stopped_message_generation`). Editing a placeholder message was the only streaming path, and a turn could not be stopped.

**Decision.**
- Private chats stream through `sendMessageDraft`; the final reply is one `sendMessage`, whose id is the turn's reply id (D020 memo). Groups, and any chat where the API refuses drafts (404 or a capability-style 400, or three unrecognized 400s in a row), use the placeholder-and-edit path; the refusal is sticky per chat until restart. Transient errors retry; a 429 holds draft updates until `retry_after`, and the final send retries a 429 within a bounded budget, returning an error (never a silent `done`) if it cannot deliver.
- Draft ids come from a per-process counter seeded from the clock, and an in-flight registry maps (chat, draft id) to the turn. Stop cancels only that turn's agent context, sends the partial text as the reply, and ends the turn `stopped`.
- `stopped` is a terminal turn status: the durable-turns state machine gains `running -> stopped`; `ListUnfinishedTurns`/`Recover` never return or replay it.
- Cancelling a streamed run kills the agent: the command is wrapped to write its pid to a per-run file in the container's `/tmp` (the exec leads its own process group); on cancel a second exec sends SIGTERM to the group, SIGKILL after 3s, and removes the file. A per-chat agent slot is held until the process is gone, so the next turn of the chat waits for the kill. A cancelled run skips memory extraction, the turn count and auto-compact.

**Consequences.** Known gaps (see HANDOFF): not yet verified live (`kill -9` mid-draft, Stop on real claude/codex turns and whether the session stays resumable, draft id accepted as a string); PID 1 is `sleep infinity`, so killed children stay zombies until `HostConfig.Init = true`; tools started with `setsid` escape the group kill; a crash between the final send and `recordReply` on the draft path sends a second reply on resume; `runner.Run`/`RunFresh` do not take the per-chat agent slot; an undelivered final reply makes the queue re-run the whole turn (agent work repeats, the user gets one reply). Spec: [superpowers/specs/2026-10-24-telegram-threads-and-drafts-design.md](superpowers/specs/2026-10-24-telegram-threads-and-drafts-design.md).

## D022 — Conversations: one agent session per (chat, thread)

**Date:** 2026-10-24
**Status:** Accepted
**Extends:** D020 (durable turns), D021 (native streaming)

**Context.** The chat-UI model of Claude and Codex has parallel conversations with separate contexts. gopod kept one agent session per chat, resumed with `claude --continue` / `codex exec resume --last`, which pick the most recently touched session and cannot tell two conversations apart. Bot API 9.3/9.4 lets a private chat have topics (`message_thread_id`).

**Decision.**
- A conversation is `(chat_folder, thread_id)`; messages outside any thread are thread 0, the *main* conversation, with today's behaviour. Threads apply to private chats only (`inboundThreadID`); groups stay thread 0.
- Container, workspace, `CLAUDE.md`, facts/pinned/preferences memory and the provider stay per chat. Per conversation: the agent session, the `conversation_summary` memory slot, turn counters and auto-compact, `/clear` and `/compact`. `/model` and `/provider` stay per chat.
- Sessions are resumed by explicit id stored in `sessions(chat_folder, thread_id, provider)`: Claude `--session-id <uuid>` for a new conversation then `--resume`; Codex plain `exec` for a new conversation with the thread id read from the stderr header (never from the model's reply), then `exec resume <id>`. Ids are validated as UUIDs before they reach a shell. The runner checks the session still exists before resuming and drops a stale row.
- Upgrade: the main conversation adopts the container's latest legacy session once per (chat, provider) before any conversation's first turn, guarded by a marker in `router_state`.
- `turns` and queue items carry `thread_id`; Recover replays into the original thread. Execution stays sequential per chat, and a queue batch only contains consecutive items of one thread, so conversations never mix in one prompt. Every outbound send of a turn, and every command reply, carries `message_thread_id`; the control `Caller` carries the thread (routing only, never authorization). Scheduler and IPC messages stay in main.
- The memory API refuses `kind=conversation_summary` (only session compact writes it); `Codex RunFreshCmd` runs `--ephemeral` so system runs never become a resumed session.

**Consequences.** Parallel execution of threads is *not* built: it is an optional research track (two agents in one container sharing `$HOME` are unverified; fallback is the current sequential queue). Open items: compaction summarises via `RunFresh` without the conversation's history (pre-existing, so `/compact` effectively clears); `/provider` still clears the main session; a `/login` code is accepted from any thread of the chat; `ClearSession` during a Codex first turn is a no-op; closed or deleted topics are never cleaned up. Requires topics ("Threaded mode") enabled for the bot in @BotFather. Spec: [superpowers/specs/2026-10-24-telegram-threads-and-drafts-design.md](superpowers/specs/2026-10-24-telegram-threads-and-drafts-design.md).

## D023 — Extracted memories are scoped per conversation

**Date:** 2026-10-25
**Status:** Accepted
**Narrows:** D022 ("facts/pinned/preferences memory stay per chat") and the same line of the threads spec

**Context.** After D022 only `conversation_summary` rows were per conversation. Facts, decisions, preferences and hypotheses extracted after every turn were stored chat-wide (thread 0) and the context compiler injected them into every conversation of the chat, so what was said in one thread showed up in a sibling thread.

**Decision.**
- Manual and pinned memories stay shared: `Memory.Add`, the agent's `/memory/add` (whatever thread the agent runs in), the remember commands and other manual writes keep storing thread 0, and thread 0 rows (pinned or not) are visible to every conversation of the chat. Memories the agent or owner saves on purpose are meant for the whole chat.
- Extracted memories are per conversation: the runner's extraction callback carries the turn's thread (`Run` and `RunStream`), and `IngestExtracted` stores every extracted kind with that `thread_id`. `AddInThread` keeps the thread for every kind (no coercion to 0).
- Visibility rule for every agent-facing read: conversation T != 0 sees rows with `thread_id IN (0, T)`; main (T = 0) is the chat-wide aggregate and sees every row of the chat; a row born in thread A is never visible to a sibling thread B. It applies to every compiler slot (pinned, recent decisions, semantic preferences and facts), the fallback search (vector and FTS), and the agent's memory HTTP API (`/memory/search`, `/memory/list`).
- The memory API learns the thread from its capability: tokens are minted per agent run for `(chat, thread)` (system runs such as `RunFresh` get thread 0), and the API never takes the thread from request data. `/memory/list` returns active rows only.
- `conversation_summary` is only ever visible to its own conversation: in the compiler, in the reserved slot (never from the fallback); in the API, only the caller's own conversation's summary (main does not see threads' summaries).
- The 0.92-similarity dedup and the extractor's `supersedes_keyword` only match rows of the same `thread_id`, so one conversation never rewrites, re-kinds or retires another's row (a thread fact never overwrites a main row and vice versa).
- Owner-facing reads stay chat-wide: the control memory commands (`/recall` via `SearchText`, `List`) and `ArchiveStale`. The owner never holds a memory capability.

**Consequences.** Rows that existed before this change keep `thread_id = 0` and stay shared; facts already extracted from threads cannot be told apart from main's. Facts learned in a thread reach other threads only through main or a manual/pinned copy. An agent in a thread that saves a memory with `/memory/add` shares it with every conversation of the chat by design.

## D024 — Coding output: output policy and tool progress in the draft

**Date:** 2026-10-25
**Status:** Accepted
**Extends:** D021 (native streaming)

**Context.** Coding turns dumped walls of code into Telegram, and during the work the user saw only a cursor: `claude -p` in text mode prints the final answer at the end. Spec: [superpowers/specs/2026-10-25-coding-output-design.md](superpowers/specs/2026-10-25-coding-output-design.md), parts 1 and 2.

**Decision.**
- `runner.OutputPolicy` (no code walls, 3-6 line work summary, code only on request or when short) is appended after the compiled memory context to every `Run`/`RunStream` system prompt, also when memory is empty; the new-chat `CLAUDE.md` template says the same. A runner constant, because existing chats' `CLAUDE.md` is never overwritten. Codex gets it through the prompt (its `RunCmd` prepends the system prompt). `RunFresh` system tasks do not get it.
- Optional provider capability `StructuredStreamer` (`StructuredCmd`, `ParseStreamLine`). Claude implements it with `--output-format stream-json --verbose --include-partial-messages`; Codex stays on plain text. Streamed turns use it unless `GOPOD_PROGRESS=0`; sync `Run` stays plain.
- `AgentStream.Chunks` carries only the main agent's text deltas; `AgentStream.Progress` (unbuffered, nil for plain runs) carries tool calls (tool name and its command/path/pattern/query/URL, never output). The reply is, by priority, the `result` event's text, the last main-agent assistant text since the last tool call, the text deltas since it, or the last assistant text at all; memory ingestion uses it, and auth classification searches only it and stderr, never tool results or the transcript. Unparsable lines are skipped; if no line parses, stdout is the reply (plain-text fallback). Once stdout proves structured only its last 64 KB is kept.
- Telegram shows the last five steps (one plain-text line each, at most 60 characters, secrets masked by a progress-line redactor built from the D012 words plus a few names and token shapes that only matter there; the shared D012 list is unchanged) until answer text arrives after the latest step; a later step drops that text as narration. Same lines on the edit path's placeholder. Stop with no answer text replies `(stopped)`.

**Consequences.** Fixtures are sanitised recordings from Claude Code 2.1.310; their event kinds were cross-checked against a run in the 2.1.289 agent image, but they are not byte-identical to it, and a live turn is still owed. The draft no longer shows narration text between tool calls. Codex progress (`--json`) is a follow-up.

## D025 — Long code in final replies

**Date:** 2026-10-25
**Status:** Accepted
**Extends:** D021 (native streaming)

**Context.** A code-heavy answer floods a Telegram chat: every line is shown, and the 4000-byte chunker split HTML on newlines without regard to tags, so a `<pre>` cut across two messages was rejected and fell back to plain text. Spec part 3: [superpowers/specs/2026-10-25-coding-output-design.md](superpowers/specs/2026-10-25-coding-output-design.md).

**Decision.**
- Only the agent's final reply is affected (draft-path final, edit-path final, overflow reply, sync path, drain fallback, `finishStopped`, the reply redirected to main from a gone topic); all of them go through `sendFinal`. Command and system replies keep `sendFormatted`/`markdownToTelegramHTML` unchanged.
- A fenced block over 12 lines renders as `<blockquote expandable><pre>…</pre></blockquote>` (`finalReplyHTML`). The Bot API prose is vague on `pre` inside a quote; TDLib's validator, which the Bot API server runs, allows it ("Pre and Code can't be part of other entities, except blockquote"). The plain-text fallback on rejected markup stays.
- A block over 60 lines or 3000 characters is sent as a document first (`sendDocument`, name from the fence language: `snippet.go`, `snippet-2.py`, unknown → `.txt`), and the text carries `📎 snippet.go (N lines)` in its place. A failed upload keeps the block inline (collapsed). Documents honour `message_thread_id` and the topic-gone fallback to main.
- The turn's reply id (D020) is the text message, never a document. The transcript (`recordReply`) stores the agent's original text, code included.
- The HTML chunker (`chunkHTML`) never cuts inside a tag, entity or UTF-8 sequence, and closes elements open at a boundary and reopens them (attributes kept) in the next chunk.
- The voice reply drops fenced blocks before TTS.

**Consequences.** If the text fails after documents went out and the queue retries the turn, the documents are sent again (accepted duplicate). On the edit path, when the final edit is lost and the reply goes to main, the stored transcript has the notes instead of the code. Documents are not memoized on the turn row. Live check pending: that Telegram renders the quoted `pre` collapsed and accepts it in every client.

## D026 — Agent admission control

**Date:** 2026-10-25
**Status:** Accepted
**Extends:** D021 (per-chat agent slot), D022 (conversations)

**Context.** The deploy host (Raspberry Pi 4B, 3.8 GB, shared with other services) has the memory cgroup off, so the 4 GiB container limit is not enforced. RAM is spent by running agents (~0.5-0.6 GB per turn with a tool), not by idle containers. Threads, scheduled tasks and background `RunFresh` work (extraction, compaction, task parsing) make several simultaneous agents likely, and nothing limited them except the queue's per-chat cap.

**Decision.**
- One global gate in `internal/runner` (`gate.go`) that every agent process start passes: `Run` and `RunStream` as *interactive*, `RunFresh` as *background*. At most `GOPOD_MAX_AGENTS` (default 2, at least 1) run at once. Interactive waiters go before background ones, FIFO inside a class; nothing is preempted. Waiters admit themselves from the head of the queue, so a cancelled waiter just leaves and a slot is never handed to a caller that is gone; `release` is idempotent.
- A start also needs host `MemAvailable` (`/proc/meminfo`) >= `GOPOD_MIN_AVAILABLE_MB` (default 900, 0 = off), re-read every 2 s. Unreadable = sufficient. On an idle system (no agent running) the memory wait is bounded at 60 s, then the start proceeds with a Warn.
- Acquire order: queue slot -> per-chat agent slot -> gate. Nothing takes a queue or chat slot while holding the gate, background runs hold neither, every wait honours ctx. A streamed turn holds its gate slot until its process has exited or been killed. The queue's `DefaultMaxConcurrent` (3) stays; the gate is the authority.
- Execs that do not call the model are not gated: auth status, session lookups, pid-file removal and kill, version probes, `/login`.
- The wait is shown to the user: the runner calls a `WaitNotice` carried in the run context once; Telegram shows `⏳ ждёт свободный слот агента…` / `⏳ ждёт свободную память…` as a stoppable draft (re-sent every 20 s while waiting, since drafts expire after ~30 s), or as the early placeholder on the edit path. A Stop while waiting ends the turn `stopped` (it used to fall back to the sync path and run the agent).
- Soft per-agent cap: Claude agent execs get `NODE_OPTIONS=--max-old-space-size=GOPOD_AGENT_NODE_HEAP_MB` (default 768, 0 = off), merged into an existing `NODE_OPTIONS`; not for Codex. Not a secret, so set by the runner (D012's forwarding rules do not apply).

**Rejected.** Limiting containers instead of running agents (idle containers cost almost nothing); making `RunStream`/`Run` take an extra callback parameter (the context value keeps signatures stable); gopod metrics `gopod_agents_running/waiting` (the catalog is checked against OBSERVABILITY.md §2.3 and the runner is not instrumented yet).

**Consequences.** With one user and one turn at a time nothing changes (no wait, no notice). Background extraction after a turn can be delayed by user turns. Enabling the memory cgroup (reboot) stays backlog. Spec: [superpowers/specs/2026-10-25-agent-admission-control-design.md](superpowers/specs/2026-10-25-agent-admission-control-design.md).

---

## D027 — Lifecycle notices

**Date:** 2026-10-25
**Status:** Accepted
**Extends:** D020 (durable turns)

**Context.** gopod restarts on every deploy (`docker compose up -d`) and after crashes. People in the middle of a turn saw it go silent and could not tell a restart from a hang; the owner could not tell a deploy from a crash. Recover already resumes the interrupted turns, but nobody was told.

**Decision.**
- On SIGTERM/SIGINT the signal no longer cancels the app context directly: `gracefulContext` (cmd/gopod) runs shutdown hooks first, then cancels every subsystem. The first signal restores default handling, so a second one kills at once.
- The hook (`telegram.Lifecycle.Shutdown`) writes `lifecycle:graceful_shutdown_at` to `router_state`, then sends one plain-text notice (`🔄 Перезапускаюсь…`) to the owner's main chat and to every distinct `(chat, thread)` with an unfinished turn (`ListUnfinishedTurns`; task turns, which have no chat, are skipped). A thread notice falls back to main when the topic is gone. Sends run concurrently under one 3 s deadline; failures are logged at Warn.
- On startup, after Recover and once the bot polls, `Lifecycle.Started` tells the owner only: `✅ Снова на связи (версия v)` when the previous run left the graceful marker, `⚠️ Был неожиданный перезапуск (версия v)` when a previous run recorded its start (`lifecycle:started_at`) but left no marker, nothing on the very first start. With replayed turns it adds `Продолжаю N незавершённых запросов.` (Russian plural). Chats with replayed turns hear from the resumed turn (existing banner), not from here. The marker is cleared after reading.
- Spam guard: at most one startup notice per 2 minutes (`lifecycle:last_start_notice_at`); a suppressed one is logged. A deploy that restarts gopod twice quickly or a crash loop produces one notice.
- Notices are not turn replies and are not recorded. `GOPOD_NOTIFY_LIFECYCLE` (default on; `0/false/no/off` disables) gates sending; without a bot token or owner id nothing is sent. Markers are kept even when disabled, so turning notices on later reports no phantom crash.
- `stop_grace_period: 30s` in docker-compose.yml: the 10 s default did not cover notice + agent kill + store close, and a SIGKILL mid-shutdown leaves no marker.

**Consequences.** A signal before the hook is registered (early startup, e.g. during Docker cleanup) sends no notice and the next start reports a crash. A SIGKILL after the marker was written is reported as graceful. Shutdown notices have no spam guard: two quick deploys send two "restarting" messages but one "back". Shutdown is up to 3 s slower.

---

## D028 — Inline-button callbacks: one layer, buttons are Router commands

**Date:** 2026-10-25
**Status:** Accepted
**Extends:** D011 (control plane), D012 (secrets), D022 (threads)

**Context.** The dynamic Telegram UX (spec `docs/superpowers/specs/2026-10-25-telegram-dynamic-ux-design.md`, F1, F7-F9) puts inline buttons on gopod's messages: turn actions (Stop, Retry, Regenerate) and settings (model/provider pickers, confirmations, paged command output). A button press is a new way into gopod, so it must not open a second authorization path next to the Router, and `callback_data` is 64 bytes of client-supplied text.

**Decision.**
- **One dispatcher.** Every `callback_query` goes to `telegram.callbackHandler` (`internal/telegram/callback.go`), which routes by the act prefix of `act:arg:...` to a handler registered with `onCallback` (one line per act, append-only). Every query is answered exactly once, also on an unknown act, an unregistered chat, an error or a panic; an inaccessible message (older than 48 h) only toasts.
- **Authorization from the message, never from the data.** The chat is the chat of the message carrying the button (`CallbackQuery.Message`), which must be a registered chat; the thread is that message's topic. Callback data is a routing hint, like `thread_id` in D022.
- **Data rules.** Callback data holds ids only (turn ids, random tokens, page numbers), at most 64 bytes, asserted in tests. No command names with arguments, no text, paths or secrets.
- **Buttons that change settings are Router commands.** `control.Response.Buttons [][]control.Button{Label, Command, Args, Style, Confirmed}` is the frontend-agnostic way for a handler to offer follow-up commands. Telegram shows each as `cc:<token>` (token: 12 random hex characters, in memory 10 min, bound to the chat it was shown in); a press dispatches `Command{Name, Args}` through `Router.Dispatch` with a `Caller` built from the callback exactly as `dispatchSlash` builds one (`IsOwner` = chat is `GOPOD_OWNER_CHAT_ID`), so `Perm` applies as for a typed command, and the button's message is edited in place to the result. A Router refusal is a toast and changes nothing. Text frontends print the buttons (`control.FormatText`).
- **Confirmations.** A risky command without `Command.Confirmed` answers `control.Confirm(cmd, question)`: `Code = control.CodeConfirm` and a Yes (`danger`, `Confirmed`)/Cancel pair. Telegram shows Yes as `cf:<token>` and Cancel as `cx:<token>`, valid 5 min and single-use as a pair: either press consumes both, so Yes runs at most once. Yes re-dispatches through the Router with `Confirmed = true`; the Router checks `Perm` again on that dispatch (`Confirmed` skips the question, never a check). The CLI confirms with `--yes` (`control.ParseConfirmFlag`). Confirmed per owner Q6: `/clear` and `/provider <name>`; `/chats unregister` and `/container restart|stop` do not exist yet and must confirm when added.
- **Pages.** A command reply longer than one message is one message with `‹ n/N ›` buttons (`pg:<token>:<n>`), split on line boundaries, edited in place; the pages stay in memory 10 min.
- **State in memory, durable ids where it matters.** Tokens and pages are forgotten on restart (the press toasts "expired"); turn actions carry the turn id and survive restarts through the `turns` table.

**Consequences.** A new settings button is a new `control.Button` in a handler's Response, not Telegram code. Anyone in a registered group can press its buttons, which equals typing there; owner-only commands are still refused by the Router. A restart invalidates every open picker, confirmation and page. Command buttons in an unregistered chat toast "not registered" (callbacks require a registered chat, slash commands do not). Button labels written by control handlers are in English like their texts until the strings move (plan Phase 5).

---

## D030 — Preserve conversation continuity across manual provider changes

**Date:** 2026-10-25
**Status:** Accepted
**Supersedes:** D022's provider switch clears main; narrows D028's destructive provider confirmation wording

**Context.** The owner requested seamless manual switching, including thread
isolation, before considering quota fallback. Provider CLI sessions are
incompatible; the exhausted provider may be unable to summarize its history.

**Decision.** Preserve native sessions on provider changes. Store a neutral
SQLite transcript per `(chat folder, thread)` and replay a bounded JSON tail
as historical data in the next provider's user prompt. Successful answers and
the last successful provider are stored atomically. Switching waits for the
chat's agent slot, checks container cleanup, and persists provider/model choice.
`/clear` invalidates neutral history and all provider session mappings in its
own thread; main and sibling threads remain isolated. Existing scoped memory
continues to supply its normal context.

**Consequences.** Manual switches require no call to the old model. Recent
user/assistant context crosses harnesses and survives restart; full native tool
state, old truncated detail and stopped partial replies do not. `/clear`
retains native files and the existing audit history but never resumes them.
Provider selection stays per chat. Automatic quota fallback is not implemented.
Spec: [provider continuity](superpowers/specs/2026-10-25-provider-continuity.md).

---

## D031 — Require confirmation for quota-triggered provider changes

**Date:** 2026-10-25
**Status:** Accepted
**Extends:** D028 command confirmations, D030 provider continuity

**Context.** The owner requested switching with a notification after quota
exhaustion, then explicitly preferred confirmation over unattended switching.
Provider selection affects every conversation in a chat.

**Decision.** A typed, known quota rejection offers a five-minute, single-use
confirmation in its own thread. On Yes, a hidden Router command validates the
original turn scope and durable quota offer, compares the expected provider
under runner locks, switches using D030, then queues one original-input retry.
The retry's source identity prevents duplicates and further switch offers if
that attempt is itself quota-limited. Notify the user of the selected provider,
queued request, stale choice or failure. Cancel never mutates state.

**Consequences.** No model calls or provider switches are started without
confirmation. Context and native sessions survive; the target may need login.
A prior failed turn may have performed actions, so workspace verification
remains necessary. Provider change and retry insertion are not one transaction;
a crash between them can require a manual resend. No unattended fallback,
automatic switch-back or quota polling. Spec:
[confirmed quota fallback](superpowers/specs/2026-10-25-quota-switch-confirmation.md).

---

## D032 — Separate sandbox runtimes and agent provider registration

**Date:** 2026-10-25
**Status:** Proposed — design saved, implementation not authorized by this ADR

**Context.** The owner asked to save specs for extending sandbox/runtime and
provider support, then deploy the existing Docker code. A concrete Docker
lifecycle and duplicated Claude/Codex choices currently limit extensibility.

**Proposal.** Keep agent CLI/auth/session behaviour in registered providers;
move allocation, isolation, execution, Stop and resource cleanup behind a runtime
contract. Qualify Docker first without behaviour changes. Preserve per-chat
runtime/provider selection and per-thread context. Additional backends/providers
and operational changes require separate implementation acceptance.

**Consequences if accepted.** A later ADR/implementation will supersede the
Docker-only constraint only for qualified adapters. This proposal does not
change current runtime support, enable subprocess execution, select vendors,
or authorize unattended fallback or new service spending.

Specs: [sandbox runtime](superpowers/specs/2026-10-25-sandbox-runtime.md) and
[provider registry](superpowers/specs/2026-10-25-provider-registry.md).

---

## D033 — Bound fresh system work and yield to foreground runs

**Date:** 2026-10-25
**Status:** Accepted

**Context.** A Codex memory-extraction run without a deadline held a chat's
execution slot for several minutes, delaying messages in multiple topics. Native
sessions were intact; the blocking process was a fresh system run. Context
cancellation alone does not terminate a Docker exec or unblock its hijacked read.

**Decision.** Bound RunFresh (extraction, compaction summary and task parsing)
to one minute, honoring shorter caller deadlines. A foreground slot waiter
cancels background work only in its own chat; foreground waiters precede new
background starts. Buffered user/system agents own a pid-file process group and
wait for a bounded TERM/KILL cleanup attempt before releasing their slots.
Docker attachments close on cancellation so reads can finish. Interrupted or
timed-out compaction preserves history, session mappings and turn counters.

**Consequences.** Best-effort extraction may be skipped when a user arrives.
A slow system/parser call may return cancellation/deadline rather than holding
the chat indefinitely. Full maintenance re-arming and quiet hours remain CP-006.
Kill failure is logged; this does not guarantee cleanup during Docker outages,
kill deliberately detached tools, provide per-topic providers, enable parallel
processes, or roll back tool side effects. Existing D030 continuity stays intact.

Evidence: [incident](incidents/2026-10-25-background-extraction-starvation.md),
PR #43; unit/race and actual Docker shell preemption tests, including on Pi.

---

## D034 — Observe Telegram capabilities without gating ordinary service

**Date:** 2026-10-25
**Status:** Accepted

**Context.** SDK v1.27.0 loses missing boolean information and misspells the
inline-support field. Its mandatory constructor getMe also makes observation
failure fatal before polling starts. BotFather settings alone do not imply
local handling or contextual rights.

**Decision.** Use a private getMe-only raw adapter with tri-state fields,
bounded requests and no raw error/body logging. Skip SDK constructor getMe;
SDK identity uses the token numeric prefix and does not need a fetched user.
Cache verified username separately for diagnostic command suffix validation.
A service-owned single-flight request outlives individual waiters but is joined
on shutdown. Retain stale last-success observations across acquisition failures.
Reports use control Router authorization and private-chat context gates.

**Consequences.** Ordinary DM routing is independent of observation freshness.
No BotFather settings, subscriptions or modes are enabled by diagnostics. New
reports/help remain private-DM-only until CP-019, and fit one message without
buttons. No generic registry, migration or provider request is introduced.

---

## D035 — Restore selection before bounded owner runtime checks

**Date:** 2026-10-25
**Status:** Accepted

**Context.** Restoring a provider formerly removed its container unconditionally,
while startup never ensured the selected runtime. Auth checks used the default
provider and Codex's negative status matched a positive substring. Management
observations must not start an inference session to discover local readiness.

**Decision.** Restore choices without container mutation; unresolved choices
fail closed. Warm only the registered configured owner before turn recovery,
under its agent slot. Compare immutable image identity and restrict destructive
reconciliation to the slot-protected startup path. Probe selected CLI/auth with
bounded process cleanup and maintain idle tracking. Add an allowlisted Codex
account/limits management exchange without thread/turn/model requests; report
unsupported Claude cold-start quota honestly. Keep missing allowance/reset
fields unknown and do not infer quota recovery.

**Consequences.** Startup does bounded work for one workspace, not every chat.
Diagnostics do not reset sessions, history, tasks or provider choice. Model
availability remains unverified without actual authorized inference. Runtime
checks and external observations have separate evidence and cleanup limits.
See [startup contract](STARTUP.md) and its linked official-interface research.

---

## D036 — Extend the existing namespace identity for delivery; freeze results independent of revision drift

**Date:** 2026-10-27
**Status:** Accepted

**Context.** CP-002 needs one host scope identity for results and deliveries. CP-020 already
owns `ScopeRef`, `namespace_ownership` and epochs, and compares `ScopeRef` by value. The
plan was written before that landed and also asks for thread and visibility in the scope.
The Store has a single SQLite connection, so helpers that open their own transaction deadlock
when called from inside another one.

**Decision.** Keep `ScopeRef` unchanged. Add `ConversationScope` (ScopeRef plus thread and
visibility) and a `ScopeAuthority` interface in `internal/store`; results and deliveries copy
the snapshot columns and never add a second identity registry. `FreezeTurnResult` requires the
namespace identity to be recorded but does not re-check registration revision or retirement,
because an existing model result must not be lost to a rename during the run; `PublishDelivery`
does revalidate the full `ScopeRef` inside its transaction. Store-internal helpers take the
`taskSQL` handle; public entry points use `withTx`, which opens a transaction or enlists in the
caller's under a SAVEPOINT, so a failed publication leaves no partial group in either case.
Dispatcher wake-up runs only after a standalone commit; enlisted callers notify after their own
commit. Parts are size-bounded at publication (64 KiB payload, 8 KiB markup, 512 B artifact
reference). Rejection tombstones are never dropped or rewritten; beyond a per-namespace (5000) and
global (50000) cap a new rejection is not stored and a per-namespace counter in `router_state`
is incremented instead (`DroppedRejections`), so a flood cannot grow the table. A part payload
can be cleared only after its delivery group is resolved.

**Consequences.** CP-004/020/021/022 reuse the same identifiers and interface. The freeze
boundary and the publication boundary authorize at different times by design. Payloads of
terminal deliveries are cleared by compaction while dedupe tombstones are kept.

---

## D037 — Single-attempt outbox dispatch: claim, call, complete

**Date:** 2026-10-28
**Status:** Accepted

**Context.** CP-002 PR 2 sends durable parts to Telegram. The Store has one SQL connection, so no
transaction may span an API call, and existing send helpers retry, fall back to plain text and
redirect a gone topic to the main chat, all of which can duplicate or misplace a message.

**Decision.** Dispatch is three separate commits: `ClaimNext` (part sending, attempt row and
fence, with scope revalidated in the same transaction), one transport call with no transaction
open, then exactly one of ack / release-unsent / fail / mark-uncertain. The transport interface is
declared at the dispatcher (consumer) and makes one API attempt with no fallback; an unknown outcome
is ambiguous and the zero `OutcomeKind` is ambiguous. Exclusivity is an OS file lock beside the
database plus a fresh fence in `router_state`; a superseded instance fails every commit with
`ErrOutboxFenced`. The restart sweep turns every `sending` part into `uncertain` and never resends.
Only an explicit 429 or a proven pre-dispatch failure is retried (5 attempts, expiry-bounded,
`retry_after` wins); a stale scope cancels instead of sending. Reasons are fixed machine codes and
diagnostics are allowlisted lowercase fields, so a raw credential-bearing error is never stored.
Every terminal write is retried with backoff on a fresh context; if an ack still cannot be
recorded the part is marked uncertain (`ack_not_persisted`), and an in-process reconcile sweep
marks parts that no live worker holds (and older than a grace period) uncertain
(`orphaned_while_sending`), so a transient database failure does not block a conversation until
restart. A part held by a running worker is never swept. A retry_after gets a 1s margin. If the
lock is still held at startup the bot retries for about 10s, then runs with the dispatcher
disabled instead of failing. The dispatcher is behind `GOPOD_OUTBOX_DISPATCHER` (off by default).

**Consequences.** Uncertain parts need an operator decision (superseding delivery, deferred). A
lock file is Unix-only, which matches the supported hosts.

---

## D038 — Durable text IPC: actor grant and destination scope are separate, one delivery owner per run

**Date:** 2026-10-28
**Status:** Accepted

**Context.** CP-002 PR 3 moves agent text IPC into the outbox. The legacy path let the owner
folder address any JID, kept terminal outcomes in memory, and scheduled turns (chat id 0) still
tried to stream stdout to Telegram, where a failed final send is reported as an undelivered reply
and makes the queue rerun a finished model execution.

**Decision.** The route has two independent gates. The actor grant is the host-selected owner
folder still bound to the personal chat; the destination scope is the owner's personal chat at
thread 0, visibility `conversation`, snapshotted from the live registration in the same pass.
Every other request, including groups and shared chats, keeps the legacy direct path unchanged
(CP-020 isolation is not relabelled), so the route is never a fallback for a denial. Identity is
namespace + epoch + request filename + SHA-256 of the exact bytes; the delivery owner is
`legacy_agent_ipc`. Order of effects is store commit (delivery or rejection tombstone), then
receipt, then archive; any failure leaves the request file, and the next pass converges on the
same delivery (`Created=false`), so a restart never publishes twice. A same-name different-bytes
request is rejected with its own `.conflict.result.json` receipt so it cannot overwrite the
original delivery's receipt. Receipts are advanced from durable state (`RecentIPCRequests`) as
queued, sent, uncertain or failed; accepted means queued. Uncertain rows are diagnostics only; no
resend command exists. Scheduled turns run through `runTaskSilently`: stdout is discarded, nothing
is sent to Telegram and the queue never sees an undelivered-reply error. The route needs
`GOPOD_OUTBOX_IPC=1` and a running dispatcher (`GOPOD_OUTBOX_DISPATCHER=1`), and is off by default.

**Consequences.** Agents see `queued` instead of `ok` on the personal chat when the flag is on.
A request file older than 24 hours is rejected as expired. A rejection whose tombstone cannot be
stored (cap, repeated store errors) is failed without one and is simply rejected again if retried.
A live smoke on the real bot is still required before enabling the flag in production.

---

## D039 — Immutable artifact snapshots: reserve first, copy outside SQL, publish with references

**Date:** 2026-10-29
**Status:** Accepted

**Context.** CP-003 PR 4 lets an agent attach generated files to an IPC message. The Store has one
SQL connection (no transaction may span file I/O), the filesystem and SQLite are not one atomic
resource, the source path is writable by the agent, and the outbox must never upload bytes that
were not the ones accepted.

**Decision.** Strict V1 requests (unknown fields rejected) are authorized by a host-issued per-run
capability, an in-memory token bound to the IPC folder, the destination `ConversationScope`
(therefore its `ScopeRef`), the delivery owner, an expiry and one ordinary output root. No request
field names a recipient, thread, privacy level or owner role; a controller-owned run, a group
folder or a destination other than the owner's personal chat is `recipient_unavailable`. Output
roots must be a direct child of the chats directory; the data directory, `ipc`, `sessions`,
`shared-namespaces` (CP-020 protected siblings), snapshots, artifact blobs, backups, mount-denied
paths, hidden trees, `memory/` and database/WAL names are refused. Revoking at run end makes
unaccepted requests fail closed (`stale_capability`); an already accepted request is looked up
first and converges on its delivery, so a revoked run does not lose committed work.

Preparation order: (1) `ReserveArtifacts` takes the full upper bound (`min(files x 20 MiB, 40 MiB)`)
under namespace (512 MiB) and global (2 GiB) quota and creates one `preparing` artifact per file,
in one transaction; (2) outside SQL each source is walked from a held directory descriptor with
`O_NOFOLLOW` on every component, must be a regular file with link count one within the bound, is
copied into a 0600 file in a 0700 host-owned UUID directory while hashing, fsynced, re-read and
compared (so a change is caught even where timestamps are too coarse), renamed and the directory
fsynced; (3) capability and scope are revalidated and `MarkArtifactsReady` commits the facts;
(4) `PublishDeliveryWithArtifacts` publishes the plan, attaches `artifact_refs` to the parts and
releases the reservation in one transaction. A duplicate request (same key and bytes) returns the
first delivery and abandons the new preparation; changed bytes are a conflict. Upload parts carry
a plain caption in their payload and a reference `artifact:<uuid>`.

Upload: `SnapshotOpener` resolves the reference through the inventory (state must be `ready`),
opens the blob without following links, checks size, link count and SHA-256 into a buffer bounded
by the file limit, and hands those bytes to the single upload attempt. There is no second pathname
open. A mismatch or missing blob quarantines the artifact and uploads nothing; the part fails with
an explicit refusal and the acknowledged prefix is never resent. Quarantine also fails still-pending
parts that reference the artifact.

GC and reconcile: an artifact is pinned while any referencing part is pending, sending or
uncertain or its group is unresolved; the pin is one SQL predicate used by GC, completion and
deletion triggers on `artifacts` and `artifact_refs`. Terminal artifacts become `deleting` six hours
(configurable `ArtifactConfig.Retention`, a deliberate shortening of the spec's 30 days so a
prompt-injected agent cannot hold the quota) after the last reference activity; the host unlinks the blob, then drops the row, so a crash
between the two finishes idempotently. `Reconcile` (at the watcher cadence, default 5 minutes)
expires preparations past their deadline (15 minutes, a shorter bound than the spec's 24 hours
because nothing here prepares slowly), finishes deleting states, quarantines ready artifacts whose
blob is missing or the wrong size, un-quarantines an artifact whose blob verifies again, and
removes unknown UUID directories older than two hours. The sweep is held (loudly logged) when the
blob root holds no preparation at all while ready rows exist, so a recreated or wrongly mounted
root does not quarantine everything. A hard link inside the private blob store is not an error:
the SHA-256 decides. Deleting rows are paged by id so failing rows cannot block the rest. It
never touches other names or follows links.

Flag and gate: `GOPOD_OUTBOX_ARTIFACTS` (default off) opens the private store and enables V1
only while the dispatcher and the durable text route run. The legacy upload paths are untouched.
**Enablement prerequisite:** the CP-007 matched DB and artifact backup, lease and restore must pass
first; this flag does not check it. Groups additionally need CP-020 and CP-019 acceptance.

**Limits, stated plainly.** Tests use temporary directories and SQLite with injected failure points
(including simulated crashes at every fsync/DB boundary); they are not a security proof and not a
Raspberry Pi qualification. Path policy detects links, special files, hardlinks and changes during
copy; it does not detect a secret placed in an innocent file and it is not a container boundary.
Hash verification happens when bytes are opened for upload, not in the reconcile sweep. Unix only
(Linux/macOS). Nothing in this binary issues capabilities yet, so even with the flag on every V1
request is rejected as `stale_capability` until the runner integration lands. Voice is refused
(`voice_unavailable`). `photo` presentation and MIME are hints; uploads are always documents.

**Review amendments (2026-10-29).** (1) `PublishDelivery` refuses upload parts; they exist only
through `PublishDeliveryWithArtifacts`, and the dispatcher passes the delivery's namespace and
epoch to the opener, which refuses an artifact of any other namespace or epoch. (2) A revoked
capability is honoured for a two-minute grace window (`Capabilities.Grace`) for a request file
written before `EndRun` (file mtime not after the revocation), so an agent whose last action was
writing the request does not lose it to the poll interval; later files, an unknown write time and a
policy revocation (`RevokeFolder`) fail closed with `stale_capability` (tombstoned). The grace
trusts the file mtime, which a stray process left in the container could backdate; the window is
short and the scope, destination and quotas still apply. (3) Per-run (8 files, 80 MiB) and
per-namespace-per-hour (40 files, 200 MiB) admission caps, counted on reserved bytes while active
and measured bytes afterwards (`rate_limited`), plus a budget of two file-bearing requests prepared
per folder per watcher tick. A snapshot is written with a bound of the remaining reservation, never
past it. (4) `Issue` requires the output root to be the actor's own chat directory and the
destination to be the actor's registered chat (resolver), caps a grant at six hours, and prunes
expired entries. (5) Only the owner folder reaches the artifact route; other folders keep the legacy
text path. (6) Opens use `O_NOCTTY`. A vanished host-side preparation directory or output root is a
retried host fault, never a `source_missing` tombstone.

**Not a defence against a hostile owner-chat agent writing the database.** The owner-tier container
mounts `store.sqlite` read-write. The triggers and inventory invariants above therefore do not hold
against an agent that writes the DB file directly; they protect against bugs and against the
request path. The blast radius of such an agent is bounded by UUID-validated blob keys (a key that
is not `<uuid>/<uuid>.blob` is never opened), no-follow opens under the held blob root, and
SHA-256 verification of the bytes before any upload; it could still redirect uploads to other
blobs it can name, or break the inventory.

**Consequences.** Reserved, stored, quarantined and deleting bytes all count against quota, so a
leaked reservation is visible and bounded by its TTL. A multi-file request is all-or-nothing at
admission but not at Telegram: parts are acknowledged separately and a failed upload leaves a
partial delivery. Operator resend of uncertain uploads remains deferred.

---

## D041 — Prepared personal attachments: a ledger before any paid call, direct send until PR 6

**Date:** 2026-10-29
**Status:** Accepted

**Context.** CP-003 PR 5 must put the two host-generated attachments of a personal final reply,
oversized code documents and the automatic TTS voice clip, on immutable preparation without changing
which replies are spoken. A TTS call is billed and its result can be lost after the money is spent,
so "retry on failure" is not safe. Foreground delivery still goes through the direct send helpers
(reply threading, topic-gone fallback) until PR 6, and the outbox transport has neither.

**Decision.**
1. **Preparation of host bytes** follows the agent-file order: reserve the upper bound
   (`ReserveArtifacts`), write a private snapshot outside SQL (`artifactfs.SnapshotBytes`: 0600, fsync,
   no overwrite), `MarkArtifactsReady` with the measured hash. The upload sends the bytes read back
   through `ReadVerified`, never the caller's buffer. Code documents release their preparation after
   the upload; failure to prepare keeps the block inline (the existing failure behavior).
2. **Generated-artifact ledger** (`generated_preparations`): one row per (namespace, epoch, kind, key),
   written by `BeginGeneration` before the call. States `generating`, `ready`, `failed`,
   `preparation_unknown`; a finished row never reopens (trigger) and every state blocks the same key,
   so a second paid call needs a new key, which is a new explicit intent. Only an explicit refusal
   status (401/403/429/400/422 from the backend adapter, with a fixed lowercase code) is `failed`;
   timeout, cancel mid-call, 5xx, empty or oversized result, or a failed write after the bytes arrived
   is `preparation_unknown`. Terminal writes use a fresh context so a cancelled attempt still records
   its outcome. Startup sweeps every `generating` row to unknown; a live sweep uses a 10-minute grace.
3. **Authorization before any effect**: `SpeechPolicy` grants name the destination JID, the one
   backend, a credential reference (never a value), a character bound and an explicit
   `BilledCostAccepted`. An unauthorized request fails before the credential is read, storage is
   reserved or the ledger is touched. A different backend is unauthorized; there is no fallback. The
   personal grant is built only for the configured owner chat; groups never reach the personal key.
4. **Storage is reserved before the call**, so a quota failure costs nothing and consumes no intent.
5. **Direct upload is at-most-once**: the ledger row carries `upload_state` (`none`, `claimed`, `sent`,
   `refused`, `unknown`). The claim is taken before the API call, so a retried turn, a duplicate hook
   or a crash never uploads the same clip twice; a claim without an outcome is unknown, never
   "not sent". The legacy voice reply is unchanged in selection (one shared `voiceCandidate`),
   thread, reply target and filename; characterization tests run the same table on both paths.
6. **One plan** (`VoiceReplyPlan`, `PublishVoiceReply`): rendered text parts followed by one upload
   part carrying the host-only voice presentation (`store.UploadPresentationVoice`, in the upload
   part's otherwise unused `ParseMode` field, so no schema change). The agent V1 request cannot set it
   (tested), so agent files stay documents. The request identity binds the key to the text bytes and
   the audio hash: a retry converges on one delivery and one upload, different content is a conflict.
   `DescribeVoiceDelivery` derives the user-facing status from stored part states and never reads
   uncertain as delivered or failed. The foreground does not use the plan yet (PR 6).
7. **Flag**: everything is behind `GOPOD_OUTBOX_ARTIFACTS` (no new flag, no new environment name).
   With it off or without an owner chat the legacy paths run unchanged.

**Limits, stated plainly.** Fake backends only: no live TTS, billing or Telegram qualification;
that needs its own authorized scope. Admission rate caps apply to host-generated code documents too
(40 files and 200 MiB per namespace per hour); beyond them the block stays inline. Agent-requested
voice (`voice:true`) stays `voice_unavailable`: it needs the runner-side capability issuer. A ready
clip that is never published expires with its reservation (one hour) and is never regenerated.
Same-process crash windows between the ledger write and the artifact write are covered by the
startup sweep, not by fault injection at every boundary.

**Review amendments (2026-10-29).** (1) The restart sweep (`SweepGenerationsBefore`) runs
synchronously before the bot polls and only flips rows created before this process started; a
generation begun after the restart is never marked unknown. (2) Long replies are truncated to the
legacy 4000 bytes before authorization and keying, on a rune boundary (the legacy cut,
`text[:4000]`, can split a multi-byte character and is kept unchanged on the legacy path); a reply
over 4000 characters is therefore spoken, not refused by the cost bound. (3) A claim is released
(`claimed` to `none`, keeping the preparation) only for a failure provably before the Telegram API
call (clip unreadable, connection never made); anything that may have reached the API stays
`claimed` until it finishes as `unknown`. (4) The "recording voice" indicator is sent immediately
before a real backend call, after authorization, credential and ledger checks. (5) The voice upload
marker is accepted only by `Store.PublishVoiceDeliveryWithArtifacts` (an unexported request flag);
every other publish path and the transport reject any other upload `ParseMode`. (6) Finished ledger
rows are purged after `GOPOD_GENERATION_RETENTION_DAYS` (default 30): a failed or unknown row
guards a key against a second paid call, and keys embed the turn id and a text digest and are never
replayed days later, so it is kept exactly as long as a ready row; rows with an upload in flight
are never purged.

**Consequences.** A paid TTS call can happen at most once per intent, and what happened is durable.
A failed or unknown voice leaves the text reply untouched, as today. PR 6 can switch foreground
delivery to `PublishVoiceReply` without changing preparation.

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

## D040 — Task reliability storage: managed definitions are revisioned tombstones; admission is one planned CAS transaction

**Date:** 2026-10-29
**Status:** Accepted

**Context.** CP-004/021/022 PR 1 adds the storage for isolated, outcome-tracked and policy-driven
tasks. Today the scheduler logs, enqueues and advances a cursor in separate writes, `DeleteTask`
hard-deletes, and the Store has one SQLite connection, so an admission that calls `Queue.Enqueue`
or any helper on the outer handle from inside a transaction deadlocks.

**Decision.** `scheduled_tasks` gains additive columns (`execution_version` 0 legacy / 1 managed,
`definition_revision`, kind, session and delivery mode, the ScopeRef snapshot copied from CP-002's
namespace identity, policy bounds, expiry, tombstone). Legacy rows keep `legacy_main`, `legacy_ipc`,
thread 0 and no scope; nothing is backfilled and legacy mutators refuse managed rows. Managed
definitions are never physically deleted: `DeleteTask` tombstones them (revision bump, future
admission off, proven-never-started turns stopped, started or uncertain attempts left
`cancel_requested`/`execution_unknown` with their reservation, pending reminder deliveries cancelled
through CP-002, uncertain ones untouched). Ledger tables (`task_occurrences`, `task_runs`,
`task_attempts`, `task_verifications`) use `ON DELETE RESTRICT`, `BEFORE DELETE` triggers reject raw
deletion, and terminal states, occurrence decisions, receipts and the provider snapshot are
immutable. `AdmitTaskOccurrence` takes a plan made outside the store (cron, DST and policy code
stays in the scheduler) and applies it in one transaction: it compare-and-sets revision and
cursor, revalidates the stored ScopeRef, applies expiry, `max_runs` and overlap, records bounded
skipped/coalesced/expired decisions, inserts run, attempt and the durable turn through the
transaction-aware `insertTurnQ` (or the CP-002 delivery for a literal reminder), and advances the
cursor, which may only move forward. The injected `Now` is the only clock inside it. Schedule
`pause/delete` and execution `RequestRunStop` are separate intents. A chat rename advances the
registration revision and makes admission fail closed until an explicit `RebindManagedTaskScope`
(same namespace, epoch, chat and folder only). Purge is not implemented: `TaskPurgeBlockers`
always includes `audit_export_unverified`. Group chats are refused. Activation stays off: nothing
calls the new APIs yet.

**Consequences.** One committed writer wins between competing ticks, edits and pauses, and a crash
leaves either the whole admission or none of it. Later PRs wire dispatch (PR 2), execution (PR 3),
settlement (PR 4) and the policy planner (PR 5) to these APIs. A rename needs an operator rebind
before managed tasks in that chat run again. Managed evidence only grows until a later explicit
maintenance step exists.

## D042 — Managed task dispatch: a spawn marker splits "never started" from "may have started"

**Date:** 2026-10-29
**Status:** Accepted

**Context.** CP-004/021 PR 2 must dispatch committed task turns without coalescing them with
interactive work, and recover after a crash without ever running a model twice for one occurrence.
The legacy queue retries failures with backoff and replays running/interrupted rows, and passes a nil
error to done hooks for `ErrStopped`; both would mislabel or repeat managed work.

**Decision.** An item with an `AttemptID` (read from `task_attempts` by the turn adapter) is a managed
singleton batch: `sameBatch` never merges it with anything, even at thread 0. `Queue.Dispatch(turnID)`
schedules an already committed turn; `Recover` finds it again after a crash, so no in-memory handoff
is authoritative. In the worker, after the chat slot is acquired, the provider snapshot is resolved
and `ReserveTaskAttempt` commits queued -> reserved (snapshot, held capacity, rechecked scope, expiry,
tombstone, stop request, frozen result) in one transaction; a failure to commit means no handler call.
The handler must commit the **spawn marker** (`Execution.BeginSpawn`: attempt reserved -> running, run
queued -> running, atomically) before requesting native execution. That marker is the proof boundary:
queued/reserved attempts are proven never started and may be redispatched with their original snapshot
(bounded by `ErrNotStarted` backoff and the turn attempt cap); running, cancel-requested or
`execution_unknown` attempts are never run again, by backoff or by `Recover`. Terminal states are
produced only from host-observed exit evidence (`Execution.Exited`): a nil error, `ErrStopped`, a closed
attachment or a timeout without that evidence settles `execution_unknown` with the reservation held,
and a cancel request that landed during the run wins over a success report. Boot recovery moves
possibly started attempts and frozen-result attempts to `execution_unknown`, closes their turn rows so
they cannot replay, rebuilds held reservations **before anything is scheduled** (each hold blocks its
folder and takes a global slot when one is free), and fails startup if holds cannot be loaded. A frozen
result recovers publication only (`OnFrozen`). `ReleaseHold` needs `local_exit_observed` or
`operator_reconciled` evidence and never authorizes replay of uncertain remote effects. Legacy
`OnDone` hooks are not called for managed items; `OnManagedDone` reports the ledger state.

**Assumptions and limits.** Single controller: one gopod process owns the database and task
containers, so the ledger alone rebuilds holds; two live controllers are unsupported. An ambiguous
spawn-marker error counts as started. Runtime execution (PR 3) must supply the real handler, snapshot
resolver and exit evidence; production wiring in this PR is inspect-only (no handler), so nothing
managed runs and legacy behavior is unchanged. Task deletion/stop coordination with dispatch,
settlement into published results and `/tasks` visibility are PR 4. A stop that wins before the marker
settles the attempt cancelled only through the controller's own refused marker.

**Consequences.** A crash cannot produce a second model run for a started occurrence; the cost is that
a possibly started attempt blocks its folder and one global slot until a local exit or an operator
reconciles it. Orphaned `running` attempts without an unfinished turn row are listed as holds but
only an operator release (after an `execution_unknown` transition) clears them.

Shutdown classification: a shutdown that races a handler return counts as an interruption only when the handler also returned an error. A nil return with host-observed exit evidence is recorded as exited even if shutdown raced it, so a handler that swallows cancellation and returns nil after proving exit is recorded as completed. A handler that wants an interruption recorded must return the context error.

## D043 — Backup foundation: private-connection snapshots, a lock-based fence, sealed sets and a restore latch

**Date:** 2026-10-29
**Status:** Accepted

**Context.** CP-007 slices 4–6 need a consistent copy of the single SQLite file on a host with no
`sqlite3` binary, while the application keeps one pooled connection (`SetMaxOpenConns(1)`), without ever
stopping owner work and without letting a restored copy start polling Telegram.

**Decision.**
1. *Snapshot.* `store.SnapshotFile` uses SQLite's online backup API (`ncruces/go-sqlite3` v0.20.0
   `Conn.BackupInit`/`Step`) from a **separate, private** connection opened `mode=rw` (never created), so the
   application's pooled connection is never borrowed and cannot deadlock; WAL readers do not block writers. Read-only
   was tried and rejected: under a concurrent writer a read-only WAL reader fails with `READONLY_RECOVERY`. Steps are
   bounded, `BUSY/LOCKED` retry until `ctx`, the destination is created 0600 `O_EXCL`, converted to
   `journal_mode=DELETE` (a single file, no sidecars), integrity- and foreign-key-checked, fsynced. Alternatives
   rejected: live `cp` of db/wal, `VACUUM INTO` (no incremental cancel, spec names the backup API).
2. *Compatibility without a ledger.* There is no migration ledger and SQLite `schema_version` is only a change
   counter, so "newer than this binary" is decided by comparing the file's tables and columns with what
   `Open` creates on a scratch database (`CheckSchemaCompat`): an unknown table or column means newer
   (`ErrSchemaNewer`); missing ones mean older and are migrated forward. The manifest also records a table/column
   fingerprint and per-table row counts.
3. *Sets.* A set is a directory (`store.sqlite`, optional `config/`, `artifacts/`, `manifest.json` with per-file
   SHA-256, `COMPLETE` marker = hash of the manifest). It is written in `<root>/.staging/<id>`, fsynced file by
   file and directory by directory, and **published only by one `rename`**; every failure before that removes the
   staging tree, so no partial set is ever listed or restorable. Failure at the final root fsync keeps a complete,
   verifiable set and reports the error. The root must be absolute, 0700, owned by the user, a non-symlink and
   outside the data dir, chats dir, repo and every declared source. Caps and free space are checked first and fail
   closed. Verify restores the DB into a scratch dir and inspects it read-only; it never calls `Store.Open`.
4. *Fence.* The controller holds an advisory `flock` on `<data>/.controller.lock` for its life (60 s wait at
   start). `matched_stopped` takes the same lock, so success is kernel evidence that no controller runs and none can
   start during the copy, then requires a **read-only** inventory of live owned containers (Docker label
   `gopod.chat`; running/restarting/paused). An unknown inventory is refused unless the operator asserts that no
   containers exist (recorded as `operator_asserted_none`). The fence interface has no stop, kill or remove
   method: unsettled writers produce a refusal (exit 3) and nothing is changed. This replaces the specified Unix
   socket + host supervisor for this slice because an in-process `flock` is dependable evidence, needs no
   agent-reachable endpoint and survives crashes; the socket remains a later option for an online fence.
5. *Quarantine.* `GOPOD_RECOVERY_QUARANTINE=1` or a latch file `<data>/.restore-quarantine` (fail-closed: any
   state but "absent" counts) makes `main` run the control plane only, **before** the metrics listener, `Store.Open`
   and every subsystem; it only reads the DB read-only and logs a validation result. Leaving it is
   `gopod backup quarantine release --yes` (host action) then a normal start.
6. *Operator surface.* `gopod backup create|verify|list|idle|quarantine` go through the control Router
   (`PermOwnerOnly`, empty slash name so no chat can reach them; `LookupBySlash("")` no longer resolves).

7. *Review hardening.* Root overlap is also decided by device/inode identity along the ancestor chain, so aliases
   and bind mounts count. Each capture holds a per-capture lock file beside its staging dir; later captures remove
   staging entries older than 30 minutes only when that lock is free, and staging never counts toward the byte
   cap. Verify opens every file component by component with `O_NOFOLLOW`. Pre-existing foreign key violations:
   `database_only` copies the database as found and records a manifest warning (table names and counts only,
   re-checked by verify, and a manifest that hides them is a mismatch), because a protective backup of a damaged
   database is better than none; `matched_stopped` stays strict and refuses, because it claims a recoverable cut.
   The scratch directory for verify defaults to `<root>/.staging` and is configurable. `matched_stopped` also
   refuses while a non-one-off container of the compose service `gopod` is live (best effort), closing the gap
   left by pre-lock controllers.

**Limits.** `matched_stopped` here covers the database, declared config files and artifact blobs only; ChatsDir
workspaces, provider session/auth homes and IPC files are listed in the manifest as `not_covered`, so it is **not**
a full matched filesystem/auth recovery guarantee. Hashes detect corruption, not an adversary who can rewrite a
whole set. No timer, retention pruning, off-host copy or automated restore/activation exist yet.

**Consequences.** A backup can be taken online (`database_only`) or with the controller stopped
(`matched_stopped`) from the existing image. A second controller on the same data dir now fails to start instead of
sharing the database. Artifact delivery stays gated on the CP-003 extension (blob manifest is implemented;
leases pinning GC during capture are not needed while writers are stopped but are required before an online
matched mode).

---

## D044 — Managed task execution: policy-guarded containers, ephemeral native runs, attempt-bound reports

**Date:** 2026-10-29
**Status:** Accepted

**Context.** CP-004 PR 3 must run a scheduled task without touching the foreground native session
and without the reusable personal container silently granting owner mounts, env or tools. Today
`Ensure` reuses a container by folder and image only, `RunFresh` is a one-minute maintenance path,
and nothing proves a container's authority.

**Decision.** (1) `EffectiveExecutionPolicy` is derived by the host for roles `foreground_personal`
(the unchanged owner-tier default) and `scheduled_task_personal` (registered-tier mounts, no
forwarded env, grants `ipc:messages` and `ipc:task-results` only). Its fingerprint (`epp1:`+sha256)
covers role, namespace/epoch/registration revision, mount sources/targets/modes, env NAMES,
credential binding, image reference and grants; never secret values. The snapshot resolver stores it
as the attempt's `profile_fingerprint`, and the container carries it, the role and a digest of its
inspectable configuration as labels. (2) Under the folder lock the guard reuses a container only if
labels are well formed, the Docker-inspected mounts/env-name/hardening digest equals the recorded
one, and the fingerprint equals the request. Missing or stale labels, unsupported inspection and
drift fail closed (`ErrPolicyUnknown`). A well-formed container of another policy is recycled
(stop, remove, recreate) only when the caller holds the folder's agent slot and no
`execution_unknown` reservation holds the folder; native credential/session directories are host
bind mounts and are never touched. (3) `RunTaskStream` uses `claude --no-session-persistence` /
`codex exec --ephemeral`, no `resume`/`--continue`, no session store, handoff, memory or compaction,
the ordinary lifetime (5 min default, 60 max) and no maintenance preemption. The attempt runs on
its frozen provider, not the chat's current one. Exit evidence comes only from the daemon's exec
report; a kill without that report leaves the attempt `execution_unknown`. (4) The task role is
**denied unless a `TaskRoleQualifier` approves the credential binding**. Both providers keep login
state in a host directory that also holds foreground transcripts, so sharing it is unqualified;
none is qualified here, so with `GOPOD_MANAGED_TASKS=1` tasks are denied (`queue.ErrDenied`:
settled not started, no retry). (5) `task-results` v1: strict parser (32 KiB, unknown fields and
trailing data rejected, bounded lists/text), an in-memory token bound to folder/run/attempt/scope,
revoked at settlement, stale/spoofed/late/changed-scope reports rejected, stored as
`model_reported` metadata only for the live running attempt, evidence never fetched.

**Review amendments.** Recycling needs an explicit `slotHeld` from the caller (turns, startup and
`RunTaskStream` pass true; the public `Ensure` passes false and never recycles a running container).
A container with no policy labels at all is legacy: replaced under the same conditions; a partial or
malformed label set is denied. The task role mounts `ipc/messages` and `ipc/task-results` only. The
exec exit wait keeps polling on a detached 10 s context after a timeout or cancel (not after
`Close`), so a successful kill is observed. The agent-visible capability env is
`GOPOD_TASK_REPORT_CAP`. The sweeper rules on an unparsable file only in the final sweep and refuses
a symlinked report directory.

**Consequences.** Flag off: no code path changes. Flag on: the foreground container also goes
through the guard, and unlabeled leftovers are replaced at startup. Open: a qualified credential
adapter for the task role, real-Docker inspection normalisation (fixture-verified only), native
smoke, per-role IPC enforcement in the watcher (grants are declarative; authority is still the
host namespace), report poll interval of 10 s plus a final sweep.


## D045 — Personal IPC formatting and a recorded plain-text fallback

**Date:** 2026-10-29
**Status:** Accepted (owner's rich-message requirements, PR #82)

**Decision.** The enabled owner-personal durable IPC route defaults to Markdown; explicit
`format` accepts `markdown`, `html`, or `plain`. Legacy requests without rich fields remain
literal. Explicit format/buttons/sticker fields are refused on unsupported routes; buttons
and stickers are deferred and rejected on the formatting route too. Invalid format values
produce persisted rejection receipts. HTML is sanitized, balanced and bounded before chunking
and freezing. Unprovable markup becomes the original literal text. The internal pure-Go
`telegramhtml` package shares canonical HTML validation between Telegram preparation and store
admission, avoiding a dependency from the store to the Telegram bot. No external parser dependency.
The conservative parser accepts well-formed XML-shaped HTML plus boolean `blockquote expandable`;
unsupported tags become visible text, unsafe links lose their target, unknown entities or malformed
nesting downgrade the whole message to plain. Markdown headings/lists/quotes/strike/spoilers and
safe links extend foreground rendering while existing supported syntax keeps its output.

**Supersedes the D037 no-second-call rule only in this case:** after an explicit 400 entity-parse
refusal of an HTML text/edit part, the dispatcher may atomically close the refused attempt and
create one plain-fallback attempt. `outbox_plain_fallbacks` binds that attempt to the part;
its uniqueness prevents a second fallback. The store checks the fence, open claim, scope,
expiry and total attempt budget. The original payload/parse mode/plan hash never change; the
plain text is deterministically derived at dispatch. A failed/unknown commit never authorizes a
send. A crash while the fallback is sending becomes uncertain on recovery, including acceptance
without a persisted ack. No timeout/5xx/ambiguous result triggers fallback. The plain fallback is
one-shot even on an explicit 429; it fails rather than creating a retry loop. Acknowledged fallback
parts are identified as sent as plain text in diagnostics (until normal history compaction).

**Limits.** Existing dispatcher/IPC flags only; no production activation or live acceptance is
claimed. Buttons, sticker registry, thread targeting, and per-task format defaults are outside P1.

## D046 — Owner restart uses verified Docker supervision and a durable receipt

**Date:** 2026-10-29
**Status:** Accepted

**Context.** The owner requested a Telegram command to restart the gateway and
agent containers, with before/after notices. A bare process exit can leave an
unsupervised bot offline, and a generic lifecycle "back" notice does not prove
container readiness. Replayed Telegram commands must not create restart loops.

**Decision.** `/restart` is Router `PermOwnerOnly`, private Telegram messages only.
Inspect the controller's own Docker identity, Compose service, active restart
policy and ten-second successful-start window. Persist a request and message
high-water mark in `router_state`, send the initiating thread an acknowledged
notice, persist `restarting`, then cancel the normal application context. After
subsystems drain, stop only captured personal container IDs with matching scope,
label and workspace mount; retry that idempotent stop at next boot before cleanup.
Existing startup restores choices and recreates/locally checks the owner runtime.
After recovery, an explicit ready/degraded notice uses that observation plus a
current running check; it never claims inference or quota success. Claim its send
in durable state first; ambiguous sends are not replayed. Suppress generic
lifecycle messages for the explicit restart. No native session/auth directory or
volume is removed; no group is enabled. Unresolved managed attempts and active
non-personal runtimes refuse the operation. There is no new package or schema
migration, only typed state operations within `internal/store`.

**Consequences.** Docker Compose deployments can request restart from Telegram
without handing arbitrary host control to the model. Host binaries, unsupported
supervisors, unqualified group recovery and a wedged drain require host operation.
The command restarts the installed image, not a new version. Real Docker/Telegram
acceptance is tracked separately from unit/CI success.

## D047 — Registered groups are admitted by default

**Date:** 2026-10-29
**Status:** Accepted (owner decision 2026-10-29)

**Context.** CP-020 admitted a group only when its JID was in `EnabledGroups` of
`group-runtime.json` (read at startup), it had a ready clean epoch from a manual
`/isolation prepare`, and the owner ran `/isolation enable`. The owner owns every
registered group and asked that groups work without that separate server-settings
permission: the owner-only `/register` is the permission.

**Decision.** `AdmissionMode` selects the gate. `registered` (default) admits a
group that is registered, observed, owned by a ready clean epoch, with a valid
scope, no `group_disabled`, no `group_cleanup_unknown`, a configured qualification
string and passing provider qualification. `allowlist` keeps the previous strict
rule (JID must also be in `EnabledGroups`). In registered mode `EnabledGroups` is
ignored (documented, not a second deny list). `MaxGroups` (default 50, ceiling 500)
replaces the old ten-entry bound: groups that own a clean epoch are ranked by
registration time and only the first `MaxGroups` are admitted, and a new epoch is
not prepared beyond the cap. Preparation is automatic: `/register` of a group, a
startup pass (after queue recovery and the scheduler are up), and the first denied real
turn of an unready group (never IPC polling or host checks) run the same
resumable Prepare and Enable steps under the runner chat slot, single-flight per
group with a one-minute failure backoff; an unobserved group waits silently. A stopped
legacy container of the namespace is removed (running ones are never touched and are
named, sanitized, in the private owner note). The last enabling write is a store CAS
that refuses a group carrying the owner marker. They never delete data, never stop
foreground work, and a failure leaves the group denied with a private owner notice
(rate limited). `/isolation disable <id>` is the per-group kill switch: it sets an
owner marker next to the SQL CAS flag, so automatic preparation never re-enables
it; `/isolation enable` clears it. `/isolation status` reports the mode, the cap
and a short reason per group. A refused group turn (not admitted, provider login
missing or expired, usage limit) shows the group one neutral line, rate limited,
without buttons or provider detail; the owner receives the state privately.

**Threat-model delta.** Before: only groups named in server settings could cause
agent runs. Now: any member of a group the owner registered can cause runs. They can
prompt the model and spend the inference-only subscription allowance. They still
cannot reach personal data, personal credentials or the host: the native account
reference stays inference-only, tools run offline and credential-free, memory and
IPC are scoped to the group namespace, images are immutable, and epoch, namespace and
CAS invariants plus drain and cleanup rules are unchanged. Abuse controls: usage-limit
handling, per-group disable, `MaxGroups`, and refusal-notice rate limiting.

**Remains gated.** Provider qualification is unchanged: the expired Claude auth must be
refreshed by the owner, the Codex tree-budget fix is separate work, and live two-turn
canaries on the Pi are still required. This change does not enable any group in
production by itself.

**Consequences.** The explicit enable step is no longer required for new groups; the
owner's remaining controls are `/register` (grant), `/isolation disable` (revoke) and
the provider qualification. A production `group-runtime.json` needs no new keys
(absent `AdmissionMode` means registered); stale `EnabledGroups` entries are harmless.

## D048 — Group trigger modes, owner-in-group identity and group sign-in guidance

**Date:** 2026-10-29
**Status:** Accepted (owner decisions 2026-10-29; spec
[group trigger and owner-in-group](superpowers/specs/2026-10-29-group-trigger-and-owner-in-group.md))

**Context.** After D047 every message Telegram delivered in a registered group became an agent turn, the
owner's own messages in a group were `owner: no` (ownership is by chat, D006), owner commands from a group
were denied, and group `/login` ended in a generic failure.

**Decision.**
1. *Trigger mode per registered group:* `owner`, `mention`, `owner+mention`, `all`, stored in the new
   `group_triggers` table, keyed by chat and bound to the registration's `namespace_id` (it survives epoch
   rotation; a re-registration into a new namespace falls back to the default). A registration without a row
   (every group registered before this change, including the existing one) behaves as `all`; a newly
   registered group is seeded with `mention` in the same transaction as its registration. The gate runs in the
   message handler before a turn is enqueued. A message that does not match is stored in the message history
   (nothing reads that history back into a prompt today, so unaddressed messages cannot inject into the agent;
   injecting history later needs its own decision) and is otherwise ignored: no turn, no model call, no quota
   use, no notice. A command addressed to another bot (`/cmd@other`) is ignored in every mode. In the gated
   modes a bare `/cmd` from a member is ignored (the owner's bare commands and `/cmd@thisbot` work) and a
   member's unknown command gets no reply; `all` keeps today's behavior. An album is decided once, when it is
   flushed: it is one turn if any item qualifies, else dropped. While the bot username is unknown (getMe has not
   succeeded; it is resolved with a bounded retry before polling) the `mention` gate treats messages with a
   mention entity or a reply to a bot as addressed. A registration lookup error fails closed. Private chats and
   unregistered chats are untouched. `mention` means a `mention` entity naming
   the bot username, a `text_mention` of the bot id, a command addressed to it, or a reply to one of its
   messages; forwarded text never addresses the bot. `owner` answers only the owner's own messages
   (strict). `/trigger [mode]` goes through `internal/control`; without an argument it shows the mode and
   states that gopod never changes Telegram privacy mode or admin rights, so `all` and `owner` only work for
   messages Telegram actually delivers.
2. *Owner-in-group identity:* `message.From.ID == GOPOD_OWNER_CHAT_ID` in a registered group, never
   `sender_chat` (anonymous admin, channel posts), forwarded messages or bots. It is carried as
   `Caller.IsGroupOwner` and satisfies only the new `PermGroupOwner` (and the `owner` trigger). It never sets
   `IsOwner`, the owner tier, personal mounts or memory, and `PermOwnerOnly` is unchanged.
3. *Group-scoped commands for the owner:* `/trigger`, `/model`, `/provider` (this group only), `/isolation
   status|disable` and `/help` run at once; `/isolation enable|prepare` ask for confirmation in the owner's
   private chat (the buttons are bound to that chat; a Confirmed flag arriving from the group is ignored).
   `/logs`, `/restart`, memory, schedules, `/telegram doctor`, backup and every other `PermOwnerOnly` command
   stay private-chat only. In a group `/provider` and `/model` are now limited to the owner; other members were
   previously allowed by `PermChatLocal`.
4. *Group sign-in:* the private confirmation of D-login is unchanged. When the confirmed group flow finds no
   group profile (groups have no sign-in of their own; they read the owner's account), the owner gets the real
   step privately (`/provider <name>`, then `/login` in the private chat) and the group one neutral line.
5. *The agent* receives one host line above every group turn, `[gopod group: trigger=<mode>;
   sender=owner|member]`, and the restricted instructions say only that line is host-written and members other
   than the owner are untrusted.

**Consequences.** Production needs no configuration. The existing group keeps `all` until the owner sends
`/trigger <mode>`; new groups default to `mention`, which with privacy mode on is what Telegram delivers
anyway. Existing workspaces keep their seeded instruction files (never overwritten); the per-turn line
carries the mode for them. Rollback is safe: the table is additive and an older binary ignores it.

## D049 — Group `/login` is a private redirect, not a confirmation

**Status:** accepted 2026-10-30. Supersedes item 4 of [D048](#d048--group-trigger-modes-owner-in-group-identity-and-group-sign-in-guidance).

**Decision.** Groups have no sign-in of their own, so the owner's `/login` in a registered group (and the group
login button) no longer asks "Authenticate group?". It sends the owner the real step in the private chat and the
group one neutral line, and starts no login CLI. The confirmation tokens, `login.group.confirm` and the
group-scoped login path (`runLoginTarget` with a confirmed group) are removed, so no code can run a login
against a group folder from the owner's private chat. Commands addressed to another bot are ignored; redirects
are limited to one per group per 30 seconds (in memory).

**Consequences.** Old confirmation buttons in the owner's private chat stop working (unknown command). Groups
cannot be given an account of their own without a new ADR.


## D050 — Markdown tables render as aligned monospace, wide ones as row cards

**Status:** accepted 2026-10-30. Refines the table note of D045 and the 2026-10-25 table research.

**Decision.** `renderMarkdown` (so `markdownToTelegramHTML` and `finalReplyHTML`) detects a GFM table outside code
fences: a header line with a pipe, a separator line of `-`/`:` cells with the same cell count, then pipe rows (a line with more cells than the header ends the table and stays plain text, so no cell is dropped). A
sentence with pipes or a pipe line without a separator is not a table. A table of at most `tableMaxWidth` (38)
display columns becomes one `<pre>` block: cells HTML-escaped, inline markdown stripped to plain text, columns padded
by display width (CJK and emoji 2), `:--:`/`--:` alignment honored, `\|` and pipes in code spans do not split cells.
A wider table with data rows becomes row cards: per row `<b>Header</b>: value` lines, rows separated by a blank
line (lossless; empty cells omitted). `chunkHTML` already closes and reopens `<pre>` and prefers line breaks, so a
long table splits into valid chunks on row boundaries.

**Consequences.** Streaming previews stay plain text; the table appears on the final edit. Command paging
(`splitPages`) cuts on lines, so a table split across pages loses its header on the later pages, which then show
as plain pipe lines (valid HTML). Native Rich Tables remain a separate spike.
