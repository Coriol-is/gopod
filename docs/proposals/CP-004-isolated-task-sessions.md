# CP-004 — Scheduled tasks run in their own session

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-05 |
| Origin | feature survey of `rzaytsev/codex-agent` (2026-10-05) and the 2026-10-03 public-release audit |
| Size | S–M |
| Touches | `internal/scheduler`, `internal/runner` (session selection per turn), `internal/queue.Item` (session hint) |
| Depends on | DT (done) |
| Spec | `—` |

## Problem
Scheduler tasks are enqueued like user messages and run through `RunStream`, i.e. `claude --continue` / `codex exec resume --last` in the chat's main session. Every cron run lands in the owner's conversation history, pollutes context, and can trigger auto-compact. `codex-agent` gives each job its own thread with its own effort profile.

## Proposal
A `session` hint on `queue.Item` (`main` | `task:<id>`). For `task:*` the runner uses the provider's fresh-session command (`RunFreshCmd`, or a named/persisted thread when the provider supports it) with a task-scoped working set, and optionally a lower effort/timeout profile. Output still goes to the chat via the existing silent/IPC path.

## Out of scope
A general worker pool (the queue's global cap already bounds concurrency). Per-task persistent memory.

## Risks
Tasks that *want* conversation context (e.g. "summarise today") lose it; the task prompt must say what it needs, or a `session: main` opt-in stays available.

## Acceptance
- A cron task runs and the next user message in the main session shows no trace of it in the transcript.
- `task_run_logs` records which session kind ran.
- Opt-in `session: main` still works for tasks that need the conversation.
