# gopod — current handoff

**Last updated:** 2026-10-31 (private Stop merged and deployed)

Read this first; use [ROADMAP](../ROADMAP.md) for the
approved queue, [proposal registry](proposals/README.md) for CP status and
[ACCEPTANCE](ACCEPTANCE.md) for pending real-client verification.

## Resume here — private Stop deployed, 2026-10-31

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

## Decisions already taken — do not re-ask

- Owner-private chat/sessions retain their broad authority; group budgets do not narrow owner-private work.
- Task-role credential adapter deferred; recurring tasks continue on the existing main-session scheduler.
- Gmail/Calendar connectors allowed in private chat. CP-033 uses Europe/Belgrade with a picker,
  conservative hourly urgent checks, English UI, and quiet hours mean no run; owner-edited tasks
  are outside preset quiet hours. Presets ship off by default; do not reset owner settings.
- CP-034: official-source skill/plugin installation from private chat only, auto-update on, never groups.
- CP-028 direction: Go/gotd, read and write, private chat only; its seven spec questions remain open in PR #108.
- No automatic paid fallback. T9 memory-cgroup/reboot and parallel threads remain paused.

## Open gates and boundaries

- Artifacts and managed-task flags remain off. Artifact activation needs matched DB/artifact backup,
  restore qualification, and artifact leases if using online matched backups; decide whether to
  separate image delivery from code/voice activation.
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
