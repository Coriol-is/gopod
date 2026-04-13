# gopod — Memory architecture

> Companion docs: [ARCHITECTURE.md](ARCHITECTURE.md) · [INTEGRATIONS.md](INTEGRATIONS.md) · [DECISIONS.md](DECISIONS.md) · [GLOSSARY.md](GLOSSARY.md)

A native, layered memory system. The agent gets multiple memory surfaces, each
optimized for a different access pattern, all backed by the same SQLite file
plus a vector index. No external service required, no managed dependency,
fully offline-capable.

---

## 0. Why layered, not "just one thing"

Real assistants need at least four kinds of memory and they have very different
read/write patterns:

| Layer | Pattern | Latency budget | Volume | Best fit |
|---|---|---|---|---|
| **Working memory** (last N msgs) | Read every turn, write every turn | < 5ms | KB per chat | SQL `messages` table (already exists) |
| **Identity / preferences** | Read every turn, write rarely | < 5ms | < 10 KB per chat | Plain text file (`CLAUDE.md`) |
| **Scratchpad** (agent's notes) | Read & write often, narrow scope | < 50ms | KB–MB per chat | Anthropic Memory Tool over a directory |
| **Long-term semantic memory** | Read on demand via search, write occasionally | < 200ms | up to ~100K items per user | sqlite-vec |

Trying to collapse these into one store is what makes most "memory libraries"
either too slow (vector search on every turn) or too lossy (chat history
getting truncated into one summary). gopod keeps them separate and lets the
agent route between them through tools.

A fifth layer — **identity reasoning** (Honcho-style "what does this user
care about over time?") — is designed-in but **not implemented in v0**. See §8.

---

## 1. The five layers

### Layer 0 — Working memory (already in store)

- **Where:** `messages` table in `data/store.sqlite`.
- **What:** raw Telegram messages, timestamped, per chat.
- **How agent sees it:** as the formatted `<messages>` envelope at the top of
  every prompt (last `MAX_MESSAGES_PER_PROMPT`, default 10).
- **Lifecycle:** never deleted by gopod. Optional retention policy later
  (e.g. drop > 1 year old) but not in v0.
- **Code:** `internal/store/messages.go` (already in M0).

### Layer 1 — Identity & preferences (CLAUDE.md)

- **Where:** `chats/<folder>/CLAUDE.md`. Mounted into the container at
  `/workspace/chat/CLAUDE.md`.
- **What:** assistant name, communication style, hard preferences ("speak
  Russian", "never suggest meetings before 10am"), per-chat persona.
- **How agent sees it:** Claude Code automatically loads this on every
  invocation (it's the project memory file convention).
- **Write path:** the agent edits it directly with the built-in `Edit` tool.
- **Why keep this even with vector search:** it's the only memory surface that
  Claude reads **automatically** on every turn without spending tools/tokens.
  This is where the highest-signal, smallest-volume facts live.
- **Templated at chat registration** with the same substitution NanoClaw uses
  (`{ASSISTANT_NAME}` etc.).

### Layer 2 — Scratchpad (Anthropic native Memory Tool)

Anthropic released a **native Memory Tool** (public beta, 2026) plus context
editing. Combined they give +39% on long-running tasks. gopod uses it.

- **Where:** `chats/<folder>/memory/` on the host, mounted RW into the
  container at `/workspace/memory/`.
- **What it is:** a virtual filesystem the agent manipulates via `memory_*`
  tools provided by Anthropic. Files survive between turns and between
  container runs.
- **Use cases:** to-do lists, in-progress reasoning chains, multi-step task
  state, plans the agent wants to come back to next session.
- **Implementation:** the Anthropic Go SDK (`anthropic-sdk-go`) and the
  character-ai SDK both expose memory-tool definitions. We accept the tool
  calls, route them to the chat folder, and return responses. Permissions:
  agent in chat X can only touch `chats/X/memory/`.
- **Backup:** `chats/<folder>/memory/` is a normal directory — `tar`-able,
  inspectable, no opaque format.

This replaces both NanoClaw's "agent writes notes into the workspace" pattern
and the manual `/compact` archiving. Pre-compact hooks become unnecessary.

### Layer 3 — Long-term semantic memory (sqlite-vec)

This is the new layer. Backed by `sqlite-vec`, lives in the same
`data/store.sqlite` file, retrievable by similarity from any chat.

#### Schema

```sql
-- Relational metadata. One row per memory item.
CREATE TABLE memories (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_folder   TEXT NOT NULL,           -- '_global' for cross-chat
  kind          TEXT NOT NULL,           -- see §3 'kinds'
  source        TEXT,                    -- 'auto', 'agent', 'user', 'import'
  title         TEXT,                    -- short, human-readable label
  content       TEXT NOT NULL,           -- the indexed text
  ref_chat_jid  TEXT,                    -- when ingested from a chat message
  ref_msg_id    INTEGER,                 -- nullable
  meta_json     TEXT,                    -- arbitrary JSON sidecar
  embed_model   TEXT NOT NULL,           -- which model produced the vector
  created_at    INTEGER NOT NULL,        -- unix ms
  updated_at    INTEGER NOT NULL,
  pinned        INTEGER NOT NULL DEFAULT 0  -- pinned items survive culling
);
CREATE INDEX idx_mem_chat_kind ON memories(chat_folder, kind, created_at);

-- Vector index (sqlite-vec virtual table). Rowid matches memories.id.
CREATE VIRTUAL TABLE memory_vec USING vec0(
  embedding float[1024]                  -- dim depends on model; configurable
);

-- Optional FTS5 for hybrid search (BM25 + dense).
CREATE VIRTUAL TABLE memory_fts USING fts5(
  content,
  content='memories',
  content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);

-- Triggers keep FTS in sync (standard sqlite-fts5 contentless pattern).
CREATE TRIGGER mem_ai AFTER INSERT ON memories BEGIN
  INSERT INTO memory_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER mem_ad AFTER DELETE ON memories BEGIN
  INSERT INTO memory_fts(memory_fts, rowid, content) VALUES('delete', old.id, old.content);
END;
CREATE TRIGGER mem_au AFTER UPDATE ON memories BEGIN
  INSERT INTO memory_fts(memory_fts, rowid, content) VALUES('delete', old.id, old.content);
  INSERT INTO memory_fts(rowid, content) VALUES (new.id, new.content);
END;
```

Three things to call out:

1. **Single file.** Memories live in `data/store.sqlite` next to chats,
   messages, tasks. No second database. Backup = copy one file.
2. **Embedding dim is configurable** at first-run time and frozen thereafter.
   Switching providers means re-embedding everything (`memory_reembed` admin
   tool, owner-only).
3. **Hybrid search out of the box.** `memory_vec` for semantic similarity,
   `memory_fts` for exact lexical matches. Tool implementation does
   reciprocal-rank-fusion of both. This handles "I remember the word
   *quaternion*" as well as "find anything about rotations".

#### Kinds

A small enum of `kind` strings keeps the memory structured:

| kind | What it is | Who writes it |
|---|---|---|
| `fact` | Atomic statement about the user/world | Agent or user |
| `preference` | Stable preference / opinion | Agent |
| `conversation_summary` | Auto-rolled summary of N messages | Auto-ingest |
| `decision` | Decision the user committed to | Agent |
| `note` | Free-form note from the user | User (`/remember ...`) |
| `document` | Imported text (PDF, paste) | Import path |
| `task_outcome` | Result of a scheduled task run | Scheduler |

Querying can filter by `kind`, e.g. `memory_search("python", kind="decision")`.

### Layer 4 — Cross-chat shared memory

Same `memories` table with `chat_folder = '_global'`. Visible to **all** chats
on read; writable only by:

- The owner chat (any agent action there).
- Any chat, when the user explicitly types `/remember --global ...`.
- The auto-ingestion pipeline, never (it always writes per-chat).

Search ranking gives `_global` items a small boost so they don't get drowned
out by per-chat items at the same score. Configurable.

---

## 2. Embedding provider

Pluggable behind `internal/memory.Embedder`:

```go
type Embedder interface {
    Model() string                  // identifier baked into rows
    Dim() int                       // dimensionality (must match vec0 schema)
    Embed(ctx context.Context, texts []string) ([][]float32, error)
}
```

Three implementations in v0:

| Provider | Model | Dim | Cost | Notes |
|---|---|---|---|---|
| **OpenAI** | `text-embedding-3-small` | native 1536, default 1024 (server-side `dimensions` truncation, MRL-trained) | $0.02/M tok | Default. We already require `OPENAI_API_KEY` for Whisper, no new dep. See [D016](DECISIONS.md). |
| **Voyage** | `voyage-3-lite` | 512 | $0.02/M tok | Anthropic-recommended for Claude pairings. Requires `VOYAGE_API_KEY`. |
| **Ollama** | `nomic-embed-text` (768) or `bge-m3` (1024) | varies | free | Fully offline. Requires Ollama running locally. |

Selection via `GOPOD_EMBEDDING_PROVIDER=openai|voyage|ollama` and
`GOPOD_EMBEDDING_MODEL=...`. Default: OpenAI `text-embedding-3-small` at
1024 dim (using `dimensions` truncation, supported by the API).

Switching providers later: `gopod memory reembed` rebuilds the `memory_vec`
table from existing `memories.content` rows. Cheap on personal scale.

---

## 3. Tools exposed to the agent

All tools are host-side handlers; the agent never touches the DB directly. A
chat's tool calls are scoped to its own folder unless the chat is the owner.

```
memory_search(query: string, k: int = 5, kind?: string, scope?: 'chat'|'global'|'all')
  → [{id, kind, title, content, score, created_at}, ...]

memory_add(content: string, kind: string = 'fact', title?: string,
           scope?: 'chat'|'global', meta?: object)
  → {id}

memory_get(id: int) → memory
memory_list(kind?, since?, limit=20) → [memory, ...]
memory_update(id, content?, title?, kind?, pinned?)
memory_delete(id)

memory_summarize_chat(since?: timestamp, kind: string = 'conversation_summary')
  → {id, content}    # owner-callable convenience

memory_reembed(model?: string)        # owner-only admin
```

Implementation notes:

- `memory_search` runs **hybrid**: top-K from `vec0 MATCH ?`, top-K from
  `memory_fts MATCH ?`, then RRF-merge. Returns at most `k` items with
  combined score. Falls back gracefully if FTS or vec0 is empty.
- `memory_add` embeds synchronously and writes both rows in one transaction.
- All `memory_*` calls are logged to `task_run_logs` with `kind='memory_op'`
  so the user can audit what the agent stored.
- Per-chat scoping is enforced **inside the host handler**, not from the LLM
  output — same authorization-by-path discipline as the IPC layer.

---

## 4. Auto-ingestion pipeline

Three triggers, all opt-in via config:

### 4.1 Every N messages (default ON)

`GOPOD_AUTO_SUMMARIZE_EVERY=20` (default).

After every 20 new user messages in a chat, the runner schedules a low-priority
follow-up: it asks Claude to produce a short summary of those messages plus the
previous summary, and stores it as `kind='conversation_summary'`. This rolls
up working memory into searchable long-term memory automatically.

The summary prompt is small and runs on the cheap model
(`GOPOD_SUMMARIZE_MODEL=claude-haiku-4-5`) to keep cost minimal.

### 4.2 On `/compact` (default ON)

When the user invokes the `/compact` slash command (see Tier 1 integration #8),
the same summarizer runs immediately on everything since the last summary,
then triggers Anthropic's context-editing flow. Optional argument `--save`
also drops a `note` memory tagged with the user's free-form label.

### 4.3 On agent decision (always ON)

The agent can always call `memory_add` mid-conversation. This is the highest-
quality path because the agent decides what's worth keeping.

### 4.4 What's deliberately *not* auto-ingested

- Raw messages (already in `messages` table; ingesting them again would be
  duplication)
- Scheduled-task outputs (the scheduler decides per-task whether to call
  `memory_add` itself)
- Files in the Memory Tool scratchpad (those are the agent's, hands off)

---

## 5. Retrieval contract — when do we *use* the long-term memory?

Two modes:

### Mode A — Agent-initiated (always on)

The agent has `memory_search` in its tool list and decides when to call it.
This is the cleanest model: the LLM knows when it needs to remember.

### Mode B — Pre-prompt injection (opt-in via `GOPOD_AUTO_RECALL=1`)

Before sending a turn, gopod runs `memory_search(last_user_msg, k=3,
scope='all')` and injects the results into the system prompt as
`<recalled_memories>...</recalled_memories>`. Cheap insurance for chats where
the agent forgets to call the tool. Costs an embedding call per turn.

Default: **off**, because Mode A + the always-loaded `CLAUDE.md` cover most
cases. Flip on for chats where recall matters more than latency/cost.

---

## 6. Module layout

```
internal/memory/
├── memory.go         # Store interface, types, RRF merge
├── sqlite.go         # ncruces/go-sqlite3 + sqlite-vec implementation
├── schema.go         # CREATE TABLE / triggers
├── embed.go          # Embedder interface
├── embed_openai.go
├── embed_voyage.go
├── embed_ollama.go
├── tools.go          # Claude tool definitions and dispatchers
├── ingest.go         # Auto-summarize-every-N pipeline
├── recall.go         # Optional Mode B pre-prompt injection
└── memory_test.go
```

`internal/runner` calls `memory.Tools(chatFolder)` to get the per-chat tool
set and registers them with the character-ai SDK's `ToolRegistry` before each
agent invocation.

---

## 7. Configuration

```
# Memory
GOPOD_MEMORY_ENABLED=1
GOPOD_EMBEDDING_PROVIDER=openai            # openai|voyage|ollama
GOPOD_EMBEDDING_MODEL=text-embedding-3-small
GOPOD_EMBEDDING_DIM=1024                   # frozen after first run
GOPOD_AUTO_SUMMARIZE_EVERY=20              # 0 = off
GOPOD_SUMMARIZE_MODEL=claude-haiku-4-5
GOPOD_AUTO_RECALL=0                        # Mode B
GOPOD_AUTO_RECALL_K=3
GOPOD_GLOBAL_SCORE_BOOST=0.05              # tiny bump for _global hits
GOPOD_MEMORY_RETENTION_DAYS=0              # 0 = keep forever
```

---

## 8. Honcho? (deferred, not in v0)

Honcho is a strong product (Honcho 3, $2/M tokens, peer/session/message model,
tiered reasoning levels). It excels at **identity reasoning** — modeling what
a user cares about over time across many sessions. gopod's layered model
gives us:

- **Working memory** (Layer 0)
- **Identity facts** (Layer 1, written by hand or by agent)
- **Active scratchpad** (Layer 2)
- **Lexical + semantic recall** (Layer 3)

…but **does not** do background reasoning over the user's identity. Honcho
would slot in as Layer 5 if/when gopod needs it:

- New tools `honcho_query(question)` and `honcho_observe(text)`.
- Auto-observe: pipe every user message to Honcho in the background, get
  back identity context on demand.
- Backed by either the managed service or a self-hosted instance (they
  publish a docker-compose).

**Why not in v0:** it's an external service (network dep, account, billing).
The local stack already covers ~80% of the practical use cases for a personal
assistant, and we can layer Honcho in later behind a single env var
(`HONCHO_API_KEY`) without disturbing anything else.

**Decision recorded:** designed-in, deferred.

---

## 9. What we get vs NanoClaw today

| Capability | NanoClaw | gopod with this design |
|---|---|---|
| Last N messages | ✅ | ✅ (same) |
| Per-chat persona file | ✅ (`groups/X/CLAUDE.md`) | ✅ (`chats/X/CLAUDE.md`) |
| Agent scratchpad | partial (workspace files) | ✅ (Anthropic Memory Tool, native) |
| Cross-session recall | manual (agent reads workspace) | ✅ (semantic + lexical search) |
| Auto-summarization | partial (pre-compact archive) | ✅ (every N msgs + on `/compact`) |
| Cross-chat shared memory | partial (`groups/global/`) | ✅ (`_global` scope, ranked) |
| Identity reasoning over time | ❌ | ❌ (deferred — Honcho hook designed-in) |

---

## 10. Implementation milestones (slot into ARCHITECTURE.md M9 / [ROADMAP](../ROADMAP.md))

| Step | What | Notes |
|---|---|---|
| **M9.1** | Schema + `internal/memory.Store` over sqlite-vec | Smoke test: insert, search, delete |
| **M9.2** | OpenAI embedder + `EMBEDDING_DIM` freeze on first run | One provider is enough to ship |
| **M9.3** | `memory_*` tools registered with the agent | Per-chat scoping enforced host-side |
| **M9.4** | Hybrid RRF search (vec0 + FTS5) | Verify both branches degrade gracefully |
| **M9.5** | Anthropic Memory Tool wired to `chats/<X>/memory/` | The Layer-2 scratchpad |
| **M9.6** | Auto-summarize every N messages | Opt-in via env, cheap model |
| **M9.7** | `/remember`, `/recall` slash commands | Human entry points |
| **M9.8** | Voyage + Ollama embedders | Behind `GOPOD_EMBEDDING_PROVIDER` |
| **M9.9** | `gopod memory reembed` admin command | For provider switches |
| **M9.10** | Optional Mode B pre-prompt recall | `GOPOD_AUTO_RECALL=1` |

Honcho integration is a separate post-v0 milestone.

---

## 11. Sources

- [asg017/sqlite-vec](https://github.com/asg017/sqlite-vec) — pure C, successor to sqlite-vss
- [The State of Vector Search in SQLite — Marco Bambini](https://marcobambini.substack.com/p/the-state-of-vector-search-in-sqlite)
- [sqlite-vec-go-bindings](https://github.com/asg017/sqlite-vec-go-bindings) — official Go bindings (CGO + ncruces WASM)
- [Using sqlite-vec in Go — Alex Garcia](https://alexgarcia.xyz/sqlite-vec/go.html)
- [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) — pure-Go (WASM) SQLite driver, full sqlite-vec support
- [Anthropic Memory Tool docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool)
- [Managing context on the Claude Developer Platform](https://www.anthropic.com/news/context-management) — context editing + memory tool, +39% perf
- [plastic-labs/honcho](https://github.com/plastic-labs/honcho) — identity reasoning memory framework
- [Honcho 3 announcement](https://blog.plasticlabs.ai/research/Benchmarking-Honcho)
- [OpenAI Embeddings — text-embedding-3-small](https://platform.openai.com/docs/guides/embeddings)
- [Voyage AI — voyage-3-lite](https://docs.voyageai.com/docs/embeddings)
- [Ollama embeddings — nomic-embed-text, bge-m3](https://ollama.com/library/nomic-embed-text)
