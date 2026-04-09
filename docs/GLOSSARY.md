# picoclaw — Glossary

Project-specific vocabulary. When you find yourself defining a term in another
doc, add it here instead and link.

## A

**ADR** — Architectural Decision Record. One entry in
[DECISIONS.md](DECISIONS.md). Append-only.

**Agent runner** — In NanoClaw, a TypeScript program inside the container
that wraps the Claude SDK. picoclaw **does not** have one — the host Go
runner talks to the `claude` CLI directly via `docker exec`. See [D002](DECISIONS.md).

**Anthropic Memory Tool** — A native Anthropic tool (public beta, 2026) that
gives the agent a virtual filesystem for cross-session scratchpad memory.
picoclaw backs it with `chats/<folder>/memory/`. Layer 2 of the memory model.
See [MEMORY.md §1](MEMORY.md).

**Authorization by path** — picoclaw never trusts authorization data the LLM
emits. The chat that owns an action is determined by the filesystem path the
request came from. See [D007](DECISIONS.md).

## C

**Caller** — Origin of a control plane `Command`: `ChatID`, `UserID`,
`Source` (`telegram` or `cli`), and the computed `IsOwner` flag. Built by
the frontend, trusted by the Router. See [CONTROL.md §3](CONTROL.md).

**Chat folder** — A per-chat directory under `chats/<folder>/`. Validated by
the regex `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`, with `_global` reserved.
Holds `CLAUDE.md`, `memory/`, `skills.json`, optional `conversations/`.
Mounted into the chat's container at `/workspace/chat/`.

**Command** — Frontend-agnostic request flowing into `internal/control.Router`:
`Name`, `Args`, `Caller`. Telegram slash commands and CLI subcommands both
get translated into one. See [CONTROL.md §3](CONTROL.md).

**Control plane** — `internal/control` package. The single dispatch layer
for admin and diagnostic operations. Distinct from the data plane (user
message → agent → reply). See [CONTROL.md](CONTROL.md), [D011](DECISIONS.md).

**Container skill** — A skill type: a directory under `container/skills/<name>/`
with `SKILL.md` + optional scripts. Mounted into the agent container at
`/home/node/.claude/skills/<name>/` so Claude Code's native skill discovery
picks it up. Zero Go code, no recompile. See [SKILLS.md §1](SKILLS.md).

## D

**Dev skill** — A Claude Code slash command in `.claude/skills/<name>/` for
picoclaw maintainers (e.g. `/release`). Not loaded at runtime. See
[SKILLS.md §3](SKILLS.md).

## F

**Feature** (vs skill) — Anything that requires recompiling picoclaw. Lives
in an `internal/` package, conditionally enabled by env var. The boundary is
sharp: skill = no rebuild, feature = rebuild. See [D009](DECISIONS.md).

## G

**GroupQueue** — picoclaw's per-chat serialization + global concurrency cap
mechanism. Inherited from NanoClaw's `src/group-queue.ts`. Lives in
`internal/queue`. See [ARCHITECTURE.md §5.5](ARCHITECTURE.md).

## H

**Honcho** — A managed memory framework (plastic-labs) that does identity
reasoning over time. Designed-in as picoclaw memory Layer 5 but **deferred**
in v0. See [MEMORY.md §8](MEMORY.md), [D005](DECISIONS.md).

## I

**IPC namespace** — A per-chat directory under `data/ipc/<chat_folder>/`
with `messages/`, `tasks/`, `input/`, `_close`. The container writes
outbound messages and task ops as JSON files; the host watches and processes
them. Authorization is by path. See [ARCHITECTURE.md §5.3](ARCHITECTURE.md).

## L

**Layer N** — In the memory model, one of five surfaces: working memory (0),
identity / `CLAUDE.md` (1), Anthropic scratchpad (2), semantic
`sqlite-vec` (3), cross-chat `_global` (4). See [MEMORY.md](MEMORY.md).

## M

**MCP skill** — A skill type: a directory under `skills/mcp/<name>/` with
a `manifest.json` (command, args, env, scope). picoclaw spawns the MCP
server and registers its tools with the agent SDK at chat startup. The main
extension mechanism for adding new agent capabilities without recompiling.
See [SKILLS.md §2](SKILLS.md).

**Memory Tool** — See **Anthropic Memory Tool**.

## N

**NanoClaw** — The TypeScript predecessor to picoclaw, at
`/Users/<user>/_code/gh-public/nanoclaw`. The reference implementation
for architecture; the source of architectural ideas; **not** the source of
code. See [ARCHITECTURE.md §2](ARCHITECTURE.md).

## O

**Owner chat** — The single chat ID set in `PICOCLAW_OWNER_CHAT_ID`. Replaces
NanoClaw's per-group `is_main` flag. The only chat allowed to register new
chats, schedule tasks for others, mount the SQLite store RW, see the project
root. See [D006](DECISIONS.md).

## P

**Perm** — Permission level a control plane handler declares: `PermPublic`,
`PermChatLocal`, `PermOwnerOnly`. The Router checks `Perm` before invoking
the handler. See [CONTROL.md §4](CONTROL.md).

**Phase** — A grouping of milestones in [ROADMAP.md](../ROADMAP.md). Phase 1 =
core (M0–M9, including M3.5 control plane). Phase 2 = integrations
(I1–I10). Phase 3 = skill ecosystem (S*).

## R

**Router** — `internal/control.Router`: the central dispatch table for the
control plane. Owns the auth invariant (Perm enforcement). Frontends call
`Router.Dispatch`; handlers register via `Router.Register`. See
[CONTROL.md §3](CONTROL.md).

## S

**Scratchpad** — Layer 2 of memory: the Anthropic Memory Tool, backed by
`chats/<folder>/memory/`.

**Source (control)** — Where a `Command` originated: `telegram` or `cli`.
Frontends set this on the `Caller` they construct.

**Semantic memory** — Layer 3 of memory: `sqlite-vec` rows queried by
embedding similarity (and FTS5 for lexical). See [MEMORY.md §1](MEMORY.md).

**Skill** — Anything that extends picoclaw without recompiling it. Three
types: container skills, MCP skills, dev skills. See [SKILLS.md](SKILLS.md).

**`sqlite-vec`** — Vector search SQLite extension by Alex Garcia. Successor
to `sqlite-vss`. Used through `ncruces/go-sqlite3`'s WASM build. See
[D004](DECISIONS.md).

## T

**Trigger pattern** — A regex (default `@picoclaw`) that messages must match
to wake the agent in a non-owner chat. Per-chat configurable.

## W

**Working memory** — Layer 0 of memory: the last N rows from the `messages`
table, formatted into the prompt envelope every turn.
