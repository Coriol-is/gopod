# Code-backed backlog

Reconciled 2026-11-06 against source at `0200892` and the recorded Pi deployment
`0200892`. Production call sites were rechecked; release health/state/version acceptance
is recorded separately from remaining feature qualification. [ROADMAP](../ROADMAP.md) owns priority,
[proposal registry](proposals/README.md) owns lifecycle and
[ACCEPTANCE](ACCEPTANCE.md) owns client/provider qualification.

Completed slices and explicitly superseded/rejected choices are in the
[archive](history/BACKLOG-2026-11-05-completed.md). Partial parent proposals remain
here; proposed, deferred and paused work is not cancelled.

## Current priority

1. CP-002 delivery with necessary CP-003 slices: remaining group/unregistered and
   non-edit effects, historical uncertainty and explicit reconciliation, eager old-row
   cleanup, mixed legacy/durable newest-row
   ordering, then integrated preview/Stop/publication qualification and foreground
   producer/cutover. Local control/final edit arbitration and More/Back retirement
   checkpoints are deployed in #187 (`0cd8078`); D069 durable repair journal follows in #189; unknowns stay held without supported settlement.
2. CP-016 full topic retirement.
3. CP-018 per-conversation provider/model selection.
4. CP-019 group command/service UX.
5. CP-033 connector readiness and discovery.
6. CP-014 media: Q1 fidelity/cards, then Q2 document/image/speech, then Q3 video.
7. CP-007 full recovery, then automation; lowered by the owner.

The independent acceptance lane starts with CP-040 A22–A25, then remaining
Stop/restart and isolation checks. Coordinate one Telegram actor and avoid overlap
with deployment/auth changes. No new work is activated by this reconciliation.

## Latest placeholder deletion slice — 2026-11-06

#193 (`0200892`, D071) journals the sole registered-private sync voice-placeholder
DeleteMessage path. Pending/ACK tombstones fence edits/repair/outbox across restart;
unknown results stop Finished/TTS. Full checks/races, independent review, required
CI and Pi health/state/version/backup verification PASS. No live deletion fault
injection claimed. Group/unregistered and other non-edit effects remain open.
[Release](deployments/2026-11-06-durable-delete-rollout.md).

Sonar tests are completely excluded by owner request; PR scope verified. Main
scanner confirms exclusions but hits57,111/50k line limit; old main report remains
stale. Production scope was not reduced further; Go CI/race unchanged.

## Latest direct edit slice — 2026-11-06

#191 (`6820422`, D070) covers the five registered-private direct text/HTML/keyboard
wrappers with durable provenance/attempts and symmetric barriers. No fallback or
TTS after unknown outcomes. Full checks/races, exact-head review and required CI
PASS; deployed in `6820422` with health/state/version and new-schema backup verification.
D071 above closes registered-private placeholder deletion. Groups/unregistered,
historical missing provenance, other non-edit effects and foreground qualification
remain open.

## Latest repair journal — 2026-11-06

#189 (`a85b328`, D069) adds commit-before-HTTP repair evidence, restart-safe raw-target
barriers, exact DB-only settlement, outbox preclaim holds and backup/idle coverage.
Full checks/races, independent review and required CI PASS. Deployed in `a85b328`;
health/state/version and new-schema backup verification passed.
Historical requests and non-repair edits are not retroactively qualified.

## Latest completed batch — 2026-11-06

#186 (`6faa197`) and #187 (`0cd8078`) passed full checks, races, exact-head independent
review and required CI. Sonar OK, five/nine smells retained. Deployment of `0cd8078`
and bounded acceptance passed; see [handoff](HANDOFF.md). Parent activation gates remain open.

## Current deployed baseline

PRs #168–#180/#182/#183/#186/#187/#189/#191/#193 are deployed on Pi, latest `0200892`; no worker. Exact-main CI,
health, state preservation and Telegram version passed. Three D068 scheduling
guides migrated; actual matching Node consumer passed read-only list/replay/conflict.
[Latest deployment and cleanup evidence](deployments/2026-11-06-durable-delete-rollout.md).
Foreground producer remains disconnected; managed tasks remain off.

## Unfinished implementation

| Item | Code present and actual runtime wiring | Remaining work / next action |
|---|---|---|
| CP-002 foreground final delivery | [result_freeze.go](../internal/runner/result_freeze.go) provides `WithResultFreeze`; [final_reply.go](../internal/telegram/final_reply.go) provides `PrepareFrozenFinalReply`; neither has a non-test caller. Batch identity and immutable publication storage exist. [main.go](../cmd/gopod/main.go) calls `recoverReadyFinalReplies` before queue recovery when the dispatcher is enabled; [recovery adapter](../cmd/gopod/final_reply.go) publishes existing ready plans only. | Implement the foreground producer/context binding and publication call sites together with preview ownership, late cancellation and reply-action parity. Successful voice/text/code presentation parity is corrected in PR #169 (`c1966dc`); conditional code-inline alternatives are deployed in PR #173 (D065); PR #174 adds successful-final process-local ownership; PR #175 holds new publication for currently stopped members; D066 intent and late completion evidence are implemented in PR #176; PR #183 (`261805e`) adds fully ACKed restart-safe callback readthrough. Local control/final arbitration and More/Back retirement checkpoints are merged in #187. D069 repair attempts/barriers and exact-evidence settlement are implemented in #189; D070 registered-private direct edits are implemented in #191; D071 placeholder deletion is deployed in #193; group/unregistered and non-edit effects, historical uncertainty, explicit unknown reconciliation, eager cleanup and mixed-path ordering remain; full preview/reply-action integration, native/client qualification and producer activation remain open. Existing recovery is not foreground cutover. D063/D065 remain the contracts. |
| CP-003 typed IPC | [artifact_request.go](../internal/ipc/artifact_request.go) validates typed file/voice requests; [outbox.go](../cmd/gopod/outbox.go) wires durable IPC preparation/transport; immutable artifact and voice/code preparation foundations are present. | Complete the wider CP-002-linked typed-output/protected-challenge/release scope. Do not reimplement file delivery already exposed by CP-040. Photo-to-document upload fallback now has separate durable attempts (PR #172, `a016b82`); conditional code-inline assembly is deployed in #173/D065. Full matched recovery remains a separate CP-007 gap. |
| CP-016 topic deletion | [topic_quiescence.go](../internal/telegram/topic_quiescence.go) implements tested quiescence and refusal reopening. `main.go` calls `RestoreTopicFences` before recovery. Queue/native/transport guards and durable store/outbox fences are wired. `QuiesceTopic` has no non-test caller; no Telegram delete orchestration is exposed. | New main/fallback text-control provenance is implemented in PR #170 (`dacefc0`), including restart-safe lookup and no-replay receipt failures. Positive receipt DB-ACK repair is implemented in PR #171 (`930586a`), with no HTTP resend. Noncontrol restart/lifecycle/IPC main-text ownership and no-replay are merged in #182 (`0ac9b3f`, deployed in `80f4b11`). Exact private topic-target text ownership is deployed in #186 (`0cd8078`). Historical provenance/retention, nontext/edit-attempt ownership, unknown remote ambiguity reconciliation, managed settlement, ordinary native-login exit qualification, then confirmed API/outcome/retirement command remain. Deployed guards do not mean `/thread delete` is usable. D064 remains the contract. |
| CP-004 managed tasks | `main.go` installs the managed ledger; under `ManagedTasksEnabled` it installs [taskexec](../internal/taskexec) handler/snapshot and runner policy. [task_run.go](../internal/runner/task_run.go) implements isolated native execution. Main supplies no qualified task credential adapter and explicitly keeps task-role execution denied. | PR 4–5 settlement/visibility and schedule policies; owner-deferred credential adapter and native qualification before activation. Flag remains off in the recorded deployment. A wired executor is not an operational managed task service. |
| CP-007 backups | [backup_cli.go](../cmd/gopod/backup_cli.go), [backup](../internal/backup) and [maintenance](../internal/maintenance) implement create/verify/list/idle/quarantine, writer fences, restore latch and scratch verification. `matched_stopped` includes artifact blobs/config; the backup test covers that manifest. | ChatsDir/provider session/auth/IPC coverage; restore epoch invalidation; activation/restore command and Pi restore drill, then timer and bounded retention. Online artifact leases are needed for an online matched mode, not missing from every existing backup. CP-039 full migration remains proposed. |
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

## Separate unresolved observations

- The first #168 CI attempt failed unchanged runner force-drain qualification;
  later green runs do not explain it. Track separately from the repaired queue race
  ([review](reviews/2026-11-04-queue-idle-handoff.md)).
- `store.Open` still builds a raw `file:` URI from the path; `#` path aliasing
  remains a known follow-up ([source](../internal/store/store.go)).
- The previously reported first-128-entry group upload window remains an open
  investigation; no rotation fix or new acceptance is claimed by this audit
  ([historical evidence](history/HANDOFF-2026-11-05-before-housekeeping.md)).

## Verification boundary

Production call-site search confirms `WithResultFreeze`, `PrepareFrozenFinalReply`
and `QuiesceTopic` still have no production callers; ready-plan recovery and topic
fence restoration are wired. Proactive wiring still omits Connector. These are
implementation gaps, not failures repaired by the latest rollout. Historical test
results, including the pre-fix queue failure, are preserved in the archive.
