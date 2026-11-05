# D066 durable ordinary Stop implementation

Base `c9a362f`; branch `feat/durable-stop-intent`. Owner accepted D066.

## Implementation checkpoint

Store records immutable intent identity and complete coalesced `(turn,attempt)`
membership, validating the captured conversation, current attempts and ordinary
execution class. Stop and publication have reciprocal transactional checks.
StartBatch and boot recovery fence cancellation despite mutable status rewrites.
A separate immutable completion snapshot stores returned text and a fixed
returned/error/no_replay class. It is not native exit or publication authority;
existing frozen results are linked through immutable exact member identity.
Backup table counts cover all three additive tables.

The ordinary handler captures the full admitted batch before selecting its
presentation item. Generation registration installs persistence callbacks before
exposing the generation. Typed slash/button/draft Stop outcomes distinguish
accepted, already requested, finished and failed/unknown. Pending Stop fences
terminal success AND error presentation. Exact read-back can confirm a commit
whose acknowledgement was lost; inconclusive evidence holds with NoReplay.
Unconfirmed Stop stays attached to native completion rather than returning into
the handler's deferred cancel. Shutdown retains its independent cancellation path.

Preview admission closes when Stop persistence begins and already-issued preview
calls join before acceptance. After accepted Stop, only fixed acknowledgement and
control retirement are sent: no accumulated answer/code/voice or final reply memo.
Late completion is recorded through the exact generation even after unregister.
Recovery controls report requested cancellation without claiming native exit.

## Review and tests

Initial independent review found error-finalization and deferred-native-cancel
races. Both are corrected; deterministic regressions cover pending Stop at terminal
error (sync/stream/fallback), failed persistence with a still-running native stream,
and joining issued previews before acceptance. Expanded races pass. Full build/vet/test and Store/Telegram/control/cmd races
pass (31.802 / 63.052 / 11.212 / 16.891 s). Independent review PASS before the
last small admission delta; its review and final verification are running.
The delta blocks another native fallback after failed Stop and preserves setup
NoReplay errors; its focused race suite passes (7.194 s).

Store tests cover reciprocal publication races, full-batch atomic validation,
partial conflicts, late freeze, close/reopen, status rewrites, immutable evidence,
old-schema migration and backup table parity. Host tests cover complete batch
commit-before-cancel, late returned/NoReplay text and ambiguous commit read-back.
Existing stopped-output tests now assert the owner-approved acknowledgement-only
behavior instead of promoting partial text into a final answer.

## Operational boundaries

No deployment, live Telegram/provider call or paid generation. Foreground result
freeze/preparation/publication remains disconnected. General preview metadata
ownership across arbitrary replacement remains separate from Stop admission.
No native exit proof is inferred from the completion table or handler return.
Unresolved cancellation evidence must not be dispatched with an older binary;
retain compatible code/holds and backup evidence. No ledger deletion or blind
rollback is provided. Managed task cancellation remains on its existing ledger.
