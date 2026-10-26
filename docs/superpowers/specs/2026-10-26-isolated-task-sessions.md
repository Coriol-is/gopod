# Task-scoped native sessions and working sets

Date: 2026-10-26. CP-004. Status: **proposed design**, not implemented or enabled.
Planning wave: 1; size estimate: S–M.

[Proposal](../../proposals/CP-004-isolated-task-sessions.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

Scheduled execution currently risks sharing the native foreground session and polluting
interactive context.

## Dependencies and boundaries

Existing queue/admission; CP-018 resolution contract; CP-020 before group enablement.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Separate immutable task identity from Telegram thread identity: task:<id>/run:<id> is not a
  guessed thread integer. Each occurrence defaults to a fresh native session with bounded
  task-specific input and separately stored execution metadata.
- Resolve provider/model/profile and namespace at admission, preserve that binding for the run,
  and never change provider midway because a foreground setting changed. Missing
  grants/provider availability follow existing failure or confirmation policies; there is no
  owner/personal fallback.
- Task execution inherits the source conversation's authority, not the authority of the private
  chat that approved a login. Scope output to the original conversation/thread and CP-002
  visibility.
- Allow explicit session:main only when requested and safe under the foreground lock. Tasks
  needing history may receive an explicit bounded authorized snapshot without sharing the main
  native transcript. Task results remain inspectable; a concise delivered result may enter
  chat history without importing the task tool transcript.
- Keep current concurrency/admission limits; do not require a new worker engine or container per
  occurrence. General role expansion requires a separate approved capability definition.

## Acceptance

- Existing IPC-delivered tasks retain their delivery mode; moving to a fresh
  native session does not enable an extra controller stdout reply.
- Repeated cron runs leave the foreground native transcript free of task prompts/tool traces.
- Two conversations/tasks cannot resume each other's sessions; invalid task/thread aliases fail
  closed.
- Provider changes while queued resolve by a documented admission boundary and never redirect an
  already admitted run.
- Explicit main-session opt-in serializes correctly; restart and retirement preserve output
  destination.

## Migration, retention and rollback

New schedules use the isolated default. Existing tasks receive an operator-visible migration
inventory; do not silently import main transcripts into task namespaces. Retain original history
and run logs.

## Risks and decisions before implementation

History-dependent tasks need explicit input selection; session isolation alone does not isolate
filesystem or credentials.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

A general worker pool, task-specific long-term memory or forced provider switching.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/agent.js; src/service.js; docs/conversations.md`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
