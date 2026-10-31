# CP-036 — Explicit Stop controls in the private chat

| | |
|---|---|
| Status | done |
| Date | 2026-10-31 |
| Origin | Owner correction: ordinary messages must not interrupt; add the group-style button and /stop |
| Size | M |
| Touches | internal/telegram, internal/control, cmd/gopod |
| Depends on | D020/D021/D022/D033 |
| Spec | [Private-chat Stop](../superpowers/specs/2026-10-31-private-chat-stop.md) |

## Problem

The private draft Stop depends on client support. The owner wants a persistent
inline Stop like the group response and a /stop command, independent of new
messages. Streaming setup fallback currently loses the stop context.

## Proposal

Use the existing edit response with an inline Stop in the owner private chat,
available before agent startup. Add /stop through the control Router, scoped to
the caller's private conversation. Both cancel the active answer; neither clears
history or resumes it automatically. Ordinary messages keep queue behavior.

## Out of scope

Automatic interruption/steering, queue clearing, group changes, scheduled-task
cancellation, process-death guarantees during Docker failure, external-action
rollback and deployment. CP-035 is reserved by the existing CLI-hooks proposal
in another worktree; this replaces the uncommitted steering draft from the audit.

## Risks

Existing TERM/KILL cleanup is best effort. Native session writes may be incomplete
if a run ends before the provider saves them. Stop does not retract IPC deliveries
or undo external actions. Already queued user messages can run after Stop.

## Acceptance

- Owner-private active/waiting answers show an inline Stop; terminal replies remove it.
- /stop cancels only the active answer in its private topic, bypassing the turn queue.
- Duplicate/stale stops are harmless; an idle /stop reports no active answer.
- New messages remain queued; stopping never creates a replacement turn.
- Buffered execution and streaming fallback obey the same cancellation context.
- History/session mappings are not cleared; native resumption retains its existing gates.
- Router authorization, topic isolation, UI lifecycle and cancellation have regression tests.

## Delivery

Merged by owner-authorized admin squash in [PR #124](https://github.com/Coriol-is/gopod/pull/124),
`8a3ce75113c0709c63ede99ed437fad4d8e191a0`. Build/vet/full and race tests passed;
PR test, isolation-spike and Sonar checks were green on final head `9dad3a5`.
The owner subsequently authorized deployment; [rollout completed](../deployments/2026-10-31-private-stop.md)
at 20:00:48 UTC. Real-client A08/A10 acceptance remains separate from implementation.
