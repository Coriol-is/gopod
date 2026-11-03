# CP-016 durable retirement foundation

2026-11-02. First implementation slice of the owner's resumed conversation
management workstream. This does not ship `/thread delete`, call Telegram or
complete CP-016. CP-018 is unchanged.

## Implemented contract

`internal/store/topic_deletion.go` stores a host-generated intent for the exact
registration namespace/epoch and nonzero private-chat thread. Main and groups
are rejected. Every mutation validates the current registration and intent ID.

- `BeginTopicDeletion` returns `created=true` only for the first intent or a new
  intent after a definite refusal. An existing intent is not API retry authority.
- `RecordTopicDeletionOutcome` accepts `deleted`, `refused` or `uncertain`.
  An uncertain operation can only advance on confirmed deletion evidence; a
  refusal of a subsequent probe does not undo the original ambiguity.
- `RetireDeletedTopic` refuses nonterminal turns or any execution claim, including
  unknown execution. It atomically clears native contexts and continuity,
  retires durable identities, marks history seeded, and writes a tombstone.
  Legacy mappings are removed only in legacy mode; D056's frozen legacy table is
  preserved after adoption. Audit turns, shared files, memory and other topics stay.
- Store admission, legacy/native session writes, request/answer continuity and
  history seeding consult the journal in their write transaction. An interactive
  result freeze that would recreate continuity rolls back atomically. Settlement
  of an already held execution claim remains available while deletion is pending.
- A definite refusal retains context and reopens these store admission paths.
  Cleanup failure retains `deleted` for retry; no partial cleanup commits.

The schema is additive and included in backup inspection's known-table registry.
Without a caller creating intents, existing behavior is unchanged.

Registration changes (including a rename that advances its revision) while an
intent is pending deliberately block automatic settlement. The old revision
cannot mutate current state and the new revision cannot inherit the old delete
authority. Admission remains fenced; this slice has no operator reconciliation
command. The command/recovery slice must surface this blocker and design explicit
reconciliation of the recorded target and outcome before enabling deletion. Do
not resolve it by silently replacing the intent or discarding its tombstone.

## Outbox integration checkpoint — 2026-11-03

The outbox slice merged in PR #161 (`602403a`): transactional
publication/validation, held claims during unresolved deletion, pending-only
cancellation after confirmed deletion, and a plain-fallback fence. Existing
in-flight remote attempts still require orchestration/quiescence; see the
[contract](../../reviews/2026-11-03-topic-outbox-fences.md). The remaining gates
below still apply before a deletion command is exposed.

## Required next slice before exposing the command

1. Add `/thread info|delete` through `internal/control`, private nonzero topic
   only, with actor/chat/thread/expiry-bound confirmation. Announce permanent
   Telegram message loss; never redirect a confirmation to main.
2. Take a conversation admission fence and persist the intent. Cancel queued and
   running work in that conversation, wait for writes and native execution to
   quiesce, and settle claims honestly. Database terminal states do not prove
   process exit. Do not stop another topic's shared container or unrelated tasks.
3. Make the exact `deleteForumTopic` call once for a new intent, outside the
   database transaction. Classify definite refusal versus ambiguous transport
   outcome. Recovery must enumerate pending intents and reconcile without blind
   API replay; retain unresolved uncertainty visibly.
4. Integrate tombstones with queue recovery, existing delivery plans/dispatcher,
   streaming and fallback-to-main, stale quota/control confirmations, counters and
   drafts. These output/control paths are not yet fully fenced by this slice;
   a pre-existing frozen result is retained and is not delivery permission.
5. After API success and quiescence, call retirement, complete UI cleanup and
   report success in main without conversation content. Retry pending local
   cleanup after restart. Qualify the full boundary with synthetic crashes on
   either side of Telegram and independent review.
6. Add external-deletion reconciliation and the observed-topic inspector.
   `TOPIC_CLOSED` or generic thread-not-found is not deletion proof. Real deletion
   needs an explicitly authorized throwaway topic; none is touched by this slice.

CP-018 preference retirement must join this transaction when its schema exists.
No preference table or new provider-routing semantics are introduced here.

## Verification

`go build ./...`, `go vet ./...`, `go test ./...` and
`go test -race ./internal/store` passed locally. The tests cover target/scope
rejection, duplicate and stale intents, definite refusal versus uncertainty,
registration changes, restart between remote success and cleanup, atomic cleanup
rollback, unknown execution, cross-topic isolation, late result-freeze rollback,
and a concurrent session write racing deletion. Docker isolation is a CI gate;
no live-provider or Telegram deletion acceptance is claimed.
