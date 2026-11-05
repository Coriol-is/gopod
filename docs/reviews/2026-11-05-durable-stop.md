# D066 durable ordinary Stop implementation

Base `c9a362f`; PR #176 merged as `51fbb7a`; reviewed source `37d4d07`. Owner accepted D066.

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
pass (31.802 / 63.052 / 11.212 / 16.891 s). Independent review PASS including the final admission delta (`37d4d07`);
independent delta races pass (6.629 s). Final source build/vet/all tests and full Telegram races pass (58.008 s).
Final head `e7d2347` adds only explicit publication-first/stale-scope tests; independent
test-delta review and Store focused races pass (7.216 s). Required final-head CI 42035806824 passed after one failed-job rerun.
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

## CI and advisory analysis checkpoint

Source-head required CI 42035428509 passed; final test-only head passed
CI 42035806824 on attempt 2 (test rerun; isolation passed attempt 1).
Sonar exact-final-head gate OK at 2026-11-05 13:27:39 UTC:
24 code smells, 0 bugs/vulnerabilities. Findings remain visible; no suppression.
Final-head analysis commit is `e7d2347fb85e9fd85272153a90dbff27059ff588`.

## Required CI observation

Final-head CI 42035806824 attempt 1 passed Docker isolation and every D066-related
package, but `TestTaskIPCRequestReplayUsesHostJournal` timed out waiting for
`alice/same-delete` at task_workflow_test.go:196. IPC source is unchanged. This
matches the pre-existing [receipt/archive liveness defect](2026-11-02-task-ipc-replay-race.md),
not proof of deadlock. Twenty local repetitions under `-race` pass (8.333 s),
which does not close that defect. One failed-job rerun on the same head passed; no timeout, fixture or production IPC behavior was altered. Both attempts are retained; a successful rerun is not acceptance of the open IPC race.

The retained deterministic IPC diagnostic was also run against final source:
`confirmed: replacement archived, receipt absent, exactly one task` (0.909 s).
Its PASS demonstrates the pre-existing defect, not correctness. The temporary
test copy was removed unchanged; no IPC source or fixture is included in this PR.

## Integration

Squash `51fbb7a48ccbe7a9c785db4dc43ca5f971f08b6d` matches the reviewed source
and final tests byte-for-byte. Merge followed independent review and green required
CI under the documented administrative squash workflow. No deployment.
