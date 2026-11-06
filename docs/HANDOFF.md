# gopod — current handoff

**Last updated:** 2026-11-06 (durable placeholder deletion in progress)

Read this first; use [ROADMAP](../ROADMAP.md) for the
approved queue, [proposal registry](proposals/README.md) for CP status and
[ACCEPTANCE](ACCEPTANCE.md) for pending real-client verification.

**Acceptance authorization (2026-11-03):** The owner explicitly permits agents
to access Telegram and run gopod acceptance checks themselves, including test
messages, commands, attachments and supported button interactions, without
per-check permission. See [CLAUDE.md](../CLAUDE.md#workflow-rules). Older references
to "owner in Telegram" do not require handing automatable checks back to the
owner. No acceptance result or deployment status changes with this instruction.

## Resume here — registered-private placeholder deletion, 2026-11-06

Owner resumed the next bounded CP-002 slice after D070. The sole production
`DeleteMessage` call removes the sync voice-answer wait placeholder; currently it
has no durable attempt, and an unknown deletion can reach Finished editing/TTS.
Implementation is in `feat/durable-delete-outcomes`, sibling worktree
`gopod-delete-outcomes`, based on `e8c9757`. The scope is durable provenance and
commit-before-HTTP deletion evidence, mutual target barriers, exact settlement,
restart/backup/quiescence coverage and outer NoReplay propagation. No group,
foreground, worker or topic deletion activation. Independent review and all release
gates follow implementation; runtime remains `6820422`.

Implementation checkpoint: separate `telegram_delete_attempts` ledger is wired
through the message gate/topic lease. True ACK retires only its captured control;
a durable tombstone blocks later writes without claiming text delivery. Pending
attempts enter backup/idle/quiescence. Initial existing repair/edit tests pass;
dedicated fault/restart/voice regressions are in progress. Registration-removal regression checks the shared raw-target gate before the
compatibility path. Independent review found and corrected a global recovery
stall on deletion receipt settlement failure: unrelated controls continue while
only the affected target stays held. Fairness coverage places an eligible control
after more than 100 held targets. Initial full build/vet/tests PASS (Telegram80.892s, Store56.220s), affected
Store/Telegram/backup/control races PASS. Final test-only fairness and fallback
budget refinements are being revalidated, with independent focused race review.
A negative overlay restoring the raw delete path fails the false/nil regression
by invoking paid generation, confirming the new assertion catches the defect.
Pi baseline was rechecked: `6820422`, healthy/no restarts/OOM, ten idle counts zero.

Source PR #193 is open on `29f7a7659b025263074cd0bc0aab5ff8f826f000`.
Independent exact-head review PASS, focused race Store8.331s/Telegram8.073s PASS.
Final full build/vet/tests and four-package races PASS on the committed head.
Required CI `42238010448` is running. Exact-head Sonar at 14:53:54 UTC is ERROR:
new duplication8.8% vs3%, predominantly regression tests;33 code smells,
zero bugs/vulnerabilities, all read and retained. Advisory soft-gate policy applies.
Next action: final local revalidation and required PR CI, merge, Pi-only
deployment/acceptance, then docs PR #192 and housekeeping.
The previous D070 docs PR #190 merged as `e8c9757`; its branch is removed and both
retained worktrees were clean at cycle start.

## Previous completed D070 — deployed, 2026-11-06

#191 (`6820422`, D070) journals registered-private direct text/HTML/keyboard edits
with durable creation/control/ACKed-outbox provenance. Pending direct/repair/outbox
effects mutually fence the physical target across restart; exact receipts settle
SQLite only. Unknown edits prevent outer fallback/replacement/TTS. Historical
private targets without provenance hold truthfully; no blanket overwrite authority.
[Review](reviews/2026-11-06-durable-edit-outcomes.md).

Full build/vet/tests and affected Store/Telegram/backup/control races PASS;
independent exact-head review and CI `42232618774` PASS on `a47d53b`.
Sonar OK,26 smells read/retained. Pi controller `6820422` passed exact-main CI
`42233254945`, isolated readiness, pre/post-migration backup verification, unchanged
state hashes, health and Telegram version. Ten idle counters zero, including
`direct_edits_pending`; ledger exists. No worker or foreground activation.
[Release and housekeeping](deployments/2026-11-06-direct-edit-rollout.md).
D070 implementation is complete; its integrated feature worktree and branch removed.
Protected backups/auth/rollback retained; D070 docs branch was removed after #190 merge.

**Next:** groups/unregistered edits, DeleteMessage and other non-edit effects,
historical missing provenance and explicit unknown reconciliation. Eager old-row
cleanup, mixed-path ordering and integrated foreground qualification remain open.
D070 covers direct edits in registered private scope, not full CP-002/CP-016.

## Previous completed repair journal — 2026-11-06

PR #189 (`a85b328`, D069) journals each control repair before HTTP, preserves
unknown outcomes across restart/rebinding/retention, and holds conflicting message
edits, outbox claims, relevant topic quiescence and backup idle. Exact positive or
definite-unsent receipts settle SQLite only; no automatic replay/force-clear.
[Source review and validation](reviews/2026-11-06-durable-repair-outcomes.md).

Full build/vet/tests, Store/Telegram/backup/control races, independent exact-head
review and required PR CI `42216574391` PASS on `e329bf2`. Sonar OK,30 smells read,
none suppressed. Pi controller `a85b328` passed exact-main CI `42217121239`,
isolated readiness, pre-rollout matched-stopped backup, post-migration database
backup, health/state preservation and Telegram version acceptance. No worker.
Nine idle counters zero including `control_repairs_pending`; ledger exists.
[Release and housekeeping](deployments/2026-11-06-durable-repair-rollout.md).
D070 above is now deployed; one integrated clean agent worktree and feature branch
removed. D069 evidence must be preserved on downgrade; no automatic unknown clearing.

**Remaining:** historical pre-journal repair evidence and explicit resolution of
unknowns without positive evidence, remaining groups/unregistered and non-edit effects after D070, eager old-row
cleanup, mixed-path ordering and integrated foreground qualification. CP-016
retains nontext/history/managed/native/retirement gaps. No foreground activation.

## Previous completed control/topic batch — 2026-11-06

- #187 (`0cd8078`): per-message edit arbitration protects published final targets
  from subsequent legacy repair/writes; More/Back checkpoint old control retirement.
  CAS scheduling prevents LIMIT100 starvation; obsolete Stop cannot requeue an
  ACKed terminal control. [Report](reviews/2026-11-06-control-final-arbitration.md).
- #186 (`6faa197`): existing durable ownership journal covers private topic-target
  text with exact source/target validation and receipt-only repair/no-replay.
  [Report](reviews/2026-11-06-topic-ownership-next.md). No schema change.

Full build/vet/tests, affected races, independent exact-head review and required
PR CI PASS. Sonar OK for both (nine/five smells retained). Pi controller `0cd8078`
passed exact-main CI `42210762626`, isolated readiness, matched-stopped backup
verification, health/state preservation and Telegram version acceptance. No worker.
[Release, acceptance and housekeeping](deployments/2026-11-06-control-topic-rollout.md).
The D069 follow-up above is also deployed. Two clean integrated agent worktrees and their
local/remote branches removed; protected backups/auth/rollback retained.

**Next implementation:** preserve unresolved historical/non-repair gaps before
foreground activation; the D069 barrier above is implemented. Eager row
cleanup, mixed-path latest ordering and integrated preview/Stop/publication remain.
CP-016 still needs nontext/edit attempts, historical ownership/retention, unknown
reconciliation, managed/native settlement and confirmed retirement orchestration.

## Previous completed cycle — 2026-11-06

Permanent development cycle is recorded in [CLAUDE.md](../CLAUDE.md#workflow-rules):
review/CI/merge → Pi controller deploy/acceptance → backlog/docs/handoff → housekeeping.
Deployment `80f4b11` passed exact-main CI `42198643997`, isolated readiness, verified
matched-stopped backup, health/state preservation and Telegram version acceptance.
Controller only; no worker. [Release and cleanup evidence](deployments/2026-11-06-reliability-cycle-rollout.md).
Two integrated clean worktrees and six local/remote branches removed. Protected
backups/auth/rollback and unrelated services preserved. New work is listed above.

## Completed parallel slices — 2026-11-06

- CP-002/003: #183 (`261805e`) adds restart-safe More/Back/As file from fully
  acknowledged immutable results. Exact scope/topic/message/markup checks reject
  partial, uncertain, compacted, bare-keyboard and superseded rows. Original text
  survives process-local memo loss. A review-found pre-HTTP failure classification
  bug was fixed and regression-tested. [Report](reviews/2026-11-06-reply-actions-ack.md).
- CP-016: #182 (`0ac9b3f`) journals restart/lifecycle/IPC noncontrol main-text
  notices, preserving fallback origin and no-replay on unknown/ACK-failed sends.
  [Report](reviews/2026-11-06-topic-retirement.md).

Both passed full build/vet/tests, affected races, independent exact-head review
and required CI: #183 `42197591379` on `ef3dbd0`; #182 `42196527739` on `752120d`.
Sonar gate OK for both, six and two smells respectively, zero bugs/vulnerabilities;
findings read, none suppressed. Reports record exact heads and analysis times.
All five source slices #178–#180/#182/#183 first deployed in `80f4b11` and remain included.

## Deployed delivery prerequisites

- #178 (`da8b4fe`): draft metadata and Stop keyboard use captured registration.
- #179 (`bbc30b4`): generation-scoped wait maps/refresher joins and durable control
  binding reject stale writes while preserving unregistered-sender compatibility.
- #180 (`6722e6a`): committed bare-keyboard attempt after explicit refusal, composing
  HTML/plain alternatives without retrying ambiguous outcomes.

Full checks, affected races, independent review and required CI passed.
[#178 review](reviews/2026-11-05-preview-generation-controls.md) ·
[#179 review](reviews/2026-11-05-wait-control-generation.md) ·
[#180 review](reviews/2026-11-05-outbox-keyboard-fallback.md).
Sonar #180 was ERROR on reviewed `6dde8c9`: duplication 22.4% versus 3%,
18 smells and zero bugs/vulnerabilities. None suppressed; soft-gate policy applied.
Before downgrading a future release containing #180, drain bare-keyboard selections
or hold dispatch and preserve the ledger; see the
[runbook](deployments/BACKUP-RESTORE.md#durable-keyboard-fallback-compatibility-2026-11-05).

## Previous deployment — 2026-11-05 (historical evidence)

Pi controller deployed as `757faa2` at 15:11:59 UTC; no worker deployment.
PRs #173–#177 are now deployed. Exact-main CI passed, candidate isolated readiness
passed, matched-stopped backup `20261105T151122Z-ms-6ff01519` verified. Controller
and owner runtime healthy, zero controller restarts/OOM; chat/session/task hashes
unchanged. Telegram `/version` response `3177169` confirms 757faa2 (Go 1.27.3).
[Rollout, acceptance and cleanup evidence](deployments/2026-11-05-stop-ipc-rollout.md).

Three existing scheduling guides received an explicit D068 matching section;
originals/content preserved. Actual shipped Node consumer on Pi passed read-only
list, identical replay and changed-byte conflict, all hash-matched; 15 tasks
unchanged and claim files empty. This is bounded host/consumer acceptance, not
qualification of arbitrary old consumers or native/visual/power-loss behavior.

Owner-authorized cleanup removed 18 old automatic database-only sets (~119 MiB).
All 20 matched-stopped, three database-only anchors, manual/auth baselines,
rollback images and guide originals retained; backup directory now 275 MiB.
Separate 556 KiB NanoClaw/OneCLI/systemd archives retained. No broad Docker prune.

## Next implementation and acceptance

Use [active backlog](BACKLOG.md) for remaining scope and
[ROADMAP](../ROADMAP.md#execution-priority--2026-11-04) for order.
Before foreground cutover, complete historical/group/unregistered/non-edit uncertainty reconciliation,
eager cleanup, mixed-path ordering and integrated preview/Stop/publication qualification.
Local arbitration and D069 exact-evidence repair settlement are implemented; unsupported
unknowns remain held. The completed bounded callback slice is described above. `WithResultFreeze` and `PrepareFrozenFinalReply` still
have no production callers. CP-016 `QuiesceTopic` has no production caller either;
`/thread delete` is unavailable. Do not reopen completed #168–#177 slices.

Managed-task credentials remain owner-deferred and activation off. CP-037 P2–P7
and T5/T5b remain paused; P0/P1 are deployed in legacy mapping. T9 cgroup/reboot
work is complete. CP-007 stays below the product queue; manual backup cleanup
is not automatic retention or qualified full restore.

CP-040 A22–A25, ordinary Stop/restart, CP-020 provider/client and CP-038 Claude
browser-completion gates remain in [ACCEPTANCE](ACCEPTANCE.md). Existing Telegram
authorization applies; coordinate a single actor. No forced crash, visual/native
or full restore qualification was added by the latest rollout or this docs audit.

## Release and recovery pointers

- Source deployed: `682042284175b7a9559acb3b5ed32a126ace64d4`;
  exact-main [CI 42233254945](https://github.com/Coriol-is/gopod/actions/runs/42233254945) passed.
- Documentation reconciled against source/runtime `6820422`.
- Pi: `ssh spaceinvaderz@192.168.1.128`; checkout `~/gopod`, data `/opt/gopod/data`,
  mounted workspace repository `/opt/gopod/repo`.
- Rollback image: `gopod-rollback:pre-6820422`; preserve it and all named backups.
- Matched-stopped backup: `20261106T142641Z-ms-fdaef581`; new-schema database
  backup: `20261106T142752Z-db-949e93c5`.
- Guide originals: `~/gopod-backups/consumer-migration-757faa2`;
  cleanup inventory: `~/gopod-backups/cleanup-20261105-757faa2.json`.
- Release evidence: `/tmp/gopod-release-6820422` on Pi; durable summary is in the
  [deployment report](deployments/2026-11-06-direct-edit-rollout.md).
- Preserve task claim spool and SQLite ledgers before any downgrade; complete
  IPC restore remains unqualified. Status-only legacy consumers remain unqualified.
- If SSH Git authentication fails, use per-command HTTPS with `gh auth git-credential`;
  explicit URL/refspec fetch requires `--no-prune`. Do not change global auth settings.

## History and documentation maintenance

[Completed/rejected/superseded ledger](history/BACKLOG-2026-11-05-completed.md) ·
[full previous handoff](history/HANDOFF-2026-11-05-before-housekeeping.md).
Historical next-action and undeployed statements in snapshots are not current instructions.
Update this file and the active backlog at implementation/review/deployment checkpoints;
archive completed slices without closing unverified parent acceptance gates.
