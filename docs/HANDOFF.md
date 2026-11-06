# gopod — current handoff

**Last updated:** 2026-11-06 (Pi deploy, backlog and housekeeping complete)

Read this first; use [ROADMAP](../ROADMAP.md) for the
approved queue, [proposal registry](proposals/README.md) for CP status and
[ACCEPTANCE](ACCEPTANCE.md) for pending real-client verification.

**Acceptance authorization (2026-11-03):** The owner explicitly permits agents
to access Telegram and run gopod acceptance checks themselves, including test
messages, commands, attachments and supported button interactions, without
per-check permission. See [CLAUDE.md](../CLAUDE.md#workflow-rules). Older references
to "owner in Telegram" do not require handing automatable checks back to the
owner. No acceptance result or deployment status changes with this instruction.

## Resume here — next parallel reliability batch, 2026-11-06

Owner resumed implementation with the standing full release cycle. Base `8b53386`:
- CP-002: `fix/control-final-arbitration`, worktree `../gopod-control-arbitration`,
  agent freeze_next; scope control-repair versus final-edit arbitration and bounded
  More/Back lifecycle checkpoints. Planned per-message edit gate plus published
  outbox target ownership/current control revision checks; no schema change.
- CP-016: `feat/topic-ownership-next`, worktree `../gopod-topic-ownership`, agent
  topic_next; extend existing text ownership journal to exact private topic targets
  (not only main/fallback), retaining receipt repair/no-replay. No schema change;
  nontext/history/unknown reconciliation and retirement remain separate.
- CP-016 source checkpoint: registered-private topic text journal implemented,
  no schema change; PR #186 head `613edd3`, full checks/races and exact-head
  independent review PASS. CI `42209821093` PASS; merged as `6faa197`. Sonar OK, five complexity
  smells read, none suppressed. Unknown or ACK-failed sends stay held across restart; exact target
  validation rejects foreign routes while siblings/main remain available.
- CP-002 correction checkpoint: LIMIT100 starvation and obsolete Stop requeue
  fixed with deterministic regressions; repeated independent review/races PASS.
  Combined-head full checks and final SHA pending. Remote ambiguous repair effects
  remain an explicit foreground gate; local serialization is not remote proof.
- Independent reviewer review_pickers; root integrates global docs, required CI,
  merge, Pi controller rollout/acceptance and bounded housekeeping. No worker.

Foreground activation and topic deletion remain gated. Current Pi `80f4b11`.

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
All five source slices #178–#180/#182/#183 are deployed in `80f4b11`.

**First next action:** CP-002 legacy control repair versus reused final-edit
arbitration and More/Back control lifecycle checkpoints; then eager old-row cleanup,
mixed legacy/durable latest-row ordering and integrated preview/Stop/publication
qualification before foreground cutover. CP-016 retains historical/nontext and
topic-target ownership, unknown outcomes, managed/native exit qualification and
confirmed retirement orchestration. `/thread delete` stays unavailable.

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

- Source deployed: `80f4b11cc5077cde1df28afb0274b5f79b7bb7ba`;
  exact-main [CI 42198643997](https://github.com/Coriol-is/gopod/actions/runs/42198643997) passed.
- Documentation reconciled against source `261805e`; runtime is `80f4b11`.
- Pi: `ssh spaceinvaderz@192.168.1.128`; checkout `~/gopod`, data `/opt/gopod/data`,
  mounted workspace repository `/opt/gopod/repo`.
- Rollback image: `gopod-rollback:pre-80f4b11`; preserve it and all named backups.
- Matched-stopped backup: `20261106T102424Z-ms-44bebb93`.
- Guide originals: `~/gopod-backups/consumer-migration-757faa2`;
  cleanup inventory: `~/gopod-backups/cleanup-20261105-757faa2.json`.
- Release evidence: `/tmp/gopod-release-80f4b11` on Pi; durable summary is in the
  [deployment report](deployments/2026-11-06-reliability-cycle-rollout.md).
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
