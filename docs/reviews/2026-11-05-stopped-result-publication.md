# CP-002 stopped-result publication fence

Base `fea0d6a`; branch `fix/stopped-result-publication`.

## Gap and implementation

Boot publishes ready final plans before ordinary queue recovery. A plan could
therefore be published even when a member turn was already durably `stopped`.
The common publication transaction now refuses NEW interactive-result delivery
when any member is stopped, returning a NoReplay hold. It runs after existing
publication lookup; a stopped turn cannot rewrite an existing outbox outcome.
Boot reports the hold and continues other ready plans, without rerender or replay.

No schema change: the existing terminal turn status supplies the durable evidence.
Results, immutable plans and reservations remain intact; reservations keep their
existing TTL. Coalesced results are held as a whole. Managed results retain their
separate ledger policy. This implements the existing conservative cancellation
contract without changing the frozen result or claiming remote non-delivery.

## Verification checkpoint

Focused Store and boot-recovery regressions pass, including a closed/reopened DB,
artifact-reference/reservation rollback, a stopped coalesced member, repeated
publication ownership, managed exclusion, and recovery continuing other plans.
Independent review and full build/vet/test/race checks are running.

## Remaining boundary

This is a publication fence after Stop has been persisted. It does not persist
Stop intent before native completion, settle a late native result, fence every
preparation call, or cancel already published parts. A publication that wins
before Stop is still outbox-owned, including sending/uncertain outcomes. Those
paths need explicit settlement before foreground producer activation. No deploy,
Telegram/native call, paid generation or flag change.
