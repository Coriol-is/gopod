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

## 2026-11-04 durable main control provenance

PR #170 (`dacefc0`) implements metadata-only pre-send ownership for registered
private main/fallback text controls, durable callback/edit lookup and no-replay
receipt failures. Ambiguity blocks quiescence but independent replies remain
available. [Evidence](../reviews/2026-11-04-topic-resume.md) records review,
combined tests and required CI. Historical provenance, noncontrol outputs,
reconciliation, native-login/managed settlement and command orchestration remain
open. This slice is merged, not deployed; CP-016 remains in progress.

## 2026-11-05 implementation continuation

PR #171 (`930586a`) adds bounded DB-only repair for exact positive Telegram receipts after ACK persistence failure. Unknown outcomes and receipts lost on crash remain held; delete orchestration is not enabled.
Merged after review/full checks/required CI; not deployed. See [HANDOFF](../HANDOFF.md)
for evidence and remaining gates. Status remains in progress.

## Noncontrol main-text notice ownership — 2026-11-06

PR #182 (`0ac9b3f`) extends the existing ownership/no-replay helper to restart,
lifecycle and IPC notices. Original-topic provenance survives main fallback; IPC
unknown outcomes and failed positive ACKs cannot trigger whole-message replay.
Full checks, affected races, independent review and required CI passed.
[Evidence](../reviews/2026-11-06-topic-retirement.md). Not deployed. Historical
and nontext/topic-target ownership, unknown-outcome reconciliation, managed/native
settlement and confirmed deletion orchestration remain open; status stays in-progress.

Deployment checkpoint 2026-11-06: all merged slices above shipped in `80f4b11`
([rollout](../deployments/2026-11-06-reliability-cycle-rollout.md)); implementation
and native/client activation gates remain open.
