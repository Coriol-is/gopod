# CP-041 — Private-chat status widgets

| | |
|---|---|
| Status | accepted — design only; implementation not authorized |
| Date | 2026-11-11 |
| Origin | Owner requested assessment of Telegram sendWidget, then specifications only |
| Size | M |
| Touches | internal/telegram, internal/control, internal/store, controller wiring; Go bot dependency |
| Depends on | Existing topic ownership, delivery outcome and backup barriers; no paused-feature activation |
| Spec | [Product](../superpowers/specs/2026-11-11-telegram-widget-panel.md), [transport and lifecycle](../superpowers/specs/2026-11-11-telegram-widget-protocol.md) |

## Problem

Text commands expose state, but there is no compact refreshable status panel inside
Telegram. Bot API 10.4 adds widgets. The installed go-telegram/bot v1.27.0 lacks
widget models/methods, the update allowlist excludes widget_query, and the outbox
has no widget part. This is new feature work, not a configuration switch.

## Proposal

Provide an opt-in, owner-private status panel with Refresh, powered by existing
read-only control data. Keep normal text commands available. First qualify one
fixed template and durable creation; add task management or arbitrary agent
widgets only through a later accepted scope.

## Out of scope

Implementation/deployment in this documentation task; groups, shared/inline
widgets, wallet operations, arbitrary HTML/JS from agents, task mutations,
Stop/Restart, provider changes, parallel-session or foreground-outbox activation.

## Risks

Unsupported clients, SDK lag, topic misbinding, stale state presented as live,
ambiguous sends producing duplicates, and accidental expansion of authority.

## Acceptance

- Both specifications define scope, protocol, ownership, recovery and test gates.
- Documentation clearly separates design readiness from implementation and rollout.
- Future implementation passes the acceptance matrix, including real-client checks.
- Existing approved implementation priority remains unchanged.
