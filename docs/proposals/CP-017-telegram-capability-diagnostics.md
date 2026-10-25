# CP-017 — Telegram capability diagnostics and mode-aware guidance

| | |
|---|---|
| Status | done |
| Date | 2026-10-25 |
| Origin | Owner requested separate self-diagnostics specification after code and Bot API research |
| Size | M |
| Touches | Telegram/control, conversation state and runner lifecycle as defined in the spec |
| Depends on | D011, D020, D022, D030, D031 |
| Plan | [Execution plan](../superpowers/plans/2026-10-25-telegram-capability-diagnostics.md) |
| Spec | [Implementation specification](../superpowers/specs/2026-10-25-telegram-capability-diagnostics.md) |

## Problem
Current behavior and verified gaps are recorded in the linked specification.

## Proposal
Implement the bounded, scoped contract in the specification in separate delivery
slices. The initial private-DM slice is implemented: cached `/telegram`, owner-only
`/telegram doctor [refresh]`, and static help. Group reports, rights probes and
Guest/inline/business notice handlers remain deferred. Deployment and client
acceptance are tracked independently in HANDOFF and ACCEPTANCE.

## Out of scope
Enabling modes, deleting real topics, changing provider routing or adding
parallel runtimes merely by merging this document.

## Acceptance
The spec defines authorization, failure/recovery behavior, migration where
applicable, tests and real acceptance gates. No unimplemented capability is
advertised as available.
