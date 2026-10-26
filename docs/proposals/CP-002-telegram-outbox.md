# CP-002 — Telegram outbox with retry and `uncertain` state

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-05 |
| Origin | feature survey of `rzaytsev/codex-agent` (2026-10-05) and the 2026-10-03 public-release audit |
| Size | M |
| Touches | `internal/store` (new `outbox` table), `internal/telegram` (all sends and edits go through it) |
| Depends on | DT (durable turns, done) |
| Spec | [Design](../superpowers/specs/2026-10-26-telegram-outbox.md) |

## Problem
Final Telegram delivery lacks a durable outbox; many sends and edits are best-effort (`sendFormatted`, `editMessage*`, `react`). Bounded retries can still exhaust on 429/5xx and lose a best-effort reply; a network blip after the send but before `SetTurnReply` leaves the turn without a memo, so recovery replays it and the user gets a duplicate (parked finding from the durable-turns review). Auth URLs and device codes are sent immediately and may be stale by the time Telegram delivers them.

## Proposal
An `outbox` table: one row per outbound message or edit (`pending → sending → sent | uncertain | failed`), a single sender goroutine that retries 429/5xx up to N times honouring `retry_after`, and marks network/unknown responses `uncertain` instead of pretending exactly-once. Login payloads are lazy: the row carries a reference, the sender resolves the current code at send time and drops expired ones. The reply memo (`turns.reply_msg_id`) is written by the sender when Telegram acks, closing the duplicate window.

## Out of scope
Streaming edits every second do not go through the outbox (too chatty); only final messages, reactions, notices and login payloads do. Exactly-once delivery.

## Risks
Ordering across chats and within a chat must be preserved by the sender. The streaming placeholder edit path and the outbox must not both write the final text.

## Acceptance
- Kill Telegram connectivity for 30 s mid-reply: a definitely unsent reply retries when connectivity returns; an ambiguous result is recorded as uncertain, without an exactly-once claim.
- A 429 with `retry_after` is honoured (test with a fake API).
- `kill -9` between send and memo yields a truthful uncertain/reconciliation state rather than blindly replaying a potentially delivered reply.
- Expired login codes are never sent; `/login` after expiry issues a fresh one.

## 2026-10-25 reconciliation
IPC message receipts and process-local terminal outcomes now exist (PR #37),
but are diagnostic and not a durable outbox. This proposal remains open.
Delivery target/visibility must be retained across retry; CP-019 private results
must never become public when eligibility expires. Do not broaden streaming
edits into a durable queue merely to implement final-message reliability.

## 2026-10-26 adoption design

The linked spec now defines this candidate in the [adoption map](2026-10-26-codex-agent-adoption.md),
wave **1**. Status remains proposed; a saved design does not approve
implementation or change the existing delivery queue. New acceptance/migration
requirements in the dated spec supersede conflicting early shorthand here.
