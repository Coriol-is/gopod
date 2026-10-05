# CP-006 — Maintenance yields to the user; quiet hours

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-05 |
| Origin | feature survey of `rzaytsev/codex-agent` (2026-10-05) and the 2026-10-03 public-release audit |
| Size | S |
| Touches | `internal/runner` (compact watcher, extraction), `internal/scheduler`, `internal/telegram/ipc_sink.go`, `internal/config` |
| Depends on | — |
| Spec | `—` |

## Problem
Auto-compact and memory extraction run in background goroutines regardless of what the user is doing; a compaction that starts just before a message delays the reply by a full summarisation turn. Scheduled tasks and IPC messages are delivered at any hour. `codex-agent` aborts maintenance when the user writes and holds proactive messages during quiet hours.

## Proposal
Maintenance turns (compact, extraction, future consolidation) take a cancellable context that the queue cancels when a user turn for the same chat is enqueued; they re-arm later. A `GOPOD_QUIET_HOURS=22:00-08:00` window (chat-local time from `GOPOD_TIMEZONE`) during which proactive deliveries (scheduler output, IPC messages not tied to a live turn) are queued and flushed at window end; direct replies to the user are never held.

## Out of scope
Per-chat quiet hours. Priority queues.

## Risks
Cancelling compaction mid-way must not corrupt the session; the compact command must be idempotent or re-runnable.

## Acceptance
- Send a message while a compaction is running: the reply starts within the normal spawn latency and compaction completes afterwards.
- A task due at 23:30 is delivered at 08:00 with its original timestamp in the text.
- A user message at 23:30 is answered immediately.
