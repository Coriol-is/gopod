# CP-031 — Owner Telegram restart

**Status:** in-progress
**Requested/accepted:** owner, 2026-10-29
**Size:** M

Add `/restart` for the owner's private chat: gracefully restart the supervised
controller, recycle the active personal runtime without deleting sessions/data,
and report the local readiness result to the initiating conversation.

Specification: [Telegram restart](../superpowers/specs/2026-10-29-telegram-restart.md).
ADR: D046 in [DECISIONS](../DECISIONS.md).

## Acceptance
- Router denies non-owners; no shell or arbitrary container arguments.
- Request is durable before the initial notice and shutdown.
- A verified supervisor restarts the controller; unmanaged launches refuse.
- Exact old container IDs are stopped and normal startup recreates the personal
  runtime; disabled groups and uncertain task reservations remain protected.
- Recovery reports ready/degraded truthfully; notice failures cannot loop.
- Real Compose/Telegram smoke remains unperformed until a separately authorized deployment.
