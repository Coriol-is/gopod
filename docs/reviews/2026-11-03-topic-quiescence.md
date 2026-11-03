# CP-016 conversation quiescence

## Implemented contract

- Exact-conversation native leases span actual Run/RunStream completion,
  CompactSession/ClearSession and extraction. Unknown execution blocks readiness.
- Queue closure cancels only the target active batch; other topics/main continue.
  Accepted input between durable insertion and scheduling is joined. Pending and
  canceled active rows require successful terminal persistence before removal or
  hooks; failed writes remain behind the fence. Frozen-result NoReplay evidence
  remains failed rather than turning into resumable work. Managed items block
  until their ledger-aware cancellation path is implemented.
- Common message/document/voice/draft sends, typing, reactions, keyboard/text
  edits, recovery edits and placeholder deletion own transport leases. Sealing
  waits issued requests without cancelling them. Ambiguous legacy outcomes latch;
  durable outbox sending/uncertain rows independently block readiness.
- Host message ownership is bounded to 4096 entries; existing controls can recover
  validated durable ownership. Unknown private edits own a chat-wide lane, joined
  when any topic is sealed. Known main/sibling requests continue. The unknown lane
  stays closed until restart even after a definite refusal; this conservative
  limitation avoids reopening an unrelated or newer fence.
- Callback ingress retains source identity for cached fallback-to-main controls.
  Asynchronous login startup transfers to its own ingress lease before callback
  return. Final login transport remains independently fenced.
- Pending intents restore before ordinary queue recovery/polling. Restored intents
  always require restart reconciliation; empty active maps never imply that old
  issued HTTP requests did not happen. No delete API is invoked on startup.
- Refusal reopen checks exact intent inside a Store transaction and gate generation.
  Queue mutex precedes Store transaction, matching cancellation/force-drain lock
  order. Durable unfinished turns/claims and registration identity are checked
  again before quiescence can return success.

## Regression evidence

Focused tests pass for actual native-exit join, uncertain native refusal, stale
reopen generation, committed-but-not-yet-scheduled input, sibling continuation,
failed cancellation persistence, unknown edit join/ambiguity, bounded ownership,
pre-dispatch canceled context, cached fallback callback ownership, startup restore,
refused/stale durable intent, durable execution claim and issued outbox attempt.

Initial full checks found two regressions, corrected before final validation:
async login inherited an ingress context canceled on callback return; a Stop test
reused the same fake Telegram message ID in distinct topics. Startup now transfers
a lease, and the fixture models Telegram's unique message IDs.

Full combined-source build/vet/test/race results are recorded at the final checkpoint.

## Exact activation gates still open

No `/thread delete` command or live deletion is exposed by this branch.

1. Durable source ownership for legacy fallback-to-main controls not present in
   the control registry: process restart or cache eviction loses their original
   topic. Cached and durable-control edits are fenced, but old unregistered
   fallback controls need an explicit ownership migration/expiration contract.
2. Explicit reconciliation of pre-crash legacy HTTP/native uncertainty. Restored
   intents conservatively block; there is no automatic permission to delete or
   reopen based on an empty process-local map. Process-local ambiguous legacy
   requests likewise require a reconciliation path rather than blind retries.
3. Ledger-aware settlement of queued managed work. The generic queue refuses to
   erase managed evidence. Ordinary queued turns are settled and checked.
4. Confirmed owner-private deletion command must call quiescence outside its own
   ingress lease, classify and persist the actual Telegram delete result, then
   call existing local retirement only after positive proof. No implementation
   here links those destructive steps or advertises the command.
5. Native/provider and Telegram end-to-end acceptance on disposable data remains
   separate from synthetic Go tests and CI Docker isolation. No live actions were
   performed in this implementation wave.

Parent owns global HANDOFF/ROADMAP and sequential integration/deployment.

## Final integration checkpoint

Combined source `31dc7ff` includes main `de8293b` (CP-002 final-plan publication)
and healthchecks. Build, vet, full tests and runner/queue/telegram/store race tests
passed. Review then identified device-login completion inheriting a released
callback/slash context: completion now acquires its own source-aware lease before
spawn, holds it through delayed native EOF, verification and feedback, and records
an uncertainty hold if cancellation only closes its attachment. Attachment close
is not asserted to prove native login exit. Delayed-EOF and fallback-main
cancellation regression tests run under the race detector.

A pre-existing managed queue stress failure was reproduced independently on main
`572e246`: `go test ./internal/queue -run TestConcurrentDispatchAndEnqueueRace
-count=100` failed three iterations with only 8–11 of 12 outcomes settled. The same
test passed 20 repetitions on this branch; the combined full/race suites passed.
This is a separate follow-up, not suppressed or rewritten by CP-016.

Active ordinary interactive authentication is an additional explicit blocker:
quiescence checks the login registry before closing lanes and again before success.
Matching host-source sessions (including OAuth awaiting a code and main-fallback
UI) block; unknown ownership/starting reservations conservatively hold the chat.
Known sibling-topic sessions do not block the target. Device completion owns a
separate lease through verification; cancellation preserves uncertainty. Ordinary
interactive login has no durable native-exit journal here: closing an attachment,
a removed login session or successful auth status is not destructive-operation
exit proof. Qualifying that evidence remains part of the disabled command's
native-login reconciliation gate, not an assertion made by this API.
