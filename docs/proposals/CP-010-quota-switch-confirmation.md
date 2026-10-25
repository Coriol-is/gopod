# CP-010 — Offer provider switching after quota exhaustion

| | |
|---|---|
| Status | done |
| Date | 2026-10-25 |
| Origin | Owner requested quota switching with a notification, then chose explicit confirmation instead of automatic switching |
| Size | M |
| Touches | runner, control, store turn lookup, Telegram error UI, main wiring |
| Depends on | CP-009, D028, D030 |
| Spec | [Confirmed quota fallback](../superpowers/specs/2026-10-25-quota-switch-confirmation.md) |
| Plan | [Implementation plan](../superpowers/plans/2026-10-25-quota-switch-confirmation.md) |

## Problem
Quota exhaustion produces an error, leaving the user to switch providers and
resend the request manually. Silent automatic switching would remove the user's
choice over a chat-wide provider change.

## Proposal
On a recognized quota rejection, offer a single-use switch-and-retry button and
Cancel in the original conversation. Preserve context through CP-009. Confirm
through the Router, then retry the same input once in the same thread.

## Out of scope
Unattended switching, automatic switch-back, periodic quota polling, auth flows,
new providers, new quota-detection heuristics for generic 429s, and deployment.

## Risks
The target may also be exhausted or unauthenticated. A provider choice affects
all threads of the chat. A prior run may have performed actions before hitting
quota; the receiving provider must verify workspace state before repeating work.

## Acceptance
- No provider change or retry before confirmation; Cancel/expiry have no effect.
- Confirming changes the whole chat's provider and queues the original request
  once, retaining its original thread, voice flag and file path.
- Foreign chat/thread/folder and stale provider choices are rejected.
- Both quota exhaustion and switch/queue failures produce truthful notices.
- No reverse switch is offered for the fallback attempt itself.
- Build, vet, full tests, race tests, independent review and CI pass.
