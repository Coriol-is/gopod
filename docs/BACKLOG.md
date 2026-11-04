# Code-backed backlog

Audited 2026-11-04 at repository baseline `38e8eba` (application source unchanged
since deployed `a25f22a`). This is the current implementation inventory; the
[proposal registry](proposals/README.md) owns CP lifecycle, [ROADMAP](../ROADMAP.md)
owns priority, and [ACCEPTANCE](ACCEPTANCE.md) owns live verification. Earlier
handoff sections and dated plans are historical, not additional active work.

Production observations remain the 2026-11-04 **08:50:42 UTC** read-only snapshot
in [HANDOFF](HANDOFF.md#current-status--2026-11-04). This code audit did not deploy,
change flags, invoke providers or perform new Telegram acceptance.

## Unfinished implementation

| Item | Code present and actual runtime wiring | Remaining work / next action |
|---|---|---|
| Queue concurrency regression | [managed_test.go](../internal/queue/managed_test.go), `TestConcurrentDispatchAndEnqueueRace`: repeated concurrent `Dispatch` and ordinary `Enqueue`. Fresh `-count=100` run fails twice at the 3-second wait for all 12 managed outcomes. Fake ordinary IDs start at 1000; managed IDs are 1–12, so an ID collision is not the explanation. | First recommendation: reproduce with scheduling/settlement traces and distinguish queue defect from harness/timing defect. Missing timely test outcomes are established; production task loss and root cause are not. Source fix/review remains a separate task. |
| CP-002 foreground final delivery | [result_freeze.go](../internal/runner/result_freeze.go) provides `WithResultFreeze`; [final_reply.go](../internal/telegram/final_reply.go) provides `PrepareFrozenFinalReply`; neither has a non-test caller. Batch identity and immutable publication storage exist. [main.go](../cmd/gopod/main.go) calls `recoverReadyFinalReplies` before queue recovery when the dispatcher is enabled; [recovery adapter](../cmd/gopod/final_reply.go) publishes existing ready plans only. | Implement the foreground producer/context binding and publication call sites together with conditional code-upload fallback, preview ownership, late cancellation and reply-action parity. Existing recovery is not foreground cutover. D063 remains the contract. |
| CP-003 typed IPC | [artifact_request.go](../internal/ipc/artifact_request.go) validates typed file/voice requests; [outbox.go](../cmd/gopod/outbox.go) wires durable IPC preparation/transport; immutable artifact and voice/code preparation foundations are present. | Complete the wider CP-002-linked typed-output/protected-challenge/release scope. Do not reimplement file delivery already exposed by CP-040. Full matched recovery remains a separate CP-007 gap. |
| CP-016 topic deletion | [topic_quiescence.go](../internal/telegram/topic_quiescence.go) implements tested quiescence and refusal reopening. `main.go` calls `RestoreTopicFences` before recovery. Queue/native/transport guards and durable store/outbox fences are wired. `QuiesceTopic` has no non-test caller; no Telegram delete orchestration is exposed. | Durable ownership for legacy fallback/control messages, remote ambiguity reconciliation, managed settlement, ordinary native-login exit qualification, then confirmed API/outcome/retirement command. Deployed guards do not mean `/thread delete` is usable. D064 remains the contract. |
| CP-004 managed tasks | `main.go` installs the managed ledger; under `ManagedTasksEnabled` it installs [taskexec](../internal/taskexec) handler/snapshot and runner policy. [task_run.go](../internal/runner/task_run.go) implements isolated native execution. Main supplies no qualified task credential adapter and explicitly keeps task-role execution denied. | PR 4–5 settlement/visibility and schedule policies; owner-deferred credential adapter and native qualification before activation. Flag remains off in the recorded deployment. A wired executor is not an operational managed task service. |
| CP-007 backups | [backup_cli.go](../cmd/gopod/backup_cli.go), [backup](../internal/backup) and [maintenance](../internal/maintenance) implement create/verify/list/idle/quarantine, writer fences, restore latch and scratch verification. `matched_stopped` includes artifact blobs/config; the backup test covers that manifest. | Timer and bounded retention; ChatsDir/provider session/auth/IPC coverage; restore epoch invalidation; activation/restore command and Pi restore drill. Online artifact leases are needed for an online matched mode, not missing from every existing backup. CP-039 full migration remains proposed. |
| CP-033 proactive presets | `main.go` registers `/proactive`, resolves owner workspace and upgrades templates. [proactive.go](../cmd/gopod/proactive.go), [catalog/upgrade/outcome](../internal/proactive), control panel and owner integration guide are present. The `Connector` dependency is not supplied, so readiness remains `unknown`, with explicit confirmation on enable. | Provider connector probe/readiness, setup/recheck/discovery-offer lifecycle and acceptance. Schedule quiet-window logic exists; it is not a complete quiet-hour delivery deferral mechanism. PR 3 is no longer “in review”. Presets are not enabled by this audit. |
| CP-037 parallel sessions | `main.go` wires lifecycle safety and durable session-store/adoption handling, including the durable mapping branch. P0/P1 shipped; recorded production mapping remains `legacy`. | Native resume qualification after adoption; P2–P7 parallel execution and routing work. T5/T5b remain paused; completed T9 cgroup work is not an open reboot task. |

## Implemented, with remaining acceptance

| Item | Implementation evidence | Open verification |
|---|---|---|
| CP-040 agent attachments | [chattmpl](../internal/runner/chattmpl/chattmpl.go) seeds owner `ATTACHMENTS.md`; [attachment skill](../container/skills/attachments/SKILL.md), IPC `as` selection and [outbox transport](../internal/telegram/outbox_transport.go) expose file/photo delivery and refusals. Feature is deployed and artifacts enabled in recorded rollout, not waiting on a deleted feature branch. | Full A22–A25 matrix: fresh Claude/Codex discoverability, photo/document, refusal and flag-off cases. Recorded topic PDF and source-missing refusal checks cover only part of this matrix. |
| CP-038 elevated | Host profile, journal, explicit login/lifecycle and completion path shipped; [deployment evidence](deployments/2026-11-03-elevated-login.md). | Claude browser completion remains open. Bounded native Codex acceptance passed. No elevated container in the latest snapshot. |
| CP-020 isolation | Controller/policy/profile implementation shipped; see [proposal](proposals/CP-020-personal-group-isolation.md). | Remaining provider/client and cross-boundary qualifications, not a new isolation implementation project. |
| Healthchecks / Stop / restart | Shipped implementation and [release evidence](deployments/2026-11-03-final-plans-health.md). | Optional-runtime checks, remaining Stop/restart client cases. No new reboot performed or implied. |

## Accepted designs and deferred candidates

CP-018 per-conversation providers follows CP-016. CP-019 group service UX and
CP-014 media queues remain in the approved later order; existing per-chat
provider and media functionality does not complete these broader designs.
CP-011/012/032/034 retain accepted design scope. CP-005/006/021–030/035/039 retain
proposed scope in the registry. This audit does not promote candidates or alter
owner deferrals. CP-035 hooks research is merged documentation, not activated hooks.

## Verification of this audit

- Searched production Go call sites separately from test references for
  `WithResultFreeze`, `PrepareFrozenFinalReply`, `QuiesceTopic` and startup recovery.
- Read actual main/config wiring, backup CLI, preset dependencies and attachment
  template/IPC/transport code; checked D063/D064 and proposal acceptance scope.
- `go test ./internal/queue -run '^TestConcurrentDispatchAndEnqueueRace$' -count=100`:
  **FAIL**, two timeout iterations, at unchanged baseline `38e8eba`. Historical
  full/race/CI passes remain valid observations, not proof this regression vanished.
- Focused final-reply/quiescence/backup checks: **PASS** with
  `go test ./internal/telegram ./cmd/gopod ./internal/backup ./internal/proactive -run 'Test(FrozenFinalReply|TopicQuiescence|CreateVerifyRestoreRoundTrip|MatchedCapturesArtifactsAndConfigAndRecordsFence)' -count=1`.
  Matching tests ran in Telegram and backup packages; cmd/gopod and proactive
  reported no matching tests. This is not a full-suite or proactive acceptance run.

No application source was changed. For future status updates, distinguish
**definition**, **production caller**, **enabled configuration**, and **live
acceptance**; tests alone cannot prove activation.
