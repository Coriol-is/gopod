# CP-002 stopped-result publication fence

Base `fea0d6a`; reviewed `e741cc4`; PR #175 merged as `a63c324`.

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
Independent review PASS on `e741cc4`; independent focused Store/cmd races pass.
Full build/vet/test and full Store/cmd races pass (26.995 s / 16.459 s).
Exact-head required [CI 42024900290](https://github.com/Coriol-is/gopod/actions/runs/42024900290)
passes (test and Docker isolation). Squash source matches reviewed head.

## Remaining boundary

This is a publication fence against the current persisted stopped status, not an
immutable cancellation-intent history. It does not persist
Stop intent before native completion, settle a late native result, fence every
preparation call, or cancel already published parts. A publication that wins
before Stop is still outbox-owned, including sending/uncertain outcomes. Those
paths need explicit settlement before foreground producer activation. No deploy,
Telegram/native call, paid generation or flag change.

## Advisory Sonar

Exact-head `e741cc4` analysis at 2026-11-05 12:10:33 UTC: gate OK,
4 complexity code smells, 0 bugs/vulnerabilities. Findings remain visible;
no suppression. Full inventory read through Sonar CLI (one page, four issues).
