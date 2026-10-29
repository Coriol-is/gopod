# Agent thread awareness and thread-targeted messages

Date: 2026-10-29. Status: **requirements draft; not implemented**. Separate spec requested by the owner
alongside [rich agent messages](2026-10-29-rich-agent-messages.md).

## Owner requirement
*(owner)* The agent must be able to send a message to a **specified thread** (Telegram topic) and must
**understand which thread the current conversation is in**.

## Current state (verified 2026-10-29)
- Conversation state is already per thread: sessions, history and native mappings are keyed by chat + thread
  (D022), and `ConversationScope` carries the thread.
- The agent is **not told** its thread: the chat templates and turn input do not name it (scheduled runs are
  documented as "main, thread 0").
- IPC message files are `{chatJid,text}`; there is no thread field. The durable IPC route admits only the
  owner's personal chat, **thread 0** (`durableDestination`), and thread 0 is a literal conversation, never a
  wildcard. The legacy sink sends to the chat without a thread.
- The outbox, dispatcher and transport already carry a thread per delivery (`ConversationScope`,
  `MessageThreadID` on send) and revalidate scope/thread before every attempt; they never fall back to the
  main topic.

## Requirements (first cut, *(guess)* unless marked)
1. **Awareness.** Every turn gives the agent its conversation identity in a stable form the message content
   cannot spoof: chat, `threadId` (0 = main) and whether the chat uses topics, delivered through the
   host-built turn context, not user text.
2. **Targeted send.** An IPC message may carry an optional `threadId` (integer >= 0). Omitted means "the
   conversation this run belongs to" for run-bound sends and thread 0 for scheduled/background sends, which
   preserves today's behavior.
3. **Authorization by path, not data.** The host decides which chats the sender may address from its IPC
   folder (CLAUDE.md); `threadId` only selects a topic **inside an already authorized chat**. The thread must
   exist and be open; unknown, deleted or closed topics are rejected with a receipt code, never redirected to
   the main topic.
4. **Durable route.** Durable IPC admission snapshots `(chat, thread, visibility)` through
   `SnapshotConversation` instead of hard-coding thread 0; stale or retired threads fail like stale scope.
5. **Isolation.** A message to a thread does not read or write another thread's history; shared memory behaves
   as today.
6. **Scheduled tasks.** A task records the thread it was created in (or an explicit target) so its output can
   return there; today it returns to main. Needs a task-schema field, gated on the CP-004 managed-task work.

## Non-goals
Groups and shared chats (closed by CP-020), creating or deleting topics (CP-016), cross-chat sends.

## Open questions
1. Should a run-bound send default to the originating thread (my default), even for tasks created from a topic?
2. Telegram has no topic-deletion update (see CP-016): rely on send-time refusal plus the existing
   retired-topic handling?
3. Must the agent be able to list known threads (ids and titles), or are the current thread plus explicit ids
   enough?
