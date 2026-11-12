# CP-018 — Provider selection per conversation

| | |
|---|---|
| Status | in-progress — implementation authorized 2026-11-11; not deployed |
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
slices. The owner requested implementation with priorities 1–3 in agents on
2026-11-11. Source work and tests now exist; native acceptance remains open.

## Out of scope
Enabling modes, deleting real topics, changing provider routing or adding
parallel runtimes merely by merging this document.

## Acceptance
The spec defines authorization, failure/recovery behavior, migration where
applicable, tests and real acceptance gates. No unimplemented capability is
advertised as available.

## Implementation checkpoint — 2026-11-12

[Source and qualification review](../reviews/2026-11-12-conversation-provider-selection.md).
Per-conversation preferences, attempt selection, command and quota routing are
implemented on the integration branch; retirement cleanup is connected atomically.
Independent review, final integration CI and native/client acceptance remain gates.
No parallel execution, durable-mapping or worker activation.
