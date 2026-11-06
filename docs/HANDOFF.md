# gopod — current handoff

**Last updated:** 2026-11-06 (parallel slices #182/#183 merged)

Read this first; use [ROADMAP](../ROADMAP.md) for the
approved queue, [proposal registry](proposals/README.md) for CP status and
[ACCEPTANCE](ACCEPTANCE.md) for pending real-client verification.

**Acceptance authorization (2026-11-03):** The owner explicitly permits agents
to access Telegram and run gopod acceptance checks themselves, including test
messages, commands, attachments and supported button interactions, without
per-check permission. See [CLAUDE.md](../CLAUDE.md#workflow-rules). Older references
to "owner in Telegram" do not require handing automatable checks back to the
owner. No acceptance result or deployment status changes with this instruction.

## Release checkpoint — 2026-11-06

Owner requested the permanent implementation/review/CI/merge/deploy/backlog/
housekeeping cycle; recorded in CLAUDE.md. Deploying exact `80f4b11` to Pi
controller only, no worker. Exact-main CI `42198643997` PASS. Live idle counters
zero and controller healthy; candidate build/backup/acceptance still pending.
Prior source `757faa2` remains deployed until verified switch. Preserve protected
backups and credentials; cleanup only integrated clean task branches/worktrees.

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
No implementation remains in flight. #178–#180/#182/#183 are not deployed;
last verified Pi remains `757faa2`, controller only.

**First next action:** CP-002 legacy control repair versus reused final-edit
arbitration and More/Back control lifecycle checkpoints; then eager old-row cleanup,
mixed legacy/durable latest-row ordering and integrated preview/Stop/publication
qualification before foreground cutover. CP-016 retains historical/nontext and
topic-target ownership, unknown outcomes, managed/native exit qualification and
confirmed retirement orchestration. `/thread delete` stays unavailable.

## Merged delivery prerequisites — pending rollout

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

## Last deployment — Pi rollout and cleanup complete, 2026-11-05

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
Before foreground cutover, complete integrated preview/Stop/publication qualification,
reply-action lifecycle parity and legacy control-repair arbitration for reused final
edit targets. The completed bounded callback slice is described above. `WithResultFreeze` and `PrepareFrozenFinalReply` still
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

- Source deployed: `757faa22ec28c6e1f025d9943924637d2e2dc88b`;
  exact-main [CI 42050073211](https://github.com/Coriol-is/gopod/actions/runs/42050073211) passed.
- Documentation reconciled against source `261805e`; runtime remains `757faa2`.
- Pi: `ssh spaceinvaderz@192.168.1.128`; checkout `~/gopod`, data `/opt/gopod/data`,
  mounted workspace repository `/opt/gopod/repo`.
- Rollback image: `gopod-rollback:pre-757faa2`; preserve it and all named backups.
- Matched-stopped backup: `20261105T151122Z-ms-6ff01519`.
- Guide originals: `~/gopod-backups/consumer-migration-757faa2`;
  cleanup inventory: `~/gopod-backups/cleanup-20261105-757faa2.json`.
- Release evidence: `/tmp/gopod-release-757faa2` on Pi; durable summary is in the
  [deployment report](deployments/2026-11-05-stop-ipc-rollout.md).
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
