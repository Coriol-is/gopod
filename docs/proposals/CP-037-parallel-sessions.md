# CP-037 — Parallel private conversations and background sessions

| | |
|---|---|
| Status | in-progress (P0 only) |
| Date | 2026-11-01 |
| Origin | Owner requested design only; private chats plus background tasks, topic folders plus shared projects, explicit per-thread separation |
| Size | L |
| Touches | queue, runner, store, control, task scheduling, mounts and recovery |
| Depends on | D022/D023/D026; CP-018, CP-004, CP-007 qualification |
| Spec | [Architecture and implementation contracts A/B/C/D](../superpowers/specs/2026-11-01-parallel-sessions.md) |
| Plan | [Staged delivery](../superpowers/plans/2026-11-01-parallel-sessions.md) |

## Problem
Native contexts already distinguish topics, but queue lanes, agent slots and
writable runtime homes are chat-wide. Independent topics cannot run concurrently.

## Proposal
Design optional resource-bounded parallel execution with durable per-topic and
task sessions, disposable dedicated runtimes and explicit shared-project access.
Owner authorized P0 implementation on 2026-11-01 with parallel worktrees, review,
acceptance and automatic merge. Later slices and deployment remain gated.

## Out of scope
Code, feature activation, deployment, host reboot, group redesign, local model
serving and multi-host orchestration.

## Risks
Shared writable homes/projects, credential refresh races, resource exhaustion,
uncertain process termination, incomplete backup and task execution qualification.

## Acceptance
- Reviewable design defines chat/thread/task identity, context/file persistence,
  scheduling, resource limits, project ownership, Stop, migration and recovery.
- Existing behavior and unresolved qualification gates are distinguished from proposals.
- Implementation and runtime remain unchanged.
