# CP-018 — Provider selection per conversation

| | |
|---|---|
| Status | accepted |
| Date | 2026-10-25 |
| Origin | Owner requested a specification for different providers in different threads |
| Size | M |
| Touches | Telegram/control, conversation state and runner lifecycle as defined in the spec |
| Depends on | D011, D020, D022, D030, D031 |
| Spec | [Implementation specification](../superpowers/specs/2026-10-25-per-thread-providers.md) |

## Problem
Current behavior and verified gaps are recorded in the linked specification.

## Proposal
Implement the bounded, scoped contract in the specification in separate delivery
slices. This acceptance is for design; implementation remains deferred.

## Out of scope
Enabling modes, deleting real topics, changing provider routing or adding
parallel runtimes merely by merging this document.

## Acceptance
The spec defines authorization, failure/recovery behavior, migration where
applicable, tests and real acceptance gates. No unimplemented capability is
advertised as available.
