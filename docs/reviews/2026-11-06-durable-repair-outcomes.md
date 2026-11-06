# CP-002 durable repair outcome barrier

Base `a0a3f1e`, branch `feat/durable-repair-outcomes`; contract D069.

## Checkpoint 1: persistence choice and boundary

Message creation ownership cannot represent repeated edits because it uniquely
binds a chat/message. Add `telegram_control_repair_attempts`: immutable attempt ID,
control token/revision/turn, scope, source topic, exact rendered request SHA256 and
whether a successful repair retires that revision. Pending is committed before
HTTP. Only attributed success/not-modified or definite refusal/proven-undispatched
settles; unknown remains pending without TTL. Settlement never acknowledges a
successor control. No foreground activation or operator force-clear is introduced.

## Checkpoint 2: implementation

The shared message edit gate reads the durable raw chat/message barrier, including
registration drift. Repair batch selection excludes pending targets before LIMIT.
Outbox candidate selection excludes held edit targets before consuming an attempt;
a late transport hold remains known-unsent and refunds its budget. Existing
conversation ordering and expiry remain authoritative. Source-topic quiescence
and backup/idle diagnostics count unresolved repair work. No cascading references
or pending deletion are allowed; settled evidence is retained too.

Repair emits one journaled HTTP attempt at a time. Explicit HTML parse refusal
must settle before a separate plain attempt. Unknown/failed settlement prevents
fallback. Bounded process-local exact positive/refusal receipts retry only SQLite;
losing those receipts on restart leaves the durable hold. A final repair ACK and
retirement of exactly its old control revision share one transaction. Historical
HTTP before this journal and wider non-repair edit ownership remain separate gaps.

Root owns global HANDOFF/BACKLOG and D069; the identical accepted D069 section is
included in this source branch so schema and contract ship together. Tests and
independent review follow before PR.

## Checkpoint 3: tests and review corrections

Deterministic tests cover unknown response followed by delayed simulated remote
application, blocked final/legacy edits and restart; exact positive receipt with
DB failure (same-process DB-only recovery vs restart loss); refusal settlement
failure forbidding HTML fallback; bounded receipt capacity without eviction;
wrong-message success, explicit 400 not-modified vs 500 carrying the same text;
precommit/canceled-before-HTTP failures; separate HTML/plain attempts; scope
rotation/control rebinding/deletion and exact-revision retirement; pending-table
immutability/deletion guard; unchanged populated legacy control/outbox hashes
through schema creation and backup allowlist inspection.

Review caught two edge cases, now covered: exact-topic quiescence filters retained
repair receipts by chat/source topic, and every local edit-gate hold (including DB
read errors/cancellation while waiting) preserves/refunds a pending plain fallback
instead of falsely marking remote refusal. Candidate filtering leaves attempts at
zero through repeated sweeps and lets sibling conversations progress. Focused
suites pass; full build/vet/tests and affected races follow before final review.

Independent focused race review: provisional PASS (Store 7.023s, Telegram 7.232s).
Temporary Go overlays removing the preclaim barrier and exact-topic receipt filter
fail the corresponding regressions as expected: an otherwise-held target becomes
claimable, and a sibling receipt incorrectly blocks quiescence. Production source
was not modified during these negative controls. Full final-head results are
recorded by the root handoff/docs integration after CI; no live acceptance is
claimed by these mocked transport tests.
