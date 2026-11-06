# gopod — current handoff

**Last updated:** 2026-11-06 (CP-002/003 and CP-016 delegated in parallel)

Read this first; use [ROADMAP](../ROADMAP.md) for the
approved queue, [proposal registry](proposals/README.md) for CP status and
[ACCEPTANCE](ACCEPTANCE.md) for pending real-client verification.

**Acceptance authorization (2026-11-03):** The owner explicitly permits agents
to access Telegram and run gopod acceptance checks themselves, including test
messages, commands, attachments and supported button interactions, without
per-check permission. See [CLAUDE.md](../CLAUDE.md#workflow-rules). Older references
to "owner in Telegram" do not require handing automatable checks back to the
owner. No acceptance result or deployment status changes with this instruction.

## Resume here — active parallel workstreams, 2026-11-06

Owner requested backlog items 1–2 in agents. Both started from `6c20646` in isolated
worktrees. CP-016 is merged; CP-002/003 remains under review.

- CP-002/003: `feat/reply-actions-ack`, worktree `../gopod-reply-actions`, agent
  `freeze_next`. Scope: no-schema callback read model from fully ACKed frozen results; exact
  scope/topic/message checks, restart-safe More/Back/As file, conservative stale-row
  handling. Mixed legacy ordering and eager row cleanup stay separate.
- CP-016: `feat/topic-retirement-next`, worktree `../gopod-topic-retirement`, agent
  `topic_next`. Scope: ownership/no-replay for restart/lifecycle and IPC noncontrol text
  notices, including original-topic to main fallback; reuse existing receipt repair.
  Historical/nontext/unknown outcomes remain separate; delete command stays off.
- CP-016 checkpoint: PR #182 merged as `0ac9b3f`; reviewed head `752120d`.
  Full build/vet/tests, affected race, independent review and required CI
  `42196527739` PASS.
  Sonar gate OK, two complexity smells read, none suppressed.
- Root integrates reports, HANDOFF/BACKLOG/ROADMAP and review. Agents own separate
  source slices and checkpoint reports. Coordinate overlapping Store/schema files.

No deployment, live deletion, managed-task activation or foreground cutover is
implied. #178–#180 and #182 await deployment; last verified Pi remains `757faa2`.

## Previous completed slice — keyboard fallback, 2026-11-05

PR [#180](https://github.com/Coriol-is/gopod/pull/180), `6722e6a`, implements
outbox fallback after explicit keyboard refusal. The bare-keyboard attempt is
committed before HTTP; text/route/targets remain immutable. HTML/plain fallback
composes both ways; ambiguous outcomes are not retried. Full checks, Store/
Telegram races, independent review and required exact-head CI PASS.

Advisory Sonar on `6dde8c9`: **ERROR**, new-code duplication 22.4% versus 3%;
18 code smells, zero bugs/vulnerabilities. Findings read, none suppressed;
merge used the documented soft-gate policy. [Evidence](reviews/2026-11-05-outbox-keyboard-fallback.md).

**First next action:** reply-action memo/callback parity after durable ACK
(More/Back/As file and newest-row handling), then integrated preview/Stop/publication
qualification and foreground cutover. The parallel workstreams above are active. #178–#180 are
not deployed; Pi remains `757faa2`. Before downgrading a future deployment of #180,
drain bare-keyboard selections or hold the dispatcher and preserve its ledger;
see [runbook](deployments/BACKUP-RESTORE.md#durable-keyboard-fallback-compatibility-2026-11-05).

## Previous completed slice — generation ownership fixes, 2026-11-05

PR [#178](https://github.com/Coriol-is/gopod/pull/178), `da8b4fe`, binds draft
metadata and Stop keyboards to the captured registration.
PR [#179](https://github.com/Coriol-is/gopod/pull/179), `bbc30b4`, extends this to
wait-placeholder/refresher ownership and durable control binding. Late old HTTP
results cannot replace successor metadata; concurrent refresher stops both join.
Unregistered sender compatibility is preserved without successor recapture.

Both passed full build/vet/tests, Telegram races, independent review and exact-head
required CI. Latest Sonar gate OK (16 code smells, zero bugs/vulnerabilities;
findings read, none suppressed). [#178 review](reviews/2026-11-05-preview-generation-controls.md) ·
[#179 review and regression evidence](reviews/2026-11-05-wait-control-generation.md).

**Remaining gates:** reply-action parity and integrated preview/control lifecycle
qualification, then qualified foreground producer/cutover under D063/D065/D066.
These bounded fixes do not establish full native/client qualification or published
delivery cancellation settlement. No implementation in flight. #178–#179 are not
deployed; Pi remains `757faa2`. CP-016 remains the separate retirement lane;
backup stays lowered, managed credentials and parallel execution deferred.

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

**Remaining sequence:** CP-002 preview/control generation ownership and
reply-action parity, then qualified foreground cutover. CP-016 retirement remains
a separate lane. Backup stays lowered; managed credentials/parallel execution
remain deferred. Foreground producer disconnected, managed tasks off. No source
implementation in flight. Preserve new ledgers/spool before any downgrade; complete
IPC restore remains unqualified. Canonical and session worktrees retained.

## Next implementation and acceptance

Use [active backlog](BACKLOG.md) for remaining scope and
[ROADMAP](../ROADMAP.md#execution-priority--2026-11-04) for order.
Before foreground cutover, implement general preview/control generation ownership
and reply-action parity. `WithResultFreeze` and `PrepareFrozenFinalReply` still
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
- Documentation reconciliation starts from `a4078ec`; no application source changes.
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
