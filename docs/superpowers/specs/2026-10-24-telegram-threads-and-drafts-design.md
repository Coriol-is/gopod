# Telegram threads and native streaming: design

Status: draft for owner approval. Date: 2026-10-24.

## Intent

gopod's Telegram chat should feel closer to the Claude / Codex chat UI. The
owner's headline wish: **several parallel conversations, each with its own
context**, like a chat-UI sidebar. A second wish, implied by "closer to chat
UI": streaming that looks and behaves like one (no "edited" flicker, a Stop
button).

Telegram's Bot API now supports both (checked against the changelog, Bot API
10.3, 2026-08-24):

- **Topics in private chats** (9.3, 9.4): `message_thread_id` on messages and on
  most send methods, `createForumTopic` in private chats, `has_topics_enabled`
  on `User`. Users create and switch topics in the Telegram UI.
- **`sendMessageDraft`** (9.3, open to all bots since 9.5): streams a partial
  message to the user. 10.3 adds `can_stop` / `keep_on_stop` and a
  `MessageGenerationStopped` update.

`go-telegram/bot` v1.27.0 supports Bot API 10.3. gopod pins v1.20.0.

## Decisions by the owner

- Messages sent **outside any thread** keep today's behaviour: one shared
  context for the chat.
- A **separate context exists only when a thread is created.** Each thread is
  its own conversation.
- Parallel conversations are the priority.
- **Long-term memory is shared across conversations, through the shared
  memory provider** (gopod's own memory store: facts, preferences, pinned
  items). The owner stated this is conditional on using that shared provider:
  memory that lives only inside a harness (Claude's or Codex's own session
  state) is per conversation, because that is part of the session.

## Decisions by me (owner to confirm or reject)

1. **Shared workspace.** All conversations of a chat share one container, one
   workspace (`chats/<folder>`) and `CLAUDE.md`. Only the conversation history
   (the agent session) and the conversation summary are per thread.
   Rationale: cheapest, matches "same project, different conversations".
2. **Threads run sequentially per chat by default; parallel execution is an
   optional follow-up** (see the owner's fallback and research track above).
   If it is built, serialization moves from per chat to per (chat, thread)
   and the global concurrency cap stays.
3. **Private chats only.** Forum-style group topics are out of scope.
4. **Order: drafts first (Phase A), threads second (Phase B).** Both rewrite
   the streaming and send paths; doing drafts first avoids wiring thread ids
   into code that is about to be replaced. Owner can swap the order.
5. **Scheduled tasks and IPC messages stay in the main conversation** unless
   the builder finds a cheap, safe way to attribute them (see Phase B).

## Decided by the owner after the builder check

- **Fallback if concurrent agents in one container conflict:** threads of a
  chat take turns (sequential per chat, one container). No container per
  thread. Parallelism is built only if the early concurrency spike passes on
  both harnesses; otherwise the queue keeps per-chat serialization and threads
  still have separate contexts.
- **Provider is per chat.** One provider for every conversation in the chat;
  no per-thread provider selection.

- **Parallelism is an optional research track.** Phase B ships with separate
  contexts and per-chat sequential execution. Parallel threads are a separate,
  later item, taken up only if the concurrency spike is convincing. If it
  proves awkward or fragile in use, it is marked "needs research" in ROADMAP
  and left out; that does not block Phase B.

## Calls settled after the builder check (mine; owner to veto)

- **Memory scope.** Facts, pinned items and preferences stay chat-wide and are
  injected into every conversation exactly as today. Only the conversation
  summary slot is per conversation.
- **`/provider` and `/model` are per chat, not per thread.** They apply to
  every conversation in the chat. `/clear`, `/compact` and the auto-compact
  counter are per conversation, and this holds in both directions: `/clear`
  in main wipes only main's session, never a thread's. (Overrides the earlier
  wording that `/model` and `/provider` act per conversation.) The session row
  records which provider created it so a switch does not resume a foreign id.
- **Stop.** The partial text is kept and delivered as the reply. The turn ends
  in a terminal `stopped` state and is never replayed. Whether the agent
  session can safely be resumed after a killed turn is verified by the builder
  on both harnesses; if the interrupted turn is simply absent from the
  session, that is acceptable.
- **Draft fallback.** If `sendMessageDraft` fails with an "unsupported" style
  error, the chat falls back to the edit path and stays there until restart.
  Transient errors retry. Phase A's recovery design is a gated spike: it must
  show no duplicate reply on `kill -9` before Phase B starts.
- **Main-session migration.** The mechanism is the builder's choice: adopting
  the latest session id, or keeping implicit resume for main only with
  per-conversation session isolation (for example a separate project
  directory). The requirement is only that main's existing history survives
  and is not displaced by thread sessions.
- **Deleted or closed topics:** no cleanup in this phase. Session rows and
  files stay.
- **Fairness:** the global concurrency cap stays first-come. Revisit if main
  gets starved in practice.

## Goals

- Messages in a thread get answers in that thread, with that thread's history
  and nothing else.
- Main chat context is untouched by thread activity, including after a restart,
  `/clear` in a thread, or `/compact` in a thread.
- Streaming uses `sendMessageDraft`; the user can press Stop and the turn is
  cancelled.
- Existing production data survives the upgrade: the current main-chat session
  keeps going.

## Non-goals

- Inline keyboards / callbacks (Regenerate, Copy, pickers). Separate spec later.
- Rich messages (`sendRichMessage`, tables, thinking/details blocks). Separate
  spec later; needs live verification in private chats.
- Forum topics in groups.
- Auto-naming topics (`editForumTopic`) from the first message.
- Per-thread workspace or shared-store memory isolation.
- Ephemeral messages, guest mode, managed bots, gifts, business, polls,
  checklists: irrelevant to a personal bot.

## Anti-goals (failure even if it "works")

- Main chat loses its history or starts a new session after the upgrade.
- A thread's `/clear` or `/compact` wipes main's session or another thread's.
- A thread's summary leaks into main's context compiler slot, or the reverse.
- Streaming regresses on a crash: durable turns (D020) must still resume
  without a second reply.

## Constraints (from CLAUDE.md and existing ADRs)

- Go only, no CGO, one SQLite file, all access through `internal/store`.
- Admin commands go through `internal/control`; authorization comes from the
  chat path, never from LLM-produced data. A thread id is routing inside an
  already-authorized chat, not authorization.
- No secrets in committed files. A new ADR (D021) for the conversation model.
- Both harnesses must work: Claude (`claude`) and Codex (`codex`).
- Do not commit without the owner's request.

## Phase A: library bump, native streaming, Stop

1. Bump `go-telegram/bot` to v1.27.0 (build, vet and tests green, behaviour
   unchanged).
2. Replace the placeholder-plus-`EditMessageText` loop in
   `internal/telegram/streaming.go` with `sendMessageDraft`. Keep the existing
   edit path as a fallback if the draft call errors, and keep
   `GOPOD_STREAM_DISABLED`.
3. Send `can_stop`. On `MessageGenerationStopped`, cancel the context of the
   matching in-flight turn. Use `keep_on_stop` so the partial text stays.
4. A stopped turn is a terminal state in `turns` and must never be replayed by
   `Recover`.

The builder must read the `sendMessageDraft` method page for exact semantics
(how a draft becomes a real message, what id the final message gets, lifetime
of a draft, what happens to a draft when the process dies) and adapt D020's
placeholder/reply-id memoization to it. The spec author has not read that page.

## Phase B: parallel conversations via threads

**Conversation key** is `(chat_folder, thread_id)`; `thread_id = 0` is the main
conversation. The container, mounts and workspace stay keyed by `chat_folder`.

**Session identity.**
- Today both providers resume implicitly (`claude --continue`,
  `codex exec resume --last`), which picks the most recently touched session.
  That breaks as soon as two conversations share one home directory.
- Each conversation stores an explicit session id and resumes by id: Claude via
  `--session-id <uuid>` on the first turn and `--resume <uuid>` after; Codex via
  `exec resume <thread id>` with the id read from the exec JSON events (the
  HANDOFF already lists "persist the Codex thread id per chat" as a follow-up).
- Storage: the existing `sessions` table (currently keyed by `chat_folder`
  alone) is extended to key by `(chat_folder, thread_id)` and to record the
  provider. The builder confirms whether the table is used anywhere today.
- **Migration:** on upgrade the main conversation must adopt the session that
  `--continue` would have picked, so nothing is lost. A thread that has no row
  starts a new session.

**Commands per conversation.** `/clear`, `/compact`, auto-compact turn
counters and the conversation summary act on the conversation the command was
sent in (`/model` and `/provider` stay per chat, see the settled calls). `ClearSession` currently deletes
`~/.claude/projects` wholesale; it must delete only that conversation's session.
Memory summary storage is scoped per conversation.

**Queue and turns.** Items carry `thread_id`. The queue serializes per
(chat, thread); the runner's per-chat lock (`lockChat`) must not serialize
threads. `turns` gains `thread_id`; durable-turn recovery replays into the
original thread.

**Send paths.** Every outbound send in the thread passes `message_thread_id`:
replies, drafts, voice, photos, documents, chat actions, reactions' parent
replies, error and login messages, and the IPC sink. Messages with no thread id
go out as today.

**Telegram prerequisite.** Topics in private chats must be enabled for the bot
in @BotFather (the builder confirms the exact setting). If it is off, no
message carries a thread id and gopod behaves exactly as today. gopod does not
create topics itself in this phase; the owner creates them in the Telegram UI.

**Scheduled tasks and IPC.** Tasks created from a thread and agent-initiated
IPC messages go to the main conversation in this phase, unless the builder
finds that attributing them is a small change; if so, the thread id travels as
routing data scoped to the already-authorized chat.

## Risks to verify early

- Two `claude` processes running concurrently in one container with one
  `$HOME` (`.claude.json` and project files). Needs a real concurrent test, on
  both harnesses, before the design is trusted.
- Memory-compiler slots (summary, pinned, etc.) currently assume one
  conversation per chat folder.
- Idle watcher and container recycling with several active conversations.

## Done means

- Unit tests for session keying, queue keying, migration, scoped clear/compact.
- A manual check on the production host: main chat and two threads hold three
  distinct contexts across a container recycle and a gopod restart; Stop works;
  `kill -9` mid-turn in a thread resumes in that thread without a duplicate.
- `go build ./... && go vet ./... && go test ./...` green. D021 written;
  HANDOFF and ROADMAP updated.
