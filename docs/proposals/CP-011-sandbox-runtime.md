# CP-011 — Separate the sandbox runtime from the agent provider

| | |
|---|---|
| Status | accepted |
| Date | 2026-10-25 |
| Origin | Owner requested saving extensibility specs before deploying the current code; implementation is deferred |
| Size | L |
| Touches | future runner runtime contract, Docker adapter, paths/mounts, configuration, lifecycle tests |
| Depends on | D013, D022, D026, D030 |
| Spec | [Sandbox runtime contract](../superpowers/specs/2026-10-25-sandbox-runtime.md) |

## Problem
`Runner` owns a concrete `*Docker`; its execution seam covers only Exec and
ExecStream. Container lifecycle, mounts, cancellation and resource admission
still depend on Docker. Adding another execution backend is not a provider-only
change. The documented subprocess fallback does not exist: disabling containers
currently skips the runner.

## Proposal
Introduce an explicit runtime contract, first adapt Docker without behaviour
changes, then qualify additional adapters through the same isolation/lifecycle
contract. Keep the agent provider independent of the execution backend.

## Out of scope
Implementing adapters now, selecting a vendor, enabling a remote service,
changing isolation or credentials, and treating subprocess execution as a sandbox.

## Acceptance
- Docker passes a shared lifecycle/exec/stream/Stop/recovery suite with existing
  mounts, permissions, sessions and queue behaviour preserved.
- Unsupported runtime/provider combinations fail before execution.
- A backend can be added without rewriting Telegram, Router, queue or provider CLI code.
- Runtime identity and resource enforcement are explicit and independently verifiable.

## Next step
Review the spec and qualify the Docker adapter migration first. Other backends
need a separate implementation plan and explicit operational acceptance.
