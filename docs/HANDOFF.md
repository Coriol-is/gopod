# gopod — current handoff

**Last updated:** 2026-11-05 (D065 implementation and focused checks)

Read this first; use [ROADMAP](../ROADMAP.md) for the
approved queue, [proposal registry](proposals/README.md) for CP status and
[ACCEPTANCE](ACCEPTANCE.md) for pending real-client verification.

**Acceptance authorization (2026-11-03):** The owner explicitly permits agents
to access Telegram and run gopod acceptance checks themselves, including test
messages, commands, attachments and supported button interactions, without
per-check permission. See [CLAUDE.md](../CLAUDE.md#workflow-rules). Older references
to "owner in Telegram" do not require handing automatable checks back to the
owner. No acceptance result or deployment status changes with this instruction.

## Resume here — code-inline implementation in progress, 2026-11-05

Owner requested the next CP-002 code-file-to-inline slice. Analysis found that
upload refusal changes the complete rendered suffix, potentially its message
count, keyboard, reply target and edit eligibility; the existing photo fallback
cannot represent that change. A [concrete conditional-plan proposal](plans/2026-11-05-final-code-inline-alternatives.md)
records storage/selection invariants, bounds, outcome policy, validation and
rollback. Recommended: freeze all suffix variants for at most four code documents;
use all-inline preparation beyond that or the 2 MiB variant budget. Alternative:
linear fragments, which change successful presentation. This is an architectural
contract extending D063, accepted by the owner as D065.
Implementation checkpoint: immutable conditional manifests, atomic suffix selection,
conservative refusal classification, bounded planner and regression tests are now
present locally. Focused planner/Store tests pass; full checks and final independent
review remain pending. No deployment or live acceptance in this slice.
Branch `feat/final-code-fallback`, base `a215a28`; next action is implement the accepted
contract, then validate and independently review. Do not treat the
proposal as an enabled implementation or a completed CP-002 milestone.

## Latest deployment — Pi, 2026-11-05

Production is `d58cdd2`, deployed at 01:57:55 UTC. Controller healthy, zero
restarts/OOM; exact binary hash verified, SQLite integrity OK and full-row
chat/session/schedule hashes unchanged. All eight idle counters zero. Owner
runtime and six unrelated services healthy. Pi only; no worker deployment.
Telegram `/version` response `3176957` confirms `gopod d58cdd2 (go1.26.10)`.
Exact-main CI passed. [Rollout and backup evidence](deployments/2026-11-05-queue-delivery-ownership.md).

First next action: continue the bounded CP-002/003 and CP-016 implementation
queue below. Foreground producer stays disconnected, managed tasks off, and
`/thread delete` unavailable. Native/client acceptance remains separate.
Verified stopped backup `20261105T015718Z-ms-d53ad460` and prior image retained;
reconcile ownership/fallback ledgers before downgrade, with no blind old-binary
restart. Earlier dated snapshots below are historical.

## Integrated upload fallback and receipt repair — 2026-11-05

Both bounded slices are merged after independent review, full build/vet/test,
full Store/Telegram race and exact-head required PR CI. Sonar gates are OK.

| Slice | Integration | Behavior / boundary |
|---|---|---|
| Receipt-backed ownership repair | PR [#171](https://github.com/Coriol-is/gopod/pull/171), `930586a`; reviewed source `cab98d7`, tip `b26b4a9` | Bounded in-process positive receipts retry only DB ACK through recovery/lookup/quiescence. No HTTP resend, no clearing generic uncertainty. Unknown outcomes and receipts lost on crash remain held. |
| Durable photo → document alternative | PR [#172](https://github.com/Coriol-is/gopod/pull/172), `a016b82`; reviewed combined head `219cef3` | Explicit allowlisted photo refusal selects a separately fenced document attempt transactionally before HTTP2. Retries remain on document; uncertain outcomes never authorize replay. Existing immutable part/plan hashes are preserved. |

[Receipt evidence](reviews/2026-11-05-topic-reconcile.md) and
[fallback evidence](reviews/2026-11-05-delivery-outcomes.md) contain the tests and
scope boundaries. Combined source includes both slices; final PR #172 CI is
[41950532576](https://github.com/Coriol-is/gopod/actions/runs/41950532576).
Exact release-main CI passed; the rollout evidence above records the deployed tip.

**Next implementation:** full immutable conditional code-inline alternatives,
preview ownership, late cancellation and reply-action integration before CP-002
foreground activation. CP-016 still needs unknown-remote reconciliation,
remaining ownership/native-login exit qualification, managed settlement and the
confirmed deletion/retirement orchestration. Positive receipt repair closes only
one local failure window, not arbitrary remote ambiguity. CP-018 remains behind
CP-016; backup stays lowered and CP-004 credentials/CP-037 execution deferred.

**Rollout boundary:** changes #168–#172 are deployed as `d58cdd2`; Telegram
version acceptance passed. No login/deletion acceptance was performed. New fallback branch metadata is not downgrade-dispatch-compatible:
old binaries ignore it and may retry a selected document as photo. Before any
future downgrade, inspect/settle affected rows or hold dispatch; never erase the
ledger as a workaround. Older backup allowlists may reject the additive table.
This does not qualify a rollback or waive existing restore gates.

The delivery agent's report checkpoint was preserved in the tracked report before
cleanup. Verified combined source `219cef3` equals integrated main; both temporary
implementation worktrees and local/remote branches are removed. Only canonical
main and the session checkout remain.

## Previous priority and queue checkpoint


PR [#168](https://github.com/Coriol-is/gopod/pull/168) squash-merged as `01cc7e0`
after independent review of `af266ad`, full build/vet/test, queue race checks,
100-repeat deterministic/stress checks (also under race), and green required
[PR CI attempt 2](https://github.com/Coriol-is/gopod/actions/runs/41921413585/attempts/2).
Sonar PR gate OK. Root cause was a shared ordinary/managed lost wakeup between
an empty pending read and worker retirement. Both now occur under one lock;
the departing worker cannot touch a successor's state.

First isolation attempt failed in unchanged runner force-drain qualification
(`ErrExecutionUnproven`); three local Docker repeats and the exact-head CI retry
passed. Preserve that independent observation; its root cause remains open.
[Evidence and review](reviews/2026-11-04-queue-idle-handoff.md).

**Owner steering:** lower CP-007 backup recovery below the current product queue.
Resume CP-002/003 foreground delivery and CP-016 topic lifecycle foundations,
then CP-018 → CP-019 → CP-033 → CP-014. Each implementation slice keeps its review,
CI and activation gates; lowering backup does not waive artifact/restore gates.
Managed-task credentials and CP-037 parallel execution remain deferred.
Queue fix is integrated and subsequently deployed as `d58cdd2`. The priority
update itself made no production changes.

## Completed implementation wave — 2026-11-04

Both bounded slices are integrated after independent review, full build/vet/test,
affected full race suites and exact-head required PR CI. Sonar gates are OK.

| Slice | Integration | Result / remaining boundary |
|---|---|---|
| CP-002/003 successful final presentation | PR [#169](https://github.com/Coriol-is/gopod/pull/169), `c1966dc` (reviewed `ad8479f`) | Sync/stream voice ordering and document/first-text reply targets now match direct delivery. Old ready plans remain immutable. Conditional upload outcomes, preview/cancellation, reply bookkeeping and foreground binding remain open. |
| CP-016 main control provenance | PR [#170](https://github.com/Coriol-is/gopod/pull/170), `dacefc0` (reviewed `d368086`) | Source/scope journal commits before main/fallback text controls; receipt crash gaps persist, callback/edit lookup survives restart. Pending ambiguity blocks quiescence without blocking independent replies. No delete command activation. |

[Delivery evidence](reviews/2026-11-04-delivery-resume.md) and
[topic evidence](reviews/2026-11-04-topic-resume.md) record regression cases,
review corrections and boundaries. Combined validation includes both slices;
fixed test fixtures now use unique Telegram message IDs and register Stop before
starting the stream goroutine, matching production ordering. No production code
was weakened to accommodate those fixtures. Older topic CI `741f729` was
cancelled when superseded; final `d368086` CI passed in
[run 41925384728](https://github.com/Coriol-is/gopod/actions/runs/41925384728).

Next implementation: CP-002 durable refused/ambiguous upload alternatives and
preview/cancellation ownership; CP-016 explicit ambiguity reconciliation and
remaining ownership/native-login/managed-settlement prerequisites. CP-018 remains
behind CP-016, then CP-019 → CP-033 → CP-014; CP-007 stays lowered. Neither CP-002
nor CP-016 is complete merely because these slices merged.

No deployment, live Telegram acceptance, delete or login action occurred in this
wave itself. Queue fix #168 and both slices were subsequently deployed as `d58cdd2`. Main CI for delivery `c1966dc`/docs `c356ce8` passed; topic merge CI
runs separately and is not claimed complete here. The two temporary implementation
worktrees and their local/remote branches were removed after verifying clean trees,
merged PRs and exact combined source equality (`d368086` versus integrated main).
Only canonical main and this detached session checkout remain.

## Current status — 2026-11-04

Historical snapshot; superseded by the 2026-11-05 rollout above.

Read-only recheck at 08:50:42 UTC: Pi source `a25f22a`; controller running and
healthy since 2026-11-03 17:13:09 UTC, zero restarts/OOM, all eight idle counters
zero. Controller binary SHA256 still matches the release. Owner/group/relay/
elevated runtime containers are absent in this snapshot; the owner health result
below is historical, not a current running-container claim. Six unrelated services
are healthy. No new Telegram/native acceptance, deployment or reboot was performed.

At the start of this refresh, main/origin/main and this checkout were `ffe6570`,
with clean trees and no open PRs. [CI for that tip](https://github.com/Coriol-is/gopod/actions/runs/41691871582)
passed. Later status-only commits do not change the deployed application.
[Current backlog](../ROADMAP.md#current-backlog--2026-11-04) distinguishes unfinished
implementation, completed implementation awaiting acceptance, and deferred work.
CP-038 is implemented/deployed; Claude browser completion remains an acceptance
gap. CP-002/CP-016 remain in progress; CP-035 is proposed. The queue defect was subsequently fixed and merged as described above;
CP-007 recovery was subsequently lowered by the owner; delivery/topic work resumes first.

## Code audit checkpoint — 2026-11-04

[BACKLOG](BACKLOG.md) is the current code-backed implementation inventory, audited
at `38e8eba`; priority stays in ROADMAP and CP lifecycle in the proposal registry.
CP-002 foreground freeze/preparation and CP-016 quiescence entrypoints have no
production callers; only their documented recovery/guard foundations are wired.
CP-004 executor is wired behind its flag, but lacks a qualified task credential
adapter. CP-007 has artifact capture and scratch verification, not a full restore
command or all session/auth/workspace coverage. CP-033 templates/outcomes/guide
are shipped; connector readiness is still unknown because no probe is injected.
CP-040 is implemented/deployed, with the full A22–A25 acceptance matrix open.

Fresh queue stress at this baseline failed **2 of 100 iterations** waiting for all
12 managed outcomes. Root cause is unclassified; this is not evidence of actual
production task loss. First next action: instrument/reproduce Dispatch/Enqueue
scheduling and settlement, then review a source fix if warranted. No feature
implementation, activation, deployment or provider/client acceptance happened in
this documentation audit. Focused final-reply/quiescence/backup tests passed;
full suites were not rerun for these documentation edits. The 08:50:42 UTC
production snapshot above remains the
latest operational observation, not a new check performed by this audit.

The obsolete tracked editor copy `docs/HANDOFF.md-e` was removed; Git retains its
history. The handoff procedure now follows CLAUDE's documentation-only exception
instead of its obsolete blanket commit prohibition.

## Repository cleanup — 2026-11-03

Owner-authorized merge/cleanup reconciled all branches. Missing CP-035 research
was added as documentation only (`ed1320a`), still proposed. No open PRs remain.
80 obsolete worktrees, 85 local branches and 53 remote branches were removed;
canonical main and this detached session checkout remain at the synchronized tip.
The old uncommitted elevated handoff and scaffold marker were preserved in the
Git-common cleanup archive before removal. Main local data/auth/config, backups
and production `a25f22a` were not changed. [Audit](reviews/2026-11-03-merge-cleanup.md).
Historical worktree paths below are evidence, not current resume locations.

## Previous release — deployed final plans, quiescence and healthchecks

The owner-approved parallel wave is complete. Production is `a25f22a` since
2026-11-03 17:13:09 UTC. Health #165 (`067c016`), final plans #166 (`de8293b`)
and topic quiescence #167 (`a25f22a`) passed independent review, full local
checks/races and required PR CI. Exact [main CI](https://github.com/Coriol-is/gopod/actions/runs/41681289877)
passed. D063/D064 record the accepted contracts. [Rollout evidence](deployments/2026-11-03-final-plans-health.md).

Nine image overlays are installed; controller and owner Codex runtime were
healthy at 17:14:23 UTC, zero restarts/OOM and all eight idle counters zero.
Verified backup `20261103T171231Z-ms-c88b23ae` (43 tables, 9,103 rows, four files)
and registered chats/sessions/scheduled tasks matched. `/version` reply `3176409`
in test topic `3176309` confirms `gopod a25f22a (go1.26.10)`. Existing auth homes,
workspaces, test topic/PDF and rollback images are preserved. Obsolete worktrees
were subsequently removed under the explicit cleanup authorization above.

Sonar #165 is advisory ERROR for loopback HTTP/security B; #166/#167 are OK.
Main Sonar baseline is stale (`4e3031a`, 2026-10-26). No finding was suppressed.
No reboot, new provider login, paid inference or live deletion occurred.

Next work follows [the 2026-11-04 execution priority](../ROADMAP.md#execution-priority--2026-11-04):
1. **Done on main:** queue regression classified and fixed in PR #168; deployment pending.
2. Owner update: CP-007 recovery is lowered after the product queue below.
3. CP-002/003 foreground delivery; CP-016 retirement; CP-018 providers; CP-019 group
   UX; CP-033 readiness/discovery; CP-014 media, in that order.
Acceptance is a separate lane: CP-040 A22–A25, ordinary Stop/restart, CP-020;
serialize Telegram activity. CP-038 browser completion waits for the required
owner login interaction. Managed-task credentials/activation and CP-037 parallel
execution remain owner-deferred. A confirmed live defect can override feature
priority when its evidence is recorded. The first implementation item is integrated; remaining items have not started.

## Previous completed wave — parallel follow-up, 2026-11-03

Owner approved three parallel streams, with sequential review/integration/deploy.
Base main `09dbaf4`; production controller `e6f0fad` since 16:29:08 UTC. Existing worktrees retained.

- `feat/freeze-next` in `../gopod-freeze-next`: CP-002 ordinary eligibility,
  frozen-result recovery and publication integration; queue/runner adapter ownership.
- `feat/topic-next` in `../gopod-topic-next`: CP-016 admission/quiescence integration;
  shared queue/output contracts must be coordinated with the first stream.
- `docs/acceptance-next` in `../gopod-acceptance-next`: sole Telegram acceptance
  actor, bounded ordinary Stop/media/continuity probes; no auth changes or live deletion.

Implementation checkpoint: CP-002 now classifies frozen/published results before
replay, holds unpublished work visibly, and preserves NoReplay through Telegram;
regressions/full/race checks and independent review passed; PR #164 (`933da28`)
passed exact-head CI and merged as `e8b58b8`.
CP-016 PR #163 (`effc746`) passed independent re-review after preserving the
original topic through final fallback chunks/retries/code uploads and rechecking
after chat actions. Final-head CI passed and PR #163 merged as `b62eb22`. Neither slice enables final
foreground ownership or the deletion command.

Acceptance checkpoint: bounded Codex Stop reached durable stopped state and
native process exit; subsequent nonce recall succeeded. Owner additionally
requested Telegram Toolkit topic creation: `topics create` implemented, 83 local
tests passed, live topic `3176309` created for this acceptance wave. PDF arrived
there and its test bytes were verified. The first source-missing warning leaked
to main and falsely claimed the file remained; a separate bounded IPC notice fix
passed review/full/race tests as `f5768ba`, integrated as `33ba30a` without PR
under the owner-approved small-fix exception. The live regression passed after rollout (see below). Topic retained; Telegram probes are paused. Toolkit main
`c28367d` is published and installed locally.

Root owns global handoff/roadmap, integration and release. Each stream records
intermediate evidence in its own review report. Source changes require independent
review, full checks and exact CI; only bounded small fixes retain the owner's
no-PR exception. Do not enable `/thread delete` or managed tasks from incomplete
prerequisites. Combined source passed full build/vet/tests and affected race suites; source
identity matches the reviewed integration worktree. Exact main
[CI 41654649071](https://github.com/Coriol-is/gopod/actions/runs/41654649071) passed.

Owner-requested SSH retry succeeded. Release `e6f0fad` is deployed; backup
`20261103T162832Z-ms-0c657f89` verified (43 tables, 9,028 rows, four files).
Registered chats/sessions/scheduled tasks matched exactly; SQLite integrity OK.
Startup/readiness, zero restarts/OOM and six external healthy services verified.
[Rollout record](deployments/2026-11-03-parallel-wave.md).
Final Telegram regression passed: warning `3176369` stayed in test topic
`3176309`, reported source_missing truthfully, and did not leak into main.
`/version` reply `3176372`: `gopod e6f0fad (go1.26.10)`. At 16:30:49 UTC:
running, zero restarts/OOM, all seven idle counters zero. Test topic/PDF retained.

Resume next: CP-002 immutable voice/code/stream final publication plan and late
cancellation settlement; CP-016 native/transport quiescence, queued-turn handling
and thread-aware edit ownership. Neither foreground freeze nor `/thread delete`
is activated. This review/integration/deploy/acceptance wave is complete.

## Deployment checkpoint — 2026-11-03

The owner authorized review/merge/deploy and subsequent small fixes without PRs.
Target `4350e1b` is deployed after green main CI and independent review.
Backups, runtime health and exact state preservation passed. Telegram silent
on/off and model/default callbacks now retain their pickers; silent restored off.
ROOT closed normally and is off. [Live rollout record](deployments/2026-11-03-backlog-wave.md).

Small follow-up `ec51ee6` is deployed without PR as authorized. Independent review,
full local build/vet/tests, [main CI](https://github.com/Coriol-is/gopod/actions/runs/41649954437)
(test + Docker isolation), isolated Pi boot, backup and state read-back passed.
Telegram `/version` reply 3176289 returns `gopod ec51ee6 (go1.26.10)`.

Resume here: continue CP-002 eligibility/recovery and frozen-result publication,
then CP-016 admission/quiescence integration. Both remain incomplete; `/thread delete`
is not enabled. Preserve existing worktrees, auth homes, backups and rollback images.
The bounded review/merge/deploy request is complete; first rollout picker evidence
and final backup/image IDs are in the live rollout record above.

## Previous — next integration after PRs #159–161, 2026-11-03

Picker fix PR #159 merged as `88a9aa3` after green CI and independent review.
CP-002 atomic batch-start prerequisite PR #160 is merged as `f5ba97a` after
independent review, full local checks/races and green test/isolation/Sonar.
[Evidence](reviews/2026-11-03-batch-start-foundation.md). Full foreground freeze,
eligibility checks and publication recovery remain the next reliability slice.

CP-016 outbox fences merged in PR #161 as `602403a` after green
test/isolation/Sonar and independent review. The slice connects deletion intents to durable publication,
claim selection and plain fallback. Unresolved deletion holds pending parts;
definite refusal resumes them; confirmed deletion cancels only pending parts.
Already issued attempts retain truthful settlement. Independent review caught
a pending tail hidden behind an uncertain head; the fix cancels pending parts
independently of dispatch eligibility. Full local checks/races and independent
re-review passed; combined queue/store/cmd race suites also passed. [Contract and boundary](reviews/2026-11-03-topic-outbox-fences.md).
This does not enable `/thread delete`: native/transport quiescence, queue/recovery,
streaming and callback fences and registration reconciliation remain open.

Ordinary Telegram [acceptance evidence](reviews/2026-11-03-ordinary-telegram-acceptance.md)
records working pagination/doctor refresh and the picker defect. Silent restored
off; owner's ROOT session left open. `/version` reports `dev`, recorded as a
separate diagnostic follow-up. No deployment or destructive live test.

### Next concrete work

1. CP-002: build explicit eligibility/recovery classification ahead of runner
   invocation using the now-authoritative batch counters. Resume frozen results
   through preparation/publication without model replay, preserve NoReplay through
   Telegram handlers, and qualify preview/final ownership plus voice/code parity.
2. CP-016: add conversation admission/quiescence orchestration and output/recovery/
   callback fences before exposing `/thread delete`; include registration-change
   reconciliation and permanent retired-topic IPC errors. The outbox slice alone
   does not prevent a previously claimed transport call from starting.
3. Deploy only when separately authorized; then retest the silent/model picker
   round trip. Ordinary model/media/Stop checks must not enter the owner's open
   ROOT modal session. `/version` returning `dev` is a separate diagnostic defect.

## Previous — elevated login completion fixed and deployed

PR #158 merged as `1a3b9e1`; controller binary `8c21d58` (identical source tree)
runs on Pi since 2026-11-03 13:03:29 UTC, ready 13:03:36, zero restarts/OOM false.
Normal login exec completion no longer cancels Telegram's UI context before
EOF/verification. Stop/timeout cancellation remains intact. The regression fails
on old source; independent review, 30 repeated targeted race runs, full local
build/vet/tests, affected race suites, PR and exact main CI passed. Sonar is
advisory ERROR (maintainability B, seven open complexity/interface/context findings).

Owner completed Codex browser login. Authorization survived rollout; Telegram
`/root login` now correctly reports already authenticated (3176209), without
starting another login. A bounded no-tools native prompt returned `ROOT LOGIN OK`
(3176212). ROOT remains open and idle for the owner. Do not restart the old
owner-stopped Codex/du host scan or repeat native login unnecessarily.

The previous misleading cancellation notice is fixed; the actual new-login
completion race is regression-tested without forcing another browser cycle.
Claude browser completion remains open. The controller default remains off and
rpi4 flag remains on. Container healthchecks remain queued. Full rollback,
backup, incident and acceptance evidence is in the
[rollout report](deployments/2026-11-03-elevated-login.md).

## CI policy — Sonar soft gate, 2026-11-03

Owner requested advisory Sonar: failures/cancellations/stale analyses are reported
but do not block merge/deploy. Build/vet/test and Docker isolation remain required.
Live configuration verified: Sonar is a separate app check, not a step in `ci`;
repository ruleset `24430037` does not require Sonar. No workflow or remote rule
change was necessary. CLAUDE.md now makes this explicit for every harness.
PR #154 Sonar gate is OK; main's last analysis is stale (2026-10-26, `4e3031a`),
with security rating E. This remains an open analysis-maintenance issue, not a
reason to fail the otherwise successful CI/deployment.

## Documentation queue reconciliation — 2026-11-03

Owner authorized merging all remaining documentation PRs (#108 and #149).
CP-028 is a saved proposed design with unresolved choices, not implemented or
activated. Its session-storage ADR number will be assigned at implementation;
all proposed write defaults consistently require confirmation. Documentation-only
updates now use the owner-approved exception in CLAUDE.md: no mandatory PR or
full verification, with diff/link/consistency checks retained.

## Owner follow-up — container healthchecks

Recorded in [ROADMAP](../ROADMAP.md#owner-todo--container-healthchecks-2026-11-03):
add meaningful health/readiness checks for every gopod-owned container, including
Claude/Codex elevated images. Implemented in PR #165 and deployed in `a25f22a`; controller and owner runtime healthy.
See [health matrix](CONTAINER-HEALTH.md); host reboot remains separate.

## Resume here — integrated elevated completion, 2026-11-03

Owner requested one larger E3–E5 iteration. `feat/elevated-complete` in
`gopod-elevated-complete` implements `/root`, fresh modal owner text, confirmations,
markers, lifecycle/drain/shutdown/boot cleanup, dedicated provider context and
supervised cancellation (D061). Independent review PASS after fixing start/close
races, inspect-only reuse, renamed survivors, restart snapshots, global admission,
nil-Docker recovery, private login interception and cancelled-first-turn context.
Focused tests, build/vet/full suite, affected race suites and real-Docker supervisor
cancellation passed; PR #154 merged as `294e8a7` after green final-head CI. Both ARM64 images built and passed offline smoke checks.

**E6 host gate qualified:** owner authorized memory-cgroup configuration and a
whole-Pi reboot. Before/after inventory preserved all nine containers and their
images; all seven configured healthchecks passed. Memory/CPU/pids enforcement is
available. Candidate images pass offline smoke; the opt-in manager lifecycle test
uses disposable state, both provider images and fixed read-only host probes.
See [host qualification](deployments/2026-11-03-elevated-host-qualification.md).

PR #154 merged as `294e8a7`; exact main CI passed. Pi deployed at 10:53:46 UTC
with binary version `124bc1a` (verified identical source tree to the squash merge).
Owner explicitly requested `GOPOD_ELEVATED=1` on rpi4; controller default remains
off. Telegram API confirms `/root` in the private command menu. No elevated
session starts automatically; live elevated session count is zero. All nine
containers run and all seven configured healthchecks pass. Data hashes unchanged.
[Deployment evidence](deployments/2026-11-03-elevated-host-qualification.md#controller-rollout-and-rpi4-activation).

Telegram picker, warning/Cancel, both provider starts, Stop button and /stop,
journal, close button and /root stop, and stale-close rejection passed live.
[Evidence](reviews/2026-11-03-elevated-telegram-acceptance.md) records message IDs.
Initial test sessions were closed. Subsequently the owner authorized elevated
Codex via device login; its authenticated Telegram response passed (3176105 →
3176107). Owner then sent a host request; that active session was left untouched.
Next: Codex resume/tool acceptance and separate Claude login/authenticated checks. Ordinary credentials were
not copied. `/login` still means ordinary-chat login. Healthchecks remain queued.

## Previous — E0–E2 deployed, 2026-11-03

Pi runs `75b3be3` (PR #152) since 09:47:51 UTC. Independent E2 review,
PR test/isolation/Sonar and exact main CI passed. Sonar CLI reports six
cognitive-complexity findings; gate passed, no suppressions were added.
[Rollout evidence](deployments/2026-11-03-elevated-e2.md) records verified
backups, retained rollback image and unchanged registrations/sessions/tasks.
Both elevated ARM64 images remain installed. All four dedicated elevated tables
exist and are empty. No `/root`, feature flag or elevated runtime is active.

E2 adds dedicated state/generation fencing, reserved-folder exclusions,
provenance bootstrap and journal-before-exec (D060); full build/vet/tests and
store/runner race tests passed. Next: E3 owner-private command, modal input and
warning/confirmation dialogs, E4 lifecycle, E5 docs, E6 host proof. The known
IPC receipt/archive race remains open ([diagnostic](reviews/2026-11-02-task-ipc-replay-race.md)), not fixed by E2.
The earlier [reliability rollout](deployments/2026-11-02-reliability-streams.md) records the bounded restart cursor acceptance.

E3 design constraints discovered by review: root Claude rejects the ordinary
`--dangerously-skip-permissions`; use a dedicated explicit tool allowlist with
`--permission-mode dontAsk` (offline no-auth root check passed, authenticated
acceptance still open). Elevated Codex restore must use `/workspace/session`.
D060 context APIs bypass neither scope validation nor P1; use only dedicated
methods. Store closure/journal -1 never proves runtime death. Do not inherit
ordinary PID-file signalling blindly into host PID namespace. The owner requires
visible start/reply/end markers and actionable refusal/uncertainty messages.

## Previous — CP-038 E0 implementation, 2026-11-03


Owner asked to finish elevated. CP-038 is now in-progress; worktree
`gopod-elevated-e0`, branch `feat/elevated-e0` contains separate Claude/Codex
ARM64 images and local smoke checks. [Evidence](reviews/2026-11-03-elevated-e0.md)
records image digests, native CLI versions, Docker API compatibility with the Pi
and passed build/vet/full tests. No elevated runtime or flag is implemented.

Next: E0 independent source review, green PR CI and merge, then E1 per the
[staged plan](superpowers/plans/2026-11-01-elevated-host-session.md).
P1 is already merged: E2/E3 must explicitly reconcile elevated native context
ownership with the durable adapter's registered-scope requirement. Never make
the reserved folder registerable to work around that check.

At that E0 checkpoint deployed code was `a0de4dc`; deployment documentation PR #149 is
preserved in the reliability rollout report. The older rollout statements below are historical.
Elevated host tests/activation are separate gates; no provider sign-in, host
privileged session or ordinary profile change was performed in E0.

## Previous — parallel queue streams 1–3, 2026-11-02


The owner asked Claude to run approved queue items **1–3 in parallel**: release
qualification, reliability, and conversation management. Recovered session
`f638f77f-451c-49f1-9e36-bc3bced50961` stopped at 17:32:33 UTC on a weekly-limit
rejection during discovery, before starting child agents or changing files.
Codex resumed the work from clean `main` `2d24266`; its
[CI run](https://github.com/Coriol-is/gopod/actions/runs/41428496002) passed.

- **Release qualification:** [live report](reviews/2026-11-02-release-qualification.md)
  verifies the Pi baseline and five owner-authorized Telegram commands, including
  reply delivery and button markup. Visual rendering and callback actions stay
  open. FTS quote escaping is fixed in [PR #143](https://github.com/Coriol-is/gopod/pull/143);
  cancellation misclassified as profile unavailability is fixed in
  [PR #146](https://github.com/Coriol-is/gopod/pull/146), merged as `5b0e362`
  (now deployed). Use SSH to
  `100.123.150.53` with strict host checking: the saved IP works, whereas the
  `rpi4` hostname failed host-key lookup. No SSH settings/key changes are needed.
- **Reliability:** [PR #145](https://github.com/Coriol-is/gopod/pull/145),
  merged as `b8260ea`, adds the CP-002 PR 6a explicit runner result-freeze hook,
  captures the executed model and preserves the no-replay error classification.
  [Integration gates](reviews/2026-11-02-foreground-freeze-foundation.md):
  production binding, durable recovery, publication and voice/code parity are
  still required before activation. Later CP-004/007/006 slices remain open.
- **Conversation management:** [PR #144](https://github.com/Coriol-is/gopod/pull/144)
  merged as `7bbc7dd`, adds CP-016 durable deletion intent, scoped retirement
  and store write fences.
  [Next integration](superpowers/plans/2026-11-02-topic-retirement-foundation.md):
  command/confirmation, conversation quiescence, output/recovery/callback fences
  and registration-change reconciliation; then CP-018 per-thread providers.
  No live topic was deleted.

The FTS fix is merged as `69cb0a9`; all four implementation PRs (#143–146) passed
`test`, Docker `isolation-spike` and Sonar before squash merge. Full local
build/vet/tests and affected race tests passed. The cross-stream runner/store
checks are recorded in [integration evidence](reviews/2026-11-02-stream-integration.md).
These changes are now deployed in `a0de4dc`; see the current rollout above.

The task-role credential adapter, CP-038 E0–E6, host reboot/cgroup changes and
elevated activation remain outside this wave. Existing worktrees and branches
are preserved. Session mapping remains legacy until separately adopted.

Older sections below are historical checkpoints. For current deployed state,
use the latest deployment and the [acceptance report](reviews/2026-11-02-release-qualification.md) rather than older
“not deployed” or “next” statements.

## Previous deployment — `8a4f55a`, 2026-11-02

`main` `8a4f55a` (CP-040 agent attachments, [D058](DECISIONS.md), PR #140 merged after
an independent review) runs on the Pi since **2026-11-02 10:17:49 UTC**; evidence,
backups and rollback tag `gopod-gopod:rollback-pre-cp040-20261102` in
[the deployment report](deployments/2026-11-02-cp040-attachments.md). Agents in the
owner chat now get the attachments policy, `ATTACHMENTS.md` and the `attachments`
skill; real round-trip checks A22–A25 are open ([ACCEPTANCE](ACCEPTANCE.md)).

**Deployment gap found and patched:** container skills are mounted from
`/opt/gopod/repo/container/skills`, a stale copy from 2026-05-07, not from the
checkout `~/gopod`; they were synced by hand (see the report). Decide whether to point
the skills mount at the checkout or keep the rsync as a runbook step.

SonarCloud: `main` analysis is still stale (last success 2026-10-27, ncloc 47,378)
even after `sonar-project.properties` (#141); the failure reason is visible only in
the SonarCloud UI. [Triage](reviews/2026-11-02-sonar-triage.md).

## Previous — CP-040 agent attachments merged, 2026-11-02

CP-040 is implemented on `feat/cp040-agent-attachments` (worktree
`gopod-cp040-impl`, one PR): owner-only `AttachmentsPolicy` + seeded
`ATTACHMENTS.md` + `container/skills/attachments`, the V1 `as` field
(`auto`/`document`/`photo`), refusal codes `artifacts_disabled`,
`path_outside_workspace`, `unsupported_type` with a host notice to the chat
([D058](DECISIONS.md), [spec](superpowers/specs/2026-11-02-agent-attachments.md)).
The refusal notice lands in the main thread (thread 0) like every IPC send, and
a notice lost on a retry is still covered by the `.failed` receipt.
Next: CI green, review and squash-merge by a separate session, deploy when the
owner authorizes, then real-client rows A22–A25 in [ACCEPTANCE](ACCEPTANCE.md).

## Also current — `d8866c9` deployed, 2026-11-02

`main` `d8866c9` runs on the Pi since **2026-11-02 00:04:53 UTC** (image
`5e75c582…`, restart count 0, no errors): `GOPOD_OUTBOX_ARTIFACTS` now defaults to
on ([D057](DECISIONS.md)), the artifact store opened (`/opt/gopod/data/artifacts`).
Evidence, backups (`20261101T235844Z-db-7d82b4d7`, `20261102T000419Z-ms-67366fa1`)
and rollback tag `gopod-gopod:rollback-pre-artifacts-20261102`:
[deployment report](deployments/2026-11-02-artifacts-default-on.md).
In flight on branches: CP-040 agent attachments (spec/plan/implementation,
`feat/cp040-agent-attachments`) and a SonarCloud triage of `main`
(`docs/sonar-triage-20261102`; main's gate is red on security rating E, 11
vulnerabilities to verdict). Real-client acceptance list unchanged below.

## Previous — `be7575f` deployed, 2026-11-01

`main` `be7575f` runs on the Pi since **2026-11-01 18:37:46 UTC** (image
`45ffd4e9…`, restart count 0, no errors after startup, session mapping
`legacy`). It ships CP-037 P0 (`5c36b81`) and P1 (`3409907`, [D056](DECISIONS.md)),
`/silent` (`6d11765`, [D055](DECISIONS.md)), the CP-038 gap closure (`9ccfba4`:
button presses now carry `MessageID`, so the `/restart` lifecycle buttons work) and
the CP-039 draft. Evidence, backups (`20261101T183220Z-db-8df16ea0`,
`20261101T183712Z-ms-eb46af94`) and rollback tag `gopod-gopod:rollback-pre-p1-20261101`:
[deployment report](deployments/2026-11-01-p1-silent-cp038.md). Reach the Pi
through Tailscale (`rpi4`, `100.123.150.53`); the LAN address is not used.

**Next:** real-client acceptance on the deployed build — A08/A10 Stop, A21
`/silent`, the `/restart` drain/force/status/cancel buttons, `/sessions status`.
`/sessions adopt` is not run; the Pi profile `deploy/compose.rpi4.yml` is not
applied. Then P2 scoped controls (`/clear`, `/new` epochs) per the CP-037 slice
plan, and the CP-039 draft refinement when the owner picks it up.

## Previous — CP-037 P1 durable identity merged, 2026-11-01

P1 adds durable session identity ([D056](DECISIONS.md)): additive store tables
for execution sessions, conversation bindings, native contexts, attempt
bindings and per-session execution claims, a runner adapter that resolves the
native context through the binding, queue binding that refuses a stale epoch,
and an owner-only `/sessions [status|adopt|cancel]` command. Adoption of legacy
`sessions` rows is one transaction at the next controller startup, deferred
while unfinished turns exist. It lives on branch `feat/p1-durable-identity` in
worktree `gopod-p1-identity`, [PR #133](https://github.com/Coriol-is/gopod/pull/133), squash-merged as `3409907`. The default mapping mode stays
`legacy`, so nothing changes on the Pi until `/sessions adopt` plus a restart;
not deployed. Rollback to pre-adoption code after durable writes needs explicit
reconciliation (the old `sessions` table is frozen at adoption). Open: real
native resume after adoption on the Pi ([ACCEPTANCE](ACCEPTANCE.md)).
The final whole-branch review's C1 (continuity through the durable adapter),
C2 (stale claims released at startup, terminal/unknown takeover), I1 (adoption
skips unreachable rows, `/sessions cancel`) and I2 (`execution_unknown`
recorded from the runner's proof) were fixed before the PR.

Next: P2 scoped controls (`/clear`, `/new` epochs) per the slice plan.

## Previous — `/silent` progress-only streaming merged, 2026-11-01

[PR #130](https://github.com/Coriol-is/gopod/pull/130) (`feat/silent-mode`,
worktree `gopod-silent`, branch retained locally) was admin squash-merged to
`main` as **`6d11765`** after green PR CI (test, isolation-spike, Sonar). It adds
[D055](DECISIONS.md): `/silent` (`internal/control/silent.go`, `ChatLocal`,
owner-only in groups, `on`/`off` picker, state `silent:<chatFolder>` in
`router_state`, default off) and, in `streamReply`, a silent turn keeps answer
chunks out of the draft and the edit-path placeholder (they accumulate only for
Stop); the view stays on the status header and the last five tool steps until the
final message. On the edit path a reply or stopped partial over `editLimit` takes
the overflow branch (placeholder keeps the progress summary, reply sent
separately). Command rows: [CONTROL.md](CONTROL.md) ChatLocal table and §6.1.
Plan: [superpowers/plans/2026-11-01-silent-mode.md](superpowers/plans/2026-11-01-silent-mode.md).

Verified: `go test ./... -race`, vet, independent reviewer findings fixed (long
edit-path reply lost, vacuous negative assertions, group-scope test), Sonar
duplication gate passed after folding the test fixtures. **Not deployed**; Pi
remains `8a3ce75`. Real Telegram acceptance of `/silent` is open: picker edit in
place, draft shows only header and steps, final reply once, Stop mid-turn,
group edit path, a reply over 4096 chars. Owner asked session `gopod-08` to run
the acceptance.

Noted, out of scope: `gofmt -l` lists `internal/runner/auth.go`, `compact.go`,
`provider_claude.go` on `main` (pre-existing, CI has no gofmt gate).

## Also current — CP-038 elevated host session designed, 2026-11-01

[CP-038](proposals/CP-038-elevated-host-session.md) is **design only** and merged to
`main` as **`2ca967c`** ([PR #128](https://github.com/Coriol-is/gopod/pull/128), green
test/isolation-spike/Sonar, worktree removed, branch deleted). Status stays `proposed`:
no code, no feature flag, nothing deployed. The owner asked to plan the feature, not
build it, so **implementation needs their acceptance of the CP first**.

What it designs: an explicitly started agent conversation with host authority on the
Pi. `Privileged: true` plus host PID/network/IPC, the Docker socket and the host root
at `/host`, started by `/root` with a provider picker, owner-private only, bounded by
explicit close rather than a timer. Contract in the
[spec](superpowers/specs/2026-11-01-elevated-host-session.md), slices `E-pre`-`E6` in the
[plan](superpowers/plans/2026-11-01-elevated-host-session.md). Container profile is a
separate builder and a separate policy role; the hardened chat profile is not
parameterised and not relaxed.

**The button defect review found is fixed**, not pending. `callbackCaller` populated no
`MessageID` and `internal/control/restart.go:28` refuses a caller without one, so
`/restart force`'s confirmation, `/restart status` and `/restart cancel` were all
refused. Scope it precisely: those argument forms and their buttons arrived with
`RegisterRestartLifecycle` in `5c36b81`, which is **not deployed**. The Pi runs
`8a3ce75`, which wires `RegisterRestartCommand` with no arguments and no buttons, so
production was never affected. Slice `E-pre` is the fix; see the gap-closure section.

**Corrections already made — do not re-derive them.** Spec section 13 records what the
first draft got wrong about the source. A stale policy version *denies*
(`internal/runner/exec_guard.go:211-213`), it does not recycle, and no `policyRevision`
bump is needed because `Fingerprint` hashes only the policy's own role and scope. The
guard does need new logic: a role mismatch currently reaches `actRecreate`, and
`ContainerFacts` never inspects `PidMode`/`NetworkMode`/`IpcMode`. The policy digest runs
only under `GOPOD_MANAGED_TASKS`, off by default. There is no session entity to extend,
so the runtime binds to one reserved `elevated` chat folder. Boot removal belongs between
`restart.recycle` and `CleanupLeftovers`, or a survivor blocks all execution.

**Gaps closed in a follow-up.** `E-pre` and the drift the review found are implemented;
see the next section. The remaining CP-038 slices (`E0`-`E6`) are untouched and still need
owner acceptance of the CP.

## Gap closure after the CP-038 design, 2026-11-01

Follow-up to the design merge. Implements CP-038 slice `E-pre` and clears the drift
the review surfaced. No CP-038 feature slice is started and no flag exists.

- **Button presses now carry `MessageID`** (`internal/telegram/cmdbuttons.go`). This
  repairs `/restart force`'s confirmation, `/restart status` and `/restart cancel`, which
  `internal/control/restart.go:28` refused because every press arrived with zero. Those
  argument forms shipped in `5c36b81` and are not deployed: the Pi's `8a3ce75` wires
  `RegisterRestartCommand`, no arguments and no buttons, so production was never
  affected. A future `/root` needs the same field. Covered by two tests: a unit test on
  `callbackCaller` and an end-to-end one that types `/restart`, presses Status, then
  confirms `/restart force` through the Router and asserts each press arrived with a
  message id.
- **`/stop` gained the same message fence** it was missing next to `/restart`. The inline
  Stop control is its own `"st"` callback, not a Router command button, so it does not
  pass through the command handler and is unaffected.
- **`docs/ISOLATION.md` §6.1 now exists** and documents the hardened container profile
  the code actually builds. The comments in `internal/runner/docker_args.go` cited that
  subsection while it did not exist; the `§6.4` reference is folded into it.
- **`internal/runner/exec_policy.go`'s `policyRevision` comment** said containers created
  under an older definition are "recycled". The guard denies them, which blocks execution
  for the folder until the container is gone; the comment now says so.
- **The top-level `container/Dockerfile` and `container/Dockerfile.codex` are left
  exactly as they are.** Removing them was the first attempt and it broke
  `TestGroupImageMatrixPreservesPersonalAndExcludesPersonalTools`: the test reads them as
  the retained pre-split baseline it diffs the maintained profiles against. So nothing
  builds them but something does read them. Each now carries a header saying it is that
  baseline. The test compares file contents, so even an explanatory header broke it; the
  files are left byte-for-byte untouched and explained in `container/README.md` instead. The real defect was the build instructions: both `README.md` and
  `container/README.md` told readers to build from these files even though they lack the
  `--ignore-scripts` npm install and the native-binary verification. Both now run
  `container/build-profiles.sh`, and comments in `version.go`,
  `docker_integration_test.go` plus a ROADMAP row point at the maintained paths.
- **The spec's routing gap is closed.** Modal routing moves the owner's input only;
  scheduled results, proactive digests and outbox sends keep arriving in the same private
  chat, unprefixed, and must never be re-routed to the reserved folder.

Still open and not closable here: M9 credential derivation stays the owner's decision, so
a first elevated session would require an interactive provider sign-in.

## Previous state — CP-037 P0 merged, 2026-11-01

[PR #126](https://github.com/Coriol-is/gopod/pull/126) (`feat/p0-integration`,
worktree `gopod-p0-integration`, branch retained) was admin squash-merged to
`main` as **`5c36b81`** after green PR CI (test, isolation-spike, Sonar) on its
final head `47e12a8`. It carries the P0 lifecycle slice: busy/unknown
runtimes protected from idle eviction, `/restart` drain plus confirmed
`/restart force`, startup quarantine that keeps the Telegram control plane up,
durable Stop-control recovery, the opt-in Pi 4 resource profile and the CI
Docker lifecycle fixture. Accepted behavior is [D054](DECISIONS.md); the command
is in [CONTROL.md §5.3](CONTROL.md) and the runtime rules in [ISOLATION.md §7](ISOLATION.md).

Claude acceptance on the final head: build/vet, full suite, race suites for
runner/queue/telegram/store/control/scheduler/cmd, 7/7 Docker fixtures and the
Compose override check passed. An independent reviewer agent returned 5 High,
6 Medium, 3 Low; `e428811` fixes the Highs (force now settles quarantined
leftovers of retired folders, quarantine inventory runs even when removal is
skipped, ExecStream failures verify quiescence instead of marking unknown,
restart-target scope errors are no longer swallowed) and the accepted Mediums/Lows.
Two Mediums were kept by design (Stop stops the runtime container; a batch that
fails during a drain is not retried). Full table and remaining limits:
[P0 acceptance report](reviews/2026-11-01-p0-acceptance.md).

**Not deployed.** Pi remains `8a3ce75`; the owner deferred any rollout until the
whole CP-037 feature is complete. The commit ids quoted above (`e428811`,
`47e12a8`, the worker and integration SHAs) live on the retained
`feat/p0-integration` branch; `main` carries them only inside squash `5c36b81`.
Main CI for `5c36b81`: see the Actions run recorded in the PR timeline.

**Next:** P1 durable identity per the [slice plan](superpowers/plans/2026-11-01-parallel-sessions.md)
once the owner opens it; real Telegram acceptance of `/restart`, `/restart force`
and Stop-control repair (C01–C14, C18–C29) stays open and is not inferred from CI.
Known P0 limits: no C9 Continue/Start UI, no stream reattachment, no automatic
replay of started work, uncertain control messages re-edited every 30 s, Pi
memory values are admission inputs only (no memory cgroup).

### Integration provenance

Worker source commits: lifecycle `37f3efd`, Telegram `05f39fa`, restart `c17f99d`.
Integration counterparts: `ee5d582`, `00b014a`, `b5e9961`; root resources `6f32bea`
and startup wiring `e980ad9`. Restart follow-up `cc29c97` is also cherry-picked: quarantine keeps Telegram
available; startup UI repair is bounded to 10 seconds. All worktrees/branches are retained.

Worker source commits: lifecycle `37f3efd`, Telegram `05f39fa`, restart `c17f99d`.
Integration counterparts: `ee5d582`, `00b014a`, `b5e9961`; root resources `6f32bea`
and startup wiring `e980ad9`. Restart follow-up `cc29c97` is also cherry-picked: quarantine keeps Telegram
available; startup UI repair is bounded to 10 seconds. Inspect the integration log
for its cherry-pick SHA and the final documentation checkpoint.

Pi was inspected read-only: 4 CPUs, 3796 MiB RAM, 1754 MiB available with current
containers already included, 2063 MiB swap used, 36 GiB disk free. Memory cgroup is
absent; CPU/PID enforcement is available. [Measurements and staged limits](reviews/2026-11-01-rpi4-resources.md).
Owner explicitly wants parallel capability: opt-in Pi profile max=2 with strict
memory admission and growth reserve; at observed headroom only one start fits.
No live profile, reboot, container mutation or inference probe was performed.

P0 implements lifecycle/restart/control recovery only. Per-thread parallelism,
new/clear session generations and mixed-provider failover remain later slices.
Credential/native-home qualification, CP-004/007 and measured real-provider load
remain gates for the complete feature. All earlier deployment evidence below is
historical production state, not evidence for this local branch.

## Design context — 2026-11-01

Owner requested design only, then selected private chats plus background tasks,
per-topic folders plus shared projects, and explicit chat/thread separation.
[CP-037 draft](superpowers/specs/2026-11-01-parallel-sessions.md) proposes dedicated
active-session containers, persistent native context/workspaces and bounded
single-host admission. Design authorization does not accept every architectural
choice or authorize implementation. No code or runtime changes made.

Owner subsequently requested implementation specs, another review, and no abrupt
planned restart; explicitly requested `/restart force` cancelling background work.
Saved contracts A/B/C/D and delivery slices linked from the architecture. Ordinary
restart drains; force cancels active foreground and background plus pending
background occurrences, preserving schedules and queued foreground. These are
specifications, not shipped behavior.

[Second review](reviews/2026-11-01-parallel-sessions-spec-review.md) found current
idle eviction, root cancellation on restart and stale startup cleanup can interrupt
work. Existing focused runner/cmd/queue tests pass; no live interruption test ran.
P0 must fix lifecycle safety before parallel activation.
Owner-requested follow-up recorded in contracts C6/A5: partial force failure,
cancel-drain UI/races, external-effect settlement and archive access/export/purge.
Archive command/API detail remains a separate follow-up; P0 scope stays lifecycle.
Consolidated routing/mixed-provider behavior in contract D, A6/B5/C7 and plan P3b.
Third self-review covers account bounds, A/B/A handoff cursors, provider-blocked
queue eligibility, control-operation races and quota/fallback versus restart.
Automatic task fallback remains deferred; current tests do not prove new contracts.
Owner added emergency Telegram Stop/status repair: contract C8/A7 and C18-C26.
P0 covers durable existing-placeholder repair; P5 covers generation/fallback races.
Unknown execution must not render as stopped, obsolete callbacks cannot stop a
new attempt, and failed edits remain durable without blocking runtime shutdown. Review is by the author,
not independent approval. Next: implement and accept P0;
provider credential/home migration, CP-004/007 and host qualification gates remain.
T5/T5b implementation and T9 reboot remain paused.
CP-035 is reserved in the separate CLI-hooks research worktree.

## Previous state — private Stop deployed, 2026-10-31

[CP-036](proposals/CP-036-private-chat-stop.md) is merged in
[PR #124](https://github.com/Coriol-is/gopod/pull/124), **`8a3ce75`**, and deployed
to the Pi at **20:00:48 UTC**. Final-head PR/main CI and Sonar passed; build/vet,
full tests and affected-package race suites passed. See
[deployment evidence and rollback](deployments/2026-10-31-private-stop.md).

Owner-private answers now carry an inline Stop before agent startup; `/stop`
acts only on the current topic. New input remains queued, and Stop does not clear
history/session mappings. Buffered fallback cancellation and voice-only delivery
are covered. All 34 session mappings, 10 schedules, 2 registrations and feature
flags matched before/after rollout. Controller is running, restarts 0, OOM false;
private Telegram menu contains `/stop`. Both online and matched-stopped backups
verified; the previous controller image is retained.

Next: real-client A08/A10 acceptance of button and command during thinking/tools/
waiting and subsequent Claude/Codex session continuation. Startup and menu checks
are not this acceptance. No deliberate active-turn interruption or inference
canary was performed. Existing security/proactive/artifact gates below remain.

## Previous deployment — security fix wave, 2026-10-31

All eight groups from the [security review](reviews/2026-10-30-security-logic-review.md)
are merged, code baseline **`e586c4a`**. Group 1 shipped first. Each PR had an independent
source review, green `test`, Docker `isolation-spike` and Sonar checks, then admin squash merge.
See [acceptance and limitations](reviews/2026-10-31-security-wave-acceptance.md) for the PR/SHA map,
regression coverage, review fixes and the combined verification result.

**Pi deployed:** `e3b5401` started **2026-10-31 00:24:07 UTC**, explicitly authorized after wave acceptance.
Image `sha256:289e86dcd86ce470c4ba8d38c97b42da944f40b6b2e14752333d11fb9d89cf69`;
controller running, restart count 0, OOM false. Owner Claude container and CLI 2.1.310 ready;
Telegram long polling and outbox running. [Deployment evidence and rollback](deployments/2026-10-31-security-wave.md).
Verified backups: `20261031T001910Z-db-a42b1b89` (online) and
`20261031T002335Z-ms-376e0956` (matched stopped). Rollback image:
`gopod-gopod:rollback-pre-security-e3b5401` (previous `630aa4e`).

**Post-deploy snapshot at rollout (not live counters):** DB integrity OK; schedules, sessions and registrations exactly match the stopped backup.
Outbox: 14 sent, none uncertain; 176 done turns, no pending/running. Existing outbox dispatcher/IPC flags
remain on; artifacts/managed-task flags remain off. Memory API binds only `172.17.0.1:9876`, is reachable
from the owner container, returns 401 without a capability, and refuses LAN connections. Telegram API
read-only probes succeeded and `/outbox` is in the command menu. No account refresh, inference canary,
Gmail/Calendar run or deliberate schedule mutation was performed.

**Next action:** real-client acceptance of trigger/callback denial, `/outbox` confirmation/settlement,
and ordinary messages during device login remains open; do not infer it from startup or CI.
New non-owner budgets: 10 unfinished tasks, 15-minute minimum recurring period, 96 task runs per
folder/UTC day; group defaults 1 active group and 96 turns per namespace/UTC day (configurable).
Owner-private turns are exempt from those budgets and the 15-minute group turn timeout.

**Boundaries:** M9 (derived credential copy) remains deferred for the owner decision. Group upload
import skips excess files instead of disabling the group, but only examines the first 128 directory
entries: new attachments can remain outside that window until older files are removed. The wave
does not complete CP-002/003/004, resolve the CP-028 spec questions, or authorize deferred low findings.
Worktrees and remote branches are retained; unrelated PR #108 remains open.

## Current source and operational snapshot

Housekeeping started from clean `main` **47f0fcc** (deployment evidence PR #122).
All 27 existing worktrees were clean; all branches and worktrees are retained.
Only open PR at the check: [#108](https://github.com/Coriol-is/gopod/pull/108),
CP-028 Telegram user-account connector specification, awaiting owner decisions.
This documentation update uses its own worktree and reviewed PR; it needs no Pi deployment.

Read-only Pi recheck **2026-10-31 00:42:54 UTC**: checkout `e3b5401`, exact image
above, controller running, restarts 0, OOM false. The original startup, backups,
rollback tag and data-verification evidence remain in the deployment report.
No runtime flags, schedules, credentials or containers changed during housekeeping.

## Shipped behavior and remaining streams

| Stream | Implemented and deployed | Still open |
|---|---|---|
| Security groups 1–8 | PRs #113–120; acceptance report above | Real-client checks, M9 derived credentials, deferred low findings |
| CP-002/003 outbox | Store/dispatcher, personal durable rich text, artifact snapshots, voice/code preparation, photo presentation and per-run capability issuer; owner `/outbox` settlement | Foreground cutover, protected challenges/staged release, artifact backup/restore qualification and activation; paid TTS qualification |
| CP-004 tasks | Storage, managed dispatch/recovery, guarded task execution/reporting | Settlement/result visibility, schedule policy, task-role credential adapter (owner deferred); managed flag off |
| CP-007 backups | Online and matched-stopped create/verify foundation, controller lock and recovery quarantine | Timer/retention, complete auth/session/workspace/IPC coverage, artifact leases, restore/activation command and Pi restore drill |
| CP-020 groups | Isolation, default registered-group admission (D047), tree-budget fix #88, triggers and owner-in-group controls (D048), private login redirect (D049) | Final Codex new/resume/tool qualification and cross-boundary/client checks; do not infer broad acceptance from startup |
| CP-031 restart | #84 / `8bdff46`, deployed; owner reported baseline success | Negative/topic/provider edge cases in live acceptance |
| CP-033 proactive | PRs 1–3: preset core/store, owner `/proactive`, templates/outcomes and integration guide | First actual digest evidence; P03 scheduled-run → once-task result before PR 4 reminders, then probe/discovery/docs |
| Tables | D050/D051 aligned blocks and compact cards | Native Rich Tables remain separate |

Group admission is no longer accurately described as globally disabled: the 2026-10-29
checkpoint records automatic preparation and successful Claude new/resume group turns after
owner login. That is historical evidence, not a current auth probe or Codex qualification.
The security rollout did not intentionally alter registrations or activate additional groups.
See [current isolation policy](ISOLATION.md), [acceptance](ACCEPTANCE.md) and the historical snapshot.

## Resume next

Private-chat interruption audit (2026-10-31):
[behavior, verification and limitations](reviews/2026-10-31-private-chat-interruptions.md).
Ordinary messages queue; Stop explicitly cancels a turn. Race tests and a local
Docker process-kill probe passed; real-client A08/A10 remain open. The sync-fallback cancellation-context defect is fixed in CP-036. Remaining
limitations: cleanup failure still releases the slot, and stopped partial-reply
delivery errors can be swallowed. No production
runtime changes or deliberate interruptions were performed.

Owner-private Stop is implemented and merged in [PR #124](https://github.com/Coriol-is/gopod/pull/124),
`8a3ce75`; [CP-036](proposals/CP-036-private-chat-stop.md) is done. Ordinary messages
remain queued. The persistent inline Stop and `/stop` cancel the active answer in
the current topic without clearing history/session mappings. Buffered fallback
uses the registered child context; voice-only completion keeps its delivery policy.
Build, vet, full tests, affected-package race tests, PR CI and main CI passed.
Source re-review was performed by the implementing agent and found/fixed the
voice-only regression; Sonar provided separate automated analysis, not a human
GitHub approval. Owner-authorized admin squash retained the source branch.
Deployment completed at 20:00:48 UTC; [evidence](deployments/2026-10-31-private-stop.md)
records both verified backups, retained rollback image and unchanged state hashes.
A08/A10 real-client checks remain open.

1. Record real-client security acceptance: changed trigger/callback denial, owner-bound buttons,
   `/outbox` confirmation/settlement, and ordinary messages during device login. Keep startup/CI
   evidence separate from Telegram-client evidence.
2. Record first proactive digest outcomes and P03 in [ACCEPTANCE](ACCEPTANCE.md); do not mutate
   the existing active freefeed digest (09:00 Europe/Belgrade) merely to close a checkbox.
3. Plan a bounded fix for the first-128-entry group upload window; no rotation policy is implemented.
   Resolve M9 credential derivation with the owner before changing credential format.
4. Continue the [approved roadmap](../ROADMAP.md): CP-033 remaining slices, CP-003 agent image
   instructions/foreground cutover and backup gates; deferred task adapter remains deferred.
   CP-034 and CP-028 require their design/owner gates before implementation.
5. ~~Fix the button `MessageID` fence~~ done in `E-pre`: `/restart force|status|cancel`
   work again on `main` (`5c36b81`, not deployed; the Pi's `8a3ce75` has no argument
   forms). Everything else in CP-038 waits for the owner to accept the CP.

## Decisions already taken — do not re-ask

- Owner-private chat/sessions retain their broad authority; group budgets do not narrow owner-private work.
- Task-role credential adapter deferred; recurring tasks continue on the existing main-session scheduler.
- Gmail/Calendar connectors allowed in private chat. CP-033 uses Europe/Belgrade with a picker,
  conservative hourly urgent checks, English UI, and quiet hours mean no run; owner-edited tasks
  are outside preset quiet hours. Presets ship off by default; do not reset owner settings.
- CP-034: official-source skill/plugin installation from private chat only, auto-update on, never groups.
- CP-028 direction: Go/gotd, read and write, private chat only; its seven spec questions remain open in PR #108.
- No automatic paid fallback. T9 memory-cgroup/reboot and parallel implementation remain paused; CP-037 design is reopened.
- CP-038 elevated session, decided 2026-11-01: `Privileged: true` plus host PID/network/IPC (not a
  narrower capability set); its own command and conversation (not a mode of the current topic);
  explicit close as the only bound, so no TTL or idle timer; confirmation, exec journal, chat marker
  and a separate `exec_guard` profile all required; modal routing while a session is live; the journal
  records only host-performed `docker exec` rows and makes no claim to record the agent's own host
  commands. The owner accepts that the container is host root and that content read inside it acts
  with that authority.

## Open gates and boundaries

- Artifact delivery defaults to on since [D057](DECISIONS.md) (`GOPOD_OUTBOX_ARTIFACTS=0` disables);
  it is live only after the next deployment. Managed-task flag remains off. Scratch restore of a set
  with an artifact, artifact-store retention and artifact leases for online matched backups remain open;
  the agent-facing contract is CP-040.
- Managed tasks cannot execute with the current unqualified task credential adapter. Before enabling,
  qualify native subscription use and an actual Pi Docker create/inspect/reuse role round trip.
- Account login/refresh, paid inference/TTS, T9 reboot, deliberate schedule changes, feature activation,
  Telegram privacy/admin changes and live Gmail/Calendar acceptance require their own explicit scope.
- CP-011/012/014/016/018/019 and CP-032 remain designs; CP-019 does not yet make group service replies private.
  Ordinary conversation remains public. Topic workspace/container state is chat-shared, not per-thread secret.
- Media baseline is path/transcription, not universal OCR/video understanding. Provider switching remains
  chat-wide. Maintenance re-arming/quiet hours, compaction recovery, crash windows and live controls retain
  their acceptance gates. [Proposal registry](proposals/README.md) is the CP status authority.
- Non-owner cron bounds are conservative; SQLite log copies can drop on the bounded queue/shutdown deadline.
  See the wave acceptance report for the complete limitations and deliberately unallocated low findings.
- `store.Open` raw SQLite URI paths containing `#` can alias fixtures; URI escaping remains a separate fix.
  Storage dependency upgrades remain constrained by D017.

## Operations and history

Pi: `ssh spaceinvaderz@192.168.1.128`, checkout `~/gopod`, data `/opt/gopod/data`.
Use [deployment evidence](deployments/2026-10-31-security-wave.md) and
[backup/restore runbook](deployments/BACKUP-RESTORE.md), preserving the rollback image and both backups.
For commands inside an SSH script, redirect `docker compose run` stdin from `/dev/null`
so the one-shot container cannot consume the rest of the deployment script.

Worktree → PR → independent review → green CI → admin squash merge; no direct main edits,
force push or remote branch deletion. HTTPS with `gh auth git-credential` is the working fallback
when Git SSH stalls. Do not prune preserved worktrees, backups, images or session data as routine cleanup.

[Full pre-housekeeping snapshot](history/HANDOFF-2026-10-31-before-housekeeping.md)
preserves detailed implementation/review/deployment history, including superseded claims.
The [older snapshot](history/HANDOFF-2026-10-25-before-housekeeping.md) remains available.
Current source, tests and timestamped deployment evidence outrank historical status prose;
[DECISIONS](DECISIONS.md) remains the ADR authority.

## Latest specification consolidation

C9 adds the canonical execution-state -> Telegram text/actions -> recovery matrix,
including not-started versus interrupted, safe Start/Continue and stale Stop
acknowledgement independent of edit success. C27-C29 supplement C18-C26. All remain
specification-only; source behavior and deployment are unchanged.
