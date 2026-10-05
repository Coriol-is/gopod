# CP-002 — Telegram outbox with retry and `uncertain` state

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-05 |
| Origin | feature survey of `rzaytsev/codex-agent` (2026-10-05) and the 2026-10-03 public-release audit |
| Size | M |
| Touches | `internal/store` (new `outbox` table), `internal/telegram` (all sends and edits go through it) |
| Depends on | DT (durable turns, done) |
| Spec | `—` |

## Problem
Every Telegram send and edit is fire-and-forget (`sendFormatted`, `editMessage*`, `react`). A 429 or a 5xx loses the reply; a network blip after the send but before `SetTurnReply` leaves the turn without a memo, so recovery replays it and the user gets a duplicate (parked finding from the durable-turns review). Auth URLs and device codes are sent immediately and may be stale by the time Telegram delivers them.

## Proposal
An `outbox` table: one row per outbound message or edit (`pending → sending → sent | uncertain | failed`), a single sender goroutine that retries 429/5xx up to N times honouring `retry_after`, and marks network/unknown responses `uncertain` instead of pretending exactly-once. Login payloads are lazy: the row carries a reference, the sender resolves the current code at send time and drops expired ones. The reply memo (`turns.reply_msg_id`) is written by the sender when Telegram acks, closing the duplicate window.

## Out of scope
Streaming edits every second do not go through the outbox (too chatty); only final messages, reactions, notices and login payloads do. Exactly-once delivery.

## Risks
Ordering across chats and within a chat must be preserved by the sender. The streaming placeholder edit path and the outbox must not both write the final text.

## Acceptance
- Kill Telegram connectivity for 30 s mid-reply: the reply arrives when connectivity returns, exactly once.
- A 429 with `retry_after` is honoured (test with a fake API).
- `kill -9` between send and memo no longer duplicates a reply on recovery.
- Expired login codes are never sent; `/login` after expiry issues a fresh one.
