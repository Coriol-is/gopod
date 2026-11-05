# gopod — current handoff

**Last updated:** 2026-11-05 (Pi 757faa2 deployed and backups trimmed)

Read this first; use [ROADMAP](../ROADMAP.md) for the
approved queue, [proposal registry](proposals/README.md) for CP status and
[ACCEPTANCE](ACCEPTANCE.md) for pending real-client verification.

**Acceptance authorization (2026-11-03):** The owner explicitly permits agents
to access Telegram and run gopod acceptance checks themselves, including test
messages, commands, attachments and supported button interactions, without
per-check permission. See [CLAUDE.md](../CLAUDE.md#workflow-rules). Older references
to "owner in Telegram" do not require handing automatable checks back to the
owner. No acceptance result or deployment status changes with this instruction.

## In flight — wait and durable control generation ownership

Branch `fix/wait-control-generation`, base `9de3cac`. Wait keys now include the
captured generation; stale cleanup must not consume a successor's placeholder or
join its refresher. Placeholder/control persistence is serialized with registration
replacement and rejects retired bindings. Repeated generation races and initial independent review pass. Full checks found
and fixed unregistered-placeholder compatibility; final build/vet/tests, full Telegram race (58.097 s) and delta review pass.
PR #179 source `699c0aa`; required CI and advisory Sonar pending.
[Evidence](reviews/2026-11-05-wait-control-generation.md).
No deployment or foreground activation.

## Resume here — preview controls merged, 2026-11-05

PR [#178](https://github.com/Coriol-is/gopod/pull/178) merged as `da8b4fe`.
Draft metadata and Stop-keyboard generation now bind to the captured registration;
a retired handler cannot overwrite/clear its replacement's draft or acquire its
Stop token. Full build/vet/tests, Telegram races, independent review and exact-head
required CI PASS; advisory Sonar OK (two existing code smells, no suppression).
[Review and evidence](reviews/2026-11-05-preview-generation-controls.md).

**First next action:** generation-bound wait-placeholder/refresher ownership and
durable control binding, then reply-action parity and qualified foreground cutover.
No implementation in flight. #178 is not deployed; Pi remains `757faa2`.

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
