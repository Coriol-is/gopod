# CP-001 — Capability token for the memory API

| | |
|---|---|
| Status | done (reconciled 2026-10-25) |
| Date | 2026-10-05 |
| Origin | feature survey of `rzaytsev/codex-agent` (2026-10-05) and the 2026-10-03 public-release audit |
| Size | S |
| Touches | `internal/memory/api.go`, `internal/runner` (env injection at `docker exec`), container skill `container/skills/memory` |
| Depends on | — |
| Spec | bounded change, no spec: `internal/memory/tokens.go`, `internal/memory/api.go` (`withChat`), `internal/runner/capability.go` |

## Original problem (resolved)
Before CP-001, the memory HTTP API bound `0.0.0.0:9876` (needed so sibling containers reach it via `host.docker.internal`), had no authentication, and took `chat` from the query string (`internal/memory/api.go`). Any agent container, or any host on the LAN when the compose port is published, could read and write any chat's memory, including the owner's. This broke the CLAUDE.md rule that authorization comes from the path, never from data the LLM produced. Documented in README "Security notes" and HANDOFF since the release audit.

## Proposal
The runner mints a random per-turn capability token when it starts an agent exec, injects it as `GOPOD_MEMORY_CAPABILITY` into that exec's environment only, and registers `token → chat folder and conversation thread` in memory for the turn's lifetime (released when the exec ends). The API requires `Authorization: Bearer <token>`, derives the chat from the token, and ignores any `chat` parameter. The container skill sends the header. `codex-agent` does the same with roles per turn (`src/service.js`); we need one role.

## Out of scope
Additional privilege roles beyond the existing chat/thread capability. Rate limiting. TLS. Moving the API off `0.0.0.0` (still needed for the docker bridge; firewalling stays an operator concern).

## Risks
A turn that outlives its token (long tool call after the exec returned) gets 401 mid-turn: tokens must live until the exec's stream closes, not until the handler returns. A token leaked into the chat transcript is valid only for one turn.

## Acceptance
- Request without a token or with a stale one gets 401; `curl` from the LAN with no token gets 401.
- Request with a live token can only read/write the chat that token was minted for; a `chat=` query parameter is ignored.
- Existing memory skill works unchanged from inside a turn.
- Unit tests for mint/lookup/expiry; README security note rewritten.

## Shipped evidence and remaining acceptance
Implemented in internal/memory/tokens.go and api.go, runner/capability.go and
main wiring. Header-only authority ignores caller chat/thread parameters;
capabilities are minted per exec, bounded by TTL and revoked at completion.
Scope/revocation/runner tests pass; real unauthenticated401 is recorded in
[deployment evidence](../deployments/2026-10-25-1e0a21b.md). A real authenticated
agent-skill call remains [A14](../ACCEPTANCE.md), not unfinished implementation.
