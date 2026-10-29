# CP-004 — Scheduled tasks run in their own session

| | |
|---|---|
| Status | in-progress |
| Date | 2026-10-05 |
| Origin | feature survey of `rzaytsev/codex-agent` (2026-10-05) and the 2026-10-03 public-release audit |
| Size | S–M |
| Touches | `internal/scheduler`, `internal/runner` (session selection per turn), `internal/queue.Item` (session hint) |
| Depends on | DT (done) |
| Spec | [Design](../superpowers/specs/2026-10-26-isolated-task-sessions.md) |
| Plan | [Task reliability plan](../superpowers/plans/2026-10-26-reliability-tasks.md) |

## Problem
Scheduler tasks are enqueued like user messages and run through `RunStream`, i.e. the explicit native session associated with main/thread 0 in the chat's main session. Every cron run lands in the owner's conversation history, pollutes context, and can trigger auto-compact. `codex-agent` gives each job its own thread with its own effort profile.

## Proposal
Managed task attempts use a dedicated ephemeral `RunTaskStream` path and a host-issued task/run/attempt namespace. They do not reuse the maintenance `RunFresh` timeout/preemption policy or foreground native sessions. Existing silent/IPC delivery remains the default; controller result publication is explicit.

## Out of scope
A general worker pool (the queue's global cap already bounds concurrency). Per-task persistent memory.

## Risks
Tasks that *want* conversation context (e.g. "summarise today") lose it; the task prompt must say what it needs, or a `session: main` opt-in stays available.

## Acceptance
- A cron task runs and the next user message in the main session shows no trace of it in the transcript.
- `task_run_logs` records which session kind ran.
- Opt-in `session: main` still works for tasks that need the conversation.

## 2026-10-25 reconciliation
CP-015 made schedules usable and observable, but did not isolate their execution
session. Task identity must be a separate namespace (task:<id>), never a guessed
Telegram thread integer. Coordinate effective provider resolution with CP-018;
this proposal does not require parallel containers or a new runtime first.

## 2026-10-26 adoption design

The linked spec now defines this candidate in the [adoption map](2026-10-26-codex-agent-adoption.md),
wave **1**. Status remains proposed; a saved design does not approve
implementation or change the existing delivery queue. New acceptance/migration
requirements in the dated spec supersede conflicting early shorthand here.

## 2026-10-26 source-backed refinement

The linked spec and plan now define the bounded initial Go slice, concrete
run/attempt identity, source/provider resolution boundary, crash/non-replay
policy and migration gates. Status stays proposed/design-only. Saving this plan
does not enable the feature, change live schedules or authorize implementation.
