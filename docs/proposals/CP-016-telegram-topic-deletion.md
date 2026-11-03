# CP-016 — Telegram topic deletion and conversation retirement

| | |
|---|---|
| Status | in-progress |
| Date | 2026-10-25 |
| Origin | Owner requested implementation specs for deleting topics and retiring sessions |
| Size | M |
| Touches | Telegram/control, conversation state and runner lifecycle as defined in the spec |
| Depends on | D011, D020, D022, D030, D031 |
| Spec | [Implementation specification](../superpowers/specs/2026-10-25-telegram-topic-deletion.md) |

## Problem
Current behavior and verified gaps are recorded in the linked specification.

## Proposal
Implement the bounded, scoped contract in the specification in separate delivery
slices. The owner resumed implementation on 2026-11-02 as conversation-management
workstream 3. The first slice implements the store journal, scoped retirement
transaction and continuity/admission write fences; see the
[foundation and remaining integration gates](../superpowers/plans/2026-11-02-topic-retirement-foundation.md).
The command and destructive real-client acceptance remain pending; later
checkpoints below record runtime/fencing implementation. No real Telegram topic
has been deleted by these slices.

## Out of scope
Enabling modes, deleting real topics, changing provider routing or adding
parallel runtimes merely by merging this document.

## Acceptance
The spec defines authorization, failure/recovery behavior, migration where
applicable, tests and real acceptance gates. No unimplemented capability is
advertised as available.

## Implementation checkpoint — 2026-11-03

The durable outbox slice merged in PR #161 (`602403a`) adds publication/claim and
plain-fallback fences over the existing deletion journal. Runtime/network
quiescence and command/recovery/stream/callback integration remain open. See
[contract and tests](../reviews/2026-11-03-topic-outbox-fences.md).

## Parallel follow-up checkpoint

Ingress/callback/native-admission and guarded output fences merged in PR #163 (`b62eb22`). Original-topic identity survives final fallback chunks, retries and code uploads. Native/transport quiescence, queued-turn settlement and threadless edit ownership still block `/thread delete`. [Evidence](../reviews/2026-11-03-topic-next.md).

## Implementation checkpoint — 2026-11-03

Native/queue/transport leases, scoped edits and conservative startup fence restoration deployed in `a25f22a` (#167, D064). Active login blocks quiescence; deletion command, durable legacy ownership and uncertainty/managed settlement remain gated.
See the [rollout evidence and remaining gates](../deployments/2026-11-03-final-plans-health.md). Status remains in-progress.
