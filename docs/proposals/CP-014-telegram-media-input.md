# CP-014 — Telegram input fidelity and media understanding

| | |
|---|---|
| Status | accepted |
| Date | 2026-10-25 |
| Origin | Owner supplied a Guest-mode bot card whose URL was lost, asked about forwarded videos and requested three delivery queues in specs |
| Size | L |
| Touches | future Telegram normalizer, attachment store/jobs, extraction, prompt/context and delivery |
| Depends on | D013, D020, D022, D026, D030; CP-013 |
| Spec | [Media input and three delivery queues](../superpowers/specs/2026-10-25-telegram-media-input.md) |
| Plan | [Implementation sequence](../superpowers/plans/2026-10-25-telegram-media-input.md) |

## Problem
Plain text/caption extraction drops link entities and keyboard URLs. Ordinary
Telegram videos are not decoded; their caption can reach the agent without the
video, and an uncaptioned video is ignored. A document path alone is not proof
its contents were extracted. This invites fabricated links and overclaimed analysis.

## Proposal
Deliver in three queues: input fidelity and truthful capability reporting;
document/image/speech analysis with source references; video timelines from
speech and selected frames, then derived materials and retrieval.

## Boundary
This accepts planning, not implementation of these media features, guest-mode
participation, a remote extraction service, new provider/runtime or paid calls.
Only CP-013's small transcript metadata change is implemented in the current PR.

## Acceptance
The spec and plan define current support, evidence provenance, separate guest
query namespaces, thread/chat boundaries, resource constraints, failure/Stop/
recovery semantics and measurable gates for each delivery queue.
