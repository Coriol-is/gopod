# Code-backed backlog

Audited 2026-11-04 at repository baseline `38e8eba` (application source unchanged
since deployed `a25f22a`). This is the current implementation inventory; the
[proposal registry](proposals/README.md) owns CP lifecycle, [ROADMAP](../ROADMAP.md)
owns priority, and [ACCEPTANCE](ACCEPTANCE.md) owns live verification. Earlier
handoff sections and dated plans are historical, not additional active work.

Production updated to `d58cdd2` on 2026-11-05; queue fix and delivery/ownership
slices #168–#172 are deployed. Health, state preservation and Telegram version
passed; [rollout evidence](deployments/2026-11-05-queue-delivery-ownership.md).
Foreground activation, topic retirement and provider/client acceptance stay open.

## Latest rollout — 2026-11-05 15:11 UTC

PRs #173–#177 deployed on Pi as `757faa2`; no worker. Three D068 scheduling
guides migrated preserving originals; deployed host and actual matching Node
consumer passed read-only list/replay/conflict. Full-row chat/session/task hashes
unchanged, health/version passed. [Evidence](deployments/2026-11-05-stop-ipc-rollout.md).
These rollout facts supersede the earlier "not deployed" slice checkpoints below.
Other native/client gates and full IPC restore remain open. Owner-authorized
backup cleanup retained rollback anchors and removed only 18 old database-only sets.

## Code-inline implementation checkpoint — 2026-11-05

The [conditional-plan proposal](plans/2026-11-05-final-code-inline-alternatives.md)
was accepted as D065. Bounded pre-rendered suffix variants and all-inline budget
fallback are implemented in PR #173. Full build/vet/test and affected races pass;
source review passed and required CI passed; merged as `0aa6628`. Deployment remains pending. Foreground activation remains gated. Production remains `d58cdd2`.

## Successful-final ownership checkpoint — 2026-11-05

PR #174 adds a process-local handoff in the existing sync/stream/fallback sender:
pre-boundary Stop wins, post-boundary Stop is rejected, and a captured retired
generation cannot claim its replacement. [Evidence](reviews/2026-11-05-final-owner-handoff.md).
Merged as `40f368f` after independent review, full checks/races and required CI. Not deployed.
This does not qualify persistent frozen cancellation or all preview metadata.

## Stopped-result publication checkpoint — 2026-11-05

PR #175 fences new interactive-result publication transactionally when any member
is currently durably stopped. Boot retains the plan and continues other recovery;
existing deliveries retain outbox ownership. Merged as `a63c324` after independent
review, full checks/races and required CI. [Evidence](reviews/2026-11-05-stopped-result-publication.md).
This does not persist cancellation intent before native completion or settle
published delivery, and does not enable the foreground producer. Not deployed.

## Durable Stop implementation checkpoint — 2026-11-05

Owner accepted [D066](plans/2026-11-05-durable-stop-intent.md). PR #176 implements
immutable complete-batch intent before cancel/acceptance, reciprocal publication
and native-admission fences, exact commit read-back and late completion snapshots.
Stop emits acknowledgement/control retirement only. Merged as `51fbb7a` after
independent review, final full checks/races and required CI (one IPC-failure rerun).
[Evidence](reviews/2026-11-05-durable-stop.md). No deployment or foreground activation.
Late return evidence does not establish native exit or resolve uncertain delivery.

## Task IPC repair merged — 2026-11-05

The [task IPC receipt/archive race](reviews/2026-11-02-task-ipc-replay-race.md) is
fixed in source by PR #177 (`cbdd9e3`), under D067/D068. Host-only durable claims
prevent replacement archival, journal pins preserve replay authority, and hash-aware
consumers reject old success for changed bytes. Independent review, full checks/
races and required CI pass. [Evidence](reviews/2026-11-05-task-ipc-claims.md).
No deployment: Pi remains `d58cdd2`. Existing consumer guides/scripts need migration;
status-only clients are not qualified. Complete IPC backup/restore remains separate.

## Current priority

Use [ROADMAP execution priority](../ROADMAP.md#execution-priority--2026-11-04),
reordered on 2026-11-04 after this audit: queue fix (complete) → CP-002/003 delivery → CP-016 retirement → CP-018 providers
→ CP-019 group UX → CP-033 readiness → CP-014 media → CP-007 recovery (lowered
by the owner). Acceptance of shipped features has a separate
lane. Table order below is an inventory, not a competing execution queue.
Queue item completed on main as `01cc7e0`; **CP-002/003 and CP-016 resume first**.
Owner-deferred managed-task credentials and parallel sessions remain deferred.

## Unfinished implementation

| Item | Code present and actual runtime wiring | Remaining work / next action |
|---|---|---|
| CP-002 foreground final delivery | [result_freeze.go](../internal/runner/result_freeze.go) provides `WithResultFreeze`; [final_reply.go](../internal/telegram/final_reply.go) provides `PrepareFrozenFinalReply`; neither has a non-test caller. Batch identity and immutable publication storage exist. [main.go](../cmd/gopod/main.go) calls `recoverReadyFinalReplies` before queue recovery when the dispatcher is enabled; [recovery adapter](../cmd/gopod/final_reply.go) publishes existing ready plans only. | Implement the foreground producer/context binding and publication call sites together with preview ownership, late cancellation and reply-action parity. Successful voice/text/code presentation parity is corrected in PR #169 (`c1966dc`); conditional code-inline alternatives are implemented in PR #173 (D065), awaiting rollout; PR #174 adds successful-final process-local ownership; PR #175 holds new publication for currently stopped members; D066 intent and late completion evidence are implemented in PR #176; full preview/reply-action integration, native/client qualification and producer activation remain open. Existing recovery is not foreground cutover. D063/D065 remain the contracts. |
| CP-003 typed IPC | [artifact_request.go](../internal/ipc/artifact_request.go) validates typed file/voice requests; [outbox.go](../cmd/gopod/outbox.go) wires durable IPC preparation/transport; immutable artifact and voice/code preparation foundations are present. | Complete the wider CP-002-linked typed-output/protected-challenge/release scope. Do not reimplement file delivery already exposed by CP-040. Photo-to-document upload fallback now has separate durable attempts (PR #172, `a016b82`); conditional code-inline assembly is implemented in #173/D065, not yet deployed. Full matched recovery remains a separate CP-007 gap. |
| CP-016 topic deletion | [topic_quiescence.go](../internal/telegram/topic_quiescence.go) implements tested quiescence and refusal reopening. `main.go` calls `RestoreTopicFences` before recovery. Queue/native/transport guards and durable store/outbox fences are wired. `QuiesceTopic` has no non-test caller; no Telegram delete orchestration is exposed. | New main/fallback text-control provenance is implemented in PR #170 (`dacefc0`), including restart-safe lookup and no-replay receipt failures. Positive receipt DB-ACK repair is implemented in PR #171 (`930586a`), with no HTTP resend. Historical provenance, noncontrol outputs, unknown remote ambiguity reconciliation, managed settlement, ordinary native-login exit qualification, then confirmed API/outcome/retirement command remain. Deployed guards do not mean `/thread delete` is usable. D064 remains the contract. |
| CP-004 managed tasks | `main.go` installs the managed ledger; under `ManagedTasksEnabled` it installs [taskexec](../internal/taskexec) handler/snapshot and runner policy. [task_run.go](../internal/runner/task_run.go) implements isolated native execution. Main supplies no qualified task credential adapter and explicitly keeps task-role execution denied. | PR 4–5 settlement/visibility and schedule policies; owner-deferred credential adapter and native qualification before activation. Flag remains off in the recorded deployment. A wired executor is not an operational managed task service. |
| CP-007 backups | [backup_cli.go](../cmd/gopod/backup_cli.go), [backup](../internal/backup) and [maintenance](../internal/maintenance) implement create/verify/list/idle/quarantine, writer fences, restore latch and scratch verification. `matched_stopped` includes artifact blobs/config; the backup test covers that manifest. | Timer and bounded retention; ChatsDir/provider session/auth/IPC coverage; restore epoch invalidation; activation/restore command and Pi restore drill. Online artifact leases are needed for an online matched mode, not missing from every existing backup. CP-039 full migration remains proposed. |
| CP-033 proactive presets | `main.go` registers `/proactive`, resolves owner workspace and upgrades templates. [proactive.go](../cmd/gopod/proactive.go), [catalog/upgrade/outcome](../internal/proactive), control panel and owner integration guide are present. The `Connector` dependency is not supplied, so readiness remains `unknown`, with explicit confirmation on enable. | Provider connector probe/readiness, setup/recheck/discovery-offer lifecycle and acceptance. Schedule quiet-window logic exists; it is not a complete quiet-hour delivery deferral mechanism. PR 3 is no longer “in review”. Presets are not enabled by this audit. |
| CP-037 parallel sessions | `main.go` wires lifecycle safety and durable session-store/adoption handling, including the durable mapping branch. P0/P1 shipped; recorded production mapping remains `legacy`. | Native resume qualification after adoption; P2–P7 parallel execution and routing work. T5/T5b remain paused; completed T9 cgroup work is not an open reboot task. |

## Implemented, with remaining acceptance

| Item | Implementation evidence | Open verification |
|---|---|---|
| Queue idle handoff | Shared ordinary/managed lost wakeup fixed in PR #168 (`01cc7e0`). Independent review, full checks, 100-repeat stress/race and required PR CI pass; [evidence](reviews/2026-11-04-queue-idle-handoff.md). | Deployed as `d58cdd2`; no production loss has been observed. First CI attempt's unchanged runner force-drain failure remains a separate observation despite successful retry. |
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

The original audit changed no application source; the later queue fix is recorded
above. For future status updates, distinguish
**definition**, **production caller**, **enabled configuration**, and **live
acceptance**; tests alone cannot prove activation.
