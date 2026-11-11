# CP-002/CP-003 follow-on: registered-private nontext creation provenance

## Change and boundaries

`ownedMessageSend` now shares the existing pre-HTTP source/effective-target scope capture, exact receipt settlement, uncertainty hold and gated external-deletion observer across text, direct document, direct voice and durable As-file sends. An acknowledged message must have a positive ID and the exact recipient chat. Document/voice preparation, immutable snapshot reads and actor authorization stay at their existing call sites; this does not grant new artifact capabilities, paths, audiences or task admission rights (D039/D040).

For registered private chats, successful nontext creation survives restart with its original source topic. An existing legacy redirect to main retains source-topic provenance with effective target zero. Only exact typed `TOPIC_DELETED` for source==target>0, owner chat and the enabled deletion gate may install the external-deletion observation. Unknown outcomes do not reroute or settle; receipt persistence failures retain the existing in-memory exact receipt for DB-only reconciliation. Definite refusal follows the existing ownership-row deletion contract. Durable As-file remains a single callback send with no main redirect; an unknown outcome returns the failure toast but retains pending ownership.

This is a creation-provenance journal, not payload-level idempotency for an entire foreground turn. Legacy `attachLongCode` still logs a failed optional upload and keeps the block inline; optional voice callers still report failure independently. A later explicit user action is a new operation. The gated immutable foreground path is the complete frozen-result delivery mechanism for the registered owner. No claim of complete priority-1 coverage or production acceptance is made.

## Validation

Focused regressions cover document and voice precommit before HTTP, ACK+restart, unknown 500, wrong-chat receipt, typed refusal, gated exact deleted observation without main fallback, receipt-write failure with DB-only reconciliation, source7/target0 legacy provenance, durable As-file ACK and unknown outcome. Existing document/voice/artifact preparation and topic observer tests also run. Full build/vet/test and focused races accompany the checkpoint report.

## Exact remaining contract blockers

### Registered groups

Existing `ConversationScope` can represent registered groups (`internal/store/scope.go`); missing namespace schema is not the blocker. The current foreground policy explicitly requires the configured owner JID (`foreground_delivery.go`) and `registered_chats.is_owner=1` (`store/final_reply.go`). D039 separately excludes group artifact capability recipients. Group enablement therefore requires an explicit destination/actor/publication policy, with artifacts still governed independently.

The negative-chat provenance/transport exclusions also need coordinated implementation: `topic_ownership.go`, `store/telegram_message_ownership.go`, `store/telegram_edit_attempts.go`, `topic_transport.go`, and the outbox topic guard. Legacy reply ordering excludes groups in `final_handoff.go` and `resume.go`; keyboard retirement discovers only the configured owner. Removing one positive-chat or is_owner check would leave the other authority barriers missing. Future registered-group chronology can be recorded under an accepted group contract; historical rows cannot be inferred.

### Unregistered chats and historical targets

`SnapshotConversation` requires a live registered namespace/revision, and durable ordinary batches depend on that snapshot. Unregistered diagnostics retain the existing uncovered behavior; they do not acquire durable delivery or edit authority. Extending them requires registration or a separately specified host scope with equivalent fencing, not a fabricated namespace. Historical message IDs without precommitted provenance or reply sequence remain unproven and are never backfilled.

### Destination and recovery policy

Gone-topic frozen answers remain held: saved-only versus explicit durable main-chat destination is still an owner decision. This change preserves existing legacy fallback only and does not select a new durable destination. Unknown remote sends/edits require receipt reconciliation or an explicit recovery policy; they are not automatically retried. Real-client acceptance, production flag activation and CP-037 worker activation are outside this checkpoint.
