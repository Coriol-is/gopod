# CP-019 — Private command, service and error replies in groups

| | |
|---|---|
| Status | accepted |
| Date | 2026-10-25 |
| Origin | Owner requested an implementation spec after researching ephemeral slash commands; ordinary bot content must remain public |
| Size | M |
| Touches | Telegram command publishing, reply/callback transport, scoped input identity and persistence |
| Depends on | D011, D020, D028; CP-017 diagnostics |
| Spec | [Ephemeral commands](../superpowers/specs/2026-10-25-telegram-ephemeral-commands.md) |

## Proposal
Keep public conversational content public. Add scoped, actor-bound ephemeral
transport for service/command/error replies in groups, including every page,
confirmation and failure path. Fail closed if private delivery is unavailable.

## Out of scope
A blanket private-output switch, hidden unsolicited DMs, enabling modes by
merging a spec, private free-text agent contexts in groups or implementation now.

## Acceptance
The spec requires zero public fallback for private results, id=0/reused-id
storage handling, eligibility/expiry checks and real multi-account visibility
acceptance. Design only; current gopod does not advertise this capability.
