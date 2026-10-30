# gopod — current handoff

**Last updated:** 2026-10-30 (context-reset checkpoint)

Read this first; use [ROADMAP](../ROADMAP.md) for the
approved queue, [proposal registry](proposals/README.md) for CP status and
[ACCEPTANCE](ACCEPTANCE.md) for pending real-client verification.

## START HERE — checkpoint 2026-10-30 (written before a Claude context reset)
**State.** `main` = `c5b976e`, no open PRs, no extra worktrees, no unmerged local branches. The Pi (192.168.1.128,
`ssh spaceinvaderz@192.168.1.128`; Tailscale name `rpi4` may be offline; needs the 1Password SSH agent unlocked) runs
`c5b976e`. Flags on: `GOPOD_OUTBOX_DISPATCHER`, `GOPOD_OUTBOX_IPC` (owner chat). Off: artifacts, managed tasks.
Last backup before deploy: `20261029T235150Z-db-480ab40a` (online `gopod backup create`, verified); rollback tag
`gopod-gopod:rollback-pre-trigger-picker` (older tags `rollback-pre-*` also exist).

**Shipped today (all merged and deployed):** CP-002 PR1-5 and rich formatting (D045), CP-004 PR1-3, CP-007 backup
foundation, `/restart` (D046), default group admission (D047: registered groups run without a config entry;
auto-prepare; legacy group `tg:-1003850302017` prepared as epoch e2), Codex tree-budget fix, group triggers +
owner-in-group identity (D048), `/trigger` picker buttons, picker-first convention (CLAUDE.md "Commands with
parameters"). Owner verified live: `/trigger`, `/provider`, group replies on Claude (new + resume).

**How to deploy (what worked):** fast-forward `~/gopod` on the Pi (`git fetch && git merge --ff-only origin/main`),
`docker tag gopod-gopod:latest gopod-gopod:rollback-pre-<topic>`, `docker compose build --build-arg VERSION=<sha> gopod`,
backup with `gp backup create` + `gp backup verify <id>` where `gp() { docker compose run --rm --no-deps -v
$HOME/gopod-backups:/backups -e GOPOD_BACKUP_DIR=/backups gopod "$@"; }` (define it as a FUNCTION: the remote shell is
zsh and a `$GP` variable does not word-split), then `docker compose up -d --no-build gopod`, check `docker logs`.
Push from the Mac over HTTPS when SSH is down: `git -c credential.helper= -c credential.helper='!gh auth git-credential'
push https://github.com/Coriol-is/gopod.git <branch>:<branch>`. Flow: worktree -> PR -> independent review agent ->
CI -> `gh pr merge --squash --admin --delete-branch` (ruleset needs one approval; admin path is the working one).

**Owner decisions (answered 2026-10-30):**
1. Task credential adapter: take the recommendation (B2, task-only auth dir with the access token only). The spec's other
   questions use their recommended answers: [spec](superpowers/specs/2026-10-29-task-role-credential-adapter.md). Not built yet.
2. [Agent buttons and hooks](superpowers/specs/2026-10-29-agent-buttons-and-hooks.md): still open (5 questions).
3. Group `/login` prompt: dropped (branch `feat/group-login-no-prompt`): the owner's group `/login` now sends the real
   step privately and a neutral line to the group; the private confirmation, token maps and `login.group.confirm`
   are removed.
4. Agent reading group chat history: not now.
5. The four merged remote branches were deleted on GitHub.

**Decisions already taken by the owner (do not re-ask):** groups keep trigger `all` (new groups `mention`); CI is not
mandatory (no branch protection); thread-targeted sends only for threads in the private chat; stickers are context
only; reactions are readable on request (CP-032, not built); lower paranoia for the owner's own actions but keep the
boundary against group members; keep the private-chat approve mechanism; commands with set-valued parameters use
picker buttons.

**Next candidates (in order):** (a) live two-turn Codex canary in the group (owner already switched provider; check
logs), (b) answers to the two spec question sets -> implement buttons phase B0/B1 or the task credential adapter,
(c) CP-002 PR6-8 (foreground freeze/streaming ownership, protected challenges, staged release), CP-004 PR4-5,
(d) stickers P2/P3, thread awareness (private chat), CP-032 reaction reading, (e) group-triggers follow-ups, (f)
live acceptance A08-A11/A18/A20 in Telegram (owner), freefeed digest 09:00 Belgrade is the first unattended check
of the task/IPC path.

**Not authorized without the owner:** account logins/refresh, paid fallbacks, T9 reboot, schedule changes, enabling
`GOPOD_OUTBOX_ARTIFACTS` or `GOPOD_MANAGED_TASKS` (both have unmet gates: restore drill + capability issuer; task
credential adapter + Pi Docker-inspect round trip), changing Telegram privacy/admin rights.

## Latest design request (2026-10-29)
- CP-032 [reaction-reading specification](superpowers/specs/2026-10-29-telegram-reaction-reading.md): group emoji/custom-ID events, durable admission, exact message/topic mapping and no-admin/unavailable diagnostics. Private native reactions are transport-blocked; no account-reader workaround is authorized. Design only; no source or runtime flags changed.
- Task-role credential adapter [design spec](superpowers/specs/2026-10-29-task-role-credential-adapter.md): why `GOPOD_MANAGED_TASKS=1` denies every task, options A-D, recommendation (access-token-only task auth copy), live gates and 5 owner questions. Design only; no source or flags changed.
- Owner reports live `/restart` checked successfully on 2026-10-29; this establishes the reported baseline, not every negative/topic/provider scenario.

## Group triggers, owner-in-group, group login guidance (branch `feat/group-triggers-owner`, unmerged, 2026-10-29)
- ADR [D048](DECISIONS.md), spec [group trigger and owner-in-group](superpowers/specs/2026-10-29-group-trigger-and-owner-in-group.md).
  Store `group_triggers` (additive), `PermGroupOwner` + `Caller.IsGroupOwner`, `/trigger`, the gate in
  `telegram/group_trigger.go`, private confirmation of group `/isolation enable|prepare` (`Response.Private`),
  group sign-in guidance (`telegram/group_login_guidance.go`), per-turn host line `[gopod group: ...]`.
- Production impact: none to configure. The existing group has no row and keeps `all`; owner sends
  `/trigger mention` (or another mode) in the group to change it. New groups start at `mention`.
- The private confirmation prompt of group `/login` was removed by D049 (owner decision 2026-10-30). An album answers only if its
  first message qualifies. Existing workspaces keep their seeded instruction files; the per-turn line carries
  the mode for them.

## Default group admission (branch `feat/group-default-admission`, not merged, 2026-10-29)
- Owner decision, ADR [D047](DECISIONS.md): registered groups are admitted without a `group-runtime.json`
  entry. `AdmissionMode` (`registered` default / `allowlist`), `MaxGroups` (default 50), automatic
  Prepare+Enable on `/register`, at startup and on the first denied admission, owner kill switch
  `/isolation disable`, one neutral rate-limited group notice plus a private owner notice on refusal.
- Not done here and still gating real use: refresh the expired Claude auth, the Codex tree-budget fix
  (separate branch), live two-turn canaries on the Pi. No production change, push or deploy was made.
- Production config needs no edit: omitted `AdmissionMode` means registered; old `EnabledGroups` is ignored.
- Touched near the group `/login` private-confirmation path only by adding a regression test
  (`internal/telegram/group_refusal_test.go`); `login_handoff.go`, `login.go`, `confirm.go` are unchanged.

## Codex tree-budget admission fix (2026-10-29, branch `fix/codex-group-tree-budget`, unmerged)
- **Root cause:** pinned Codex creates `CODEX_HOME/tmp/arg0/codex-arg0<id>/` containing the
  symlinks `apply_patch`, `applypatch` (Linux also `codex-linux-sandbox`, `codex-execve-wrapper`)
  and a `.lock` on every start and leaves them behind after exit (also when killed); its janitor only
  removes them at the next start, after gopod's walk. The single-turn run left them, so the next
  `Start` hit the **symlink rule** in `privateTreeBudget` on the native tree, before any inference.
  The number rules are not implicated: a fresh group tree (plugins/apps/skill search disabled) is
  well under 64 MiB / 2048 files; the owner's personal `.codex` is 160 MiB / 5960 files only because of
  plugin clones and remote catalogs that group config disables. No FIFO/socket/non-symlink found.
- **Evidence:** local, not Pi. Codex 0.165.0 (pinned is 0.164.0, same mechanism) run offline with
  a temp `CODEX_HOME`: `--version` alone leaves `tmp/arg0*/codex-arg0*/{.lock,apply_patch,applypatch}`;
  an interrupted `exec` also created SQLite `-wal`/`-shm` files, `sessions/`, `skills/.system`,
  `thread-writer-locks/`. Read-only Pi inspection (`find`/`du` metadata only) of the owner's
  `.codex`: no symlinks, FIFOs or sockets, max depth 15, WAL files present. No Pi group namespace
  existed to inspect (qualification data already cleaned), so the failing tree itself was not observed;
  the attribution is inferred from the local reproduction and the stage evidence. Linux alias names
  come from Codex's arg0 code (not re-verified on the Pi).
- **Also fixed:** the depth test used `filepath.SplitList` (splits on `:`), so it never fired; it now
  counts `/` components (limit 64 unchanged). Walk is entry-capped so growth or loops terminate.
- **Diagnosable:** `TreeBudgetError{Tree, Reason, Class}` -> host `slog` warning -> `AdmissionError.Detail`
  -> canary `admission_detail=`. Group chats still see only the generic unavailable text.
- **Owner-directed relaxation (same branch):** limits raised to native 512 MiB / 20000 files and
  workspace 256 MiB / 5000 (runaway-growth guards, not security limits; depth cap 64 kept). A planted
  symlink/FIFO/socket in the native or workspace tree is now unlinked via `unlinkat` on the verified
  parent fd (no follow, no recursion), warned and counted, then admitted; only a failed removal
  (`heal`) or a bytes/files/depth/walk violation refuses. Policy tree never heals; owner-private
  note not implemented (log only). `/login`, control confirm and login handoff untouched.
- **Review fixes (independent review of 24958a8):** healing moved out of `Authorize` (check-only,
  admits healable entries untouched) into `Start` under the namespace lock after reconcile; ENOENT
  counts as healed; classification/unlink use the verified parent fd (`fstatat`/`openat O_NOFOLLOW`)
  and the root is opened `O_NOFOLLOW`; arg0 aliases must point at a `codex` binary under the
  image's npm prefix (exact path unverified on the Pi). Workspace symlink healing kept because
  `telegram/media.go` writes into the host workspace by path; see ISOLATION.md for the audit.
- **Boundary (strict part):** no-follow rooted walk (Lstat + same-inode check on every directory
  open); the only symlinks tolerated are the four exact alias names at exactly depth 5 under
  `codex/tmp/arg0/codex-arg0<id>/`, native tree only, never followed. Claude native state may leave
  its own entries; not observed here.
- **Still required (owner/orchestrator):** rebuild the Codex group image and controller from the
  merged fix and rerun the full two-turn (new + resume) Codex canary on the Pi; confirm with
  `admission_detail=` if it still fails. Claude qualification still needs a refreshed subscription.

## Resume here (2026-10-29, Codex continuation)
- Latest Claude session `17a9d2c0-7ae3-4ea8-bdf2-a4af91a2ebe5` stopped on a usage limit.
  Its P1 formatting child stopped after read-only exploration; the clean worktree was resumed in Codex.
- CP-004 PR2/PR3 (#79/#81), CP-007 foundation (#80), and rich-message requirements (#82) are merged.
  Base for this continuation: `19ae2e2`. Old branch-status notes below are historical implementation notes.
- P1 merged as `3fcbf06` in PR #83 after green CI and independent review.
  Owner `/restart` merged as `8bdff46` in PR #84; both features are deployed.
  Owner authorized PRs and automatic admin squash merge after green CI (ruleset 24430037 requires
  one approval; native auto-merge is disabled). The worktrees of the merged branches were removed after verification on 2026-10-29 night.
- P1 implements Markdown-default / sanitized HTML / plain text for personal durable IPC, shared canonical
  HTML validation, explicit rich-field rejection on unsupported routes, and one recorded plain fallback
  after an explicit Telegram entity-parse refusal. The original plan stays immutable; ambiguous sends
  remain uncertain and are never replayed. ADR D045. Legacy messages without rich fields stay literal.
- Production: `8bdff46` deployed on 2026-10-29 at 19:11 UTC; see [evidence](deployments/2026-10-29-8bdff46.md).
  Owner-personal durable text IPC and dispatcher are enabled. Two Markdown/HTML smoke messages
  received Telegram acknowledgements; all schedules and data counts were preserved. Groups,
  artifact delivery and managed tasks remain disabled.
- Next: owner-client formatting inspection and remaining restart edge cases. The owner has checked the baseline `/restart`. API acknowledgements and startup readiness do not establish these live acceptance gates.
- **Group admission live 2026-10-29 (late):** Pi runs `2543950` (D047 default group admission + tree-budget fix #88);
  first online `gopod backup create` set `20261029T222618Z-db-63737a76` (verified), rollback tag
  `gopod-gopod:rollback-pre-group-admission`. The legacy group `tg:-1003850302017` was prepared automatically
  (epoch `e2`); after the owner re-logged in Claude in the private chat, new and resume group turns ran on Claude.
  Owner-only commands from the group are denied because ownership is by chat (see
  [group trigger and owner-in-group spec](superpowers/specs/2026-10-29-group-trigger-and-owner-in-group.md)).
  Group `/login` always fails by design (groups use the owner's account); fix and trigger modes are specified, not built.
  Still open: live two-turn Codex canary in the group, trigger modes, owner-in-group commands, stickers, buttons spec,
  thread awareness.
- **Status sync 2026-10-29 night:** `main` = `079e5ea` (PRs #70-#86 merged, none open, no extra worktrees or
  unmerged local branches). Production `8bdff46`, restart count 1 is the owner's graceful `/restart`
  (19:18 UTC, exit 0). Flags on: `GOPOD_OUTBOX_DISPATCHER`, `GOPOD_OUTBOX_IPC` (owner chat, thread 0).
  Off: artifacts, managed tasks, groups. Observed since deploy: Claude usage-limit error at 19:19 UTC
  (quota, not a defect) and a denied `/restart` from the disabled group (expected). The freefeed digest
  (09:00 Belgrade) is still the first unattended check of the post-deploy task/IPC path.
  Four merged remote branches (`docs/deploy-rich-restart`, `docs/reaction-reading`,
  `feat/rich-messages-formatting`, `feat/telegram-restart`) are left for the owner to approve deleting.
- Not authorized here: account logins, paid fallbacks, T9 reboot, group activation or schedule changes.

## Telegram restart (merged #84, deployed 2026-10-29)
- CP-031 / D046: branch `feat/telegram-restart`, worktree `gopod-telegram-restart`.
  `/restart` is owner/private-only through the Router; it records a replay-fenced
  request, requires an acknowledged initiating-thread notice, verifies Docker
  Compose supervision, gracefully drains and recycles exact old personal container
  IDs. Startup reports ready/degraded only after local runtime checks.
- Local build, vet (native and Linux ARM64), full tests and restart-related race suites passed.
  Merged in PR #84 and deployed as `8bdff46`; gateway and owner runtime recreated successfully during rollout.
  The owner reports the live `/restart` baseline checked. Negative cases, specific topic behavior
  and provider usability beyond that report remain separate acceptance.
- Active non-personal profiles and unresolved managed task holds refuse restart;
  disabled groups stay disabled. Sessions/auth/workspaces and schedules are preserved.
- See [spec](superpowers/specs/2026-10-29-telegram-restart.md); this is not an emergency
  kill of a wedged drain, host reboot or image update.


## CP-004 PR 3 merged (#81, 2026-10-29)
- Branch `feat/cp004-task-exec` (worktree `gopod-cp004-pr3`), merged as `39d4d5e`. ADR D044.
  `internal/runner/{exec_policy,exec_guard,task_managed,task_run,provider_task,docker_policy}.go`, `internal/taskreport`,
  `internal/taskexec`, `internal/store/task_report.go`, `queue.ErrDenied`/`FolderHeld`, flag `GOPOD_MANAGED_TASKS` (off).
- With the flag on, **no task can run yet**: no credential adapter is qualified for the task role (shared native state dir also holds
  foreground transcripts). Owner decision needed: qualify a shared-dir binding explicitly, or build a separate task credential adapter.
- Pending acceptance gates: native subscription smoke for Claude and Codex (separate explicit gate, never run in unit tests), a real
  Docker inspection check on the Pi (digest normalisation is fixture-verified only), role round trip with the live foreground container.
- **Before setting `GOPOD_MANAGED_TASKS` a live create/inspect/reuse round trip on the Pi is REQUIRED.** The guard compares the
  Docker-inspected mounts, env names and hardening exactly with the digest recorded at creation; the only tolerated normalizations are
  `CapDrop` "ALL" case and `SecurityOpt` `no-new-privileges[:true]`. A daemon that rewrites bind sources, `User` or other fields would
  make the owner container undeployable (denied every turn) until the digest is deliberately extended.
- With the flag on, a running container without policy labels (legacy) is replaced by the first slot-holding turn when no unproven
  attempt holds the folder; startup cleanup does the same unless `GOPOD_LEFTOVER_CLEANUP_DISABLED` is set. The public `Runner.Ensure`
  (used by `/login`) holds no slot and refuses to recycle a running container (`ErrRecycleNeedsSlot`), so `/login` right after a role or
  scope change fails until a turn has run. The task role mounts only `ipc/messages` and `ipc/task-results`; the report capability env
  is `GOPOD_TASK_REPORT_CAP` (no KEY/SECRET/TOKEN in agent env names).
- Not done: settlement/result publication/stop coordination (PR 4), schedule policy (PR 5), per-role IPC enforcement in the watcher,
  persisting report revisions beyond latest. CP-004 stays `in-progress`.

## CP-007 PR 1 merged (#80, 2026-10-29)
- Branch `feat/cp007-backups` (worktree `gopod-cp007-pr1`), merged as `e671c0f`: backup foundation
  (reliability-operations plan slices 4-6, ADR D043). `gopod backup create|verify|list|idle|quarantine`
  (`internal/{backup,maintenance}`, `internal/store/backup*.go`, `internal/control/backup_cmds.go`,
  `cmd/gopod/{backup_cli,quarantine}.go`), runbook [BACKUP-RESTORE](deployments/BACKUP-RESTORE.md).
- Behavior change on `main`: the controller now takes `<data>/.controller.lock` at start (waits 60 s), so a second
  controller on one data dir fails to start; `GOPOD_RECOVERY_QUARANTINE=1` or `<data>/.restore-quarantine` runs control
  plane only. Both default to normal behavior.
- Not done: timer/retention (slice 7), session/auth/ChatsDir/IPC capture, restore + activation command, Pi drill. The
  Pi now runs `gopod backup`; first online create/verify evidence is in the deployment report.
  The rollout used a pre-deploy cold copy. Restore/activation drill remains pending. CP-007 is `in-progress`.

## CP-004 PR 2 merged (#79, 2026-10-29)
- Branch `feat/cp004-dispatch` (worktree `gopod-cp004-pr2`), based on PR 1 (`c7ff9d2`): managed dispatch and conservative
  recovery (`internal/queue/managed.go`, `internal/store/task_dispatch.go`, `cmd/gopod/managed_turns.go`, ADR D042). Merged as `5680a85`.
- Production wiring is inspect-only (`queue.ManagedRuntime` without a handler): no managed task can exist or run yet, legacy
  queue behavior is unchanged. Possibly started attempts become `execution_unknown`, are never replayed, and their holds are
  rebuilt before any work is scheduled. Needs PR 3 for the real handler, snapshot resolver and exit evidence.
- Not done: PR 3-5; stop/delete vs dispatch coordination and frozen-result publication (PR 4); operator command for `ReleaseHold`.
  CP-004 stays `in-progress`.

## CP-003 PR 5 merged (#78, 2026-10-29)
- Branch `feat/cp003-voice-code-parity` (worktree `gopod-cp003-pr5`), merged as `9cd3ce1`. Code documents
  and the personal automatic voice clip are prepared as immutable snapshots (`internal/telegram/{prepared,artifact_prepare,
  speech,speech_openai,voice_plan}.go`, `internal/store/generation.go`, `artifactfs.SnapshotBytes`), ADR D041.
- Behind `GOPOD_OUTBOX_ARTIFACTS` only (same gates as PR 4, no new flag). Foreground still sends directly; `PublishVoiceReply`
  (text + voice in one plan, truthful status) is built and tested but unused until PR 6.
- Not done: **live TTS/billing qualification (needs its own authorized scope and evidence)**, foreground cutover (PR 6),
  agent-requested voice (needs the runner capability issuer), operator resend of uncertain uploads. CP-002/003 stay `in-progress`.

## CP-004 PR 1 merged (#77, 2026-10-29)
- Branch `feat/cp004-task-storage` (worktree `gopod-cp004-pr1`), merged as `c7ff9d2`: additive task storage only
  (`internal/store/{schema_tasks,task_definitions,task_occurrences,task_runs}.go`, ADR D040).
- Nothing calls the new APIs yet and `GetDueTasks` still serves only legacy rows, so behavior is unchanged.
  `AdmitTaskOccurrence` takes a planned `AdmissionRequest`; the cron/DST/overlap planner is PR 5, queue
  dispatch of the committed turn is PR 2. CP-004 is `in-progress`; CP-021/022 stay `proposed` until their slices.
- Not done: PR 2-5, native execution, settlement, purge (always blocked), managed `/tasks` UI.

## CP-003 PR 4 merged (#76, 2026-10-29)
- Merged as `6548bd6`, off by default: artifact inventory
  and quotas (`internal/store/artifacts*.go`), rooted no-follow snapshot store (`internal/artifactfs`), per-run
  capabilities, strict V1 admission and reconcile (`internal/ipc/{capabilities,artifact_request,artifacts,artifacts_reconcile}.go`),
  verified-bytes upload (`internal/telegram/artifact_opener.go`), ADR D039.
- Off by default: `GOPOD_OUTBOX_ARTIFACTS=1` also needs the dispatcher and `GOPOD_OUTBOX_IPC=1`. **Do not enable** before the
  CP-007 matched DB/artifact backup, lease and restore gate passes (D039); groups still need CP-020/CP-019.
- Not done: the runner-side capability issuer (so every V1 request is still `stale_capability`), voice and generated-code
  parity (PR 5), foreground freeze (PR 6), operator resend of uncertain uploads, a bounded live smoke. CP-002/003 stay `in-progress`.

## CP-002 PR 3 merged (#74, 2026-10-28)
- Branch `feat/cp002-ipc-durable` (worktree `gopod-cp002-pr3`), based on PR 2 (`e7eaeee`): personal text IPC
  durable admission (`internal/ipc/durable*.go`, `store.RecentIPCRequests`), silent scheduled turns
  (`telegram.runTaskSilently`), ADR D038. Enabled in production for the owner's personal chat since 2026-10-29.
- Off by default: needs `GOPOD_OUTBOX_DISPATCHER=1` and `GOPOD_OUTBOX_IPC=1`; only the owner's personal chat,
  thread 0. Everything else (groups, shared chats, cross-chat owner sends) is the unchanged legacy path.
- Not done: bounded live smoke on the real bot; operator handling of uncertain rows (diagnostics only, no
  resend); PR 4 artifacts. CP-002 stays `in-progress`.

## CP-002 PR 2 merged (#73, 2026-10-28)
- Branch `feat/cp002-dispatcher` (worktree `gopod-cp002-pr2`), based on PR 1 (`d3c3d69`): store
  claim/ack/release/fail/uncertain/sweep (`internal/store/outbox_dispatch.go`), single-attempt
  transport and dispatcher (`internal/telegram/outbox_{transport,dispatcher}.go`), ADR D037.
- Dispatcher is wired in `cmd/gopod` only when `GOPOD_OUTBOX_DISPATCHER=1` (default off). Nothing
  publishes deliveries yet and no existing send path was changed. CP-002 stays `in-progress`.
- Review fixes: terminal-write retries, in-process orphan reconcile, retry_after margin, stale-cancel loop, startup lock wait (dispatcher disabled on failure), artifact reader close.
- Next: PR 3 personal text IPC durable admission. Deferred: visibility value set, operator resend of
  uncertain deliveries via a superseding delivery, artifact opener (PR 4).

## Current state
Core Telegram assistant, Docker runtime, Claude/Codex native sessions,
conversation-scoped history, memory, voice, scheduler and filesystem IPC are
implemented. No other runtime, channel, guest agent or bot-to-bot peer is enabled.

The reference Pi controller now runs **8bdff46**, deployed 2026-10-29 19:11 UTC (see Resume here; earlier baseline a3a9c53 from 2026-10-27).
CP-020 implementation is merged (PRs #66/#67; green CI/Sonar and independent review).
Groups remain disabled in production configuration. Both final native images
passed TCP isolation preflight; full activation is blocked by expired Claude auth
and Codex post-preflight tree-budget admission failure. Codex simple no-tool
inference succeeded. Personal sessions/memory/schedules were preserved.
See [deployment evidence and remaining gates](deployments/2026-10-27-group-isolation.md).
[Private-login rollout](deployments/2026-10-25-01c18fe.md): group auth now requires
private owner confirmation, preserving the group target/tier. Actual client login
visibility remains A20; no account login was performed by rollout.
[Doctor output rollout](deployments/2026-10-25-ea38581.md): underscore rendering,
manual-refresh labels and historical owner startup cache corrected; client
reverification pending.
[Startup deployment evidence](deployments/2026-10-25-0a25267.md): controller
automatically created the selected Claude owner runtime; CLI2.1.310/auth ready.
Management correctly reports Claude quota unknown/unsupported; no model call.
[CP-017 deployment evidence](deployments/2026-10-25-f1f4532.md): private diagnostics
activated, read-only getMe/menu checks passed; actual client acceptance A18 pending.
Claude Code2.1.310 and Codex CLI 0.164.0 agent tags remain unchanged. Build/review/CI,
full tests and runner/Telegram/queue races passed. A real no-network Docker
preemption test passed on the Pi: a silent background shell was gone before a
foreground waiter reused the slot (no model request). Running version/image,
startup/integrity verified. Latest login rollout preserved sessions12/history37/tasks1 and both registrations;
restarts 0, OOMKilled false, no unfinished user turns at the post-ready verification snapshot.
[Background-preemption deployment evidence](deployments/2026-10-25-18e0c86.md) records backup,
rollback, tests and limits. New authenticated Codex replies were observed before
this fix; real multi-topic/model acceptance after it remains open. Native
Claude's NODE_OPTIONS acceptance is not a qualified memory cap.

The existing **freefeed-daily-digest is active**, 09:00 Europe/Belgrade. It was
already active in the 17:24 predeploy backup; the older paused observation is
historical. No schedule/provider mutation was issued by this incident fix. Do
not pause/resume, replace or change it merely to close a validation checkbox.

## What's done
- CP-017 initial private-DM capability diagnostics is implemented; deployment/client evidence tracked separately below.
- CP-001: per-exec memory capability, header-only authorization and chat/thread
  scope; stale registry status closed. Authenticated real skill acceptance A14.
- CP-008: /model selection/persistence and provider flags; stale accepted status
  closed. /model restart/use acceptance remains A11. Per-topic choice is CP-018.
- CP-009/010 (D030/D031): bounded per-topic handoff preserves native sessions;
  provider selection is still chat-wide. Quota switching needs explicit consent
  and queues one retry; no unattended fallback chain. /clear invalidates this
  topic's history and both provider mappings, not a complete data purge.
- CP-013: successful speech transcripts carry source/automatic-ASR/duration
  metadata after raw command/login routing. Voice baseline confirmed by owner; remaining media/provenance acceptance A07.
- CP-015: agent scheduling guide, validated recurring task operations and
  scoped receipts; explicit IPC delivery, canonical tg: JID adapter and message
  error receipts. Live delivery API acceptance was verified, not user reading.
- /tasks now uses Unicode-safe truncation and three-task pages with scoped
  Pause/Resume/Refresh/navigation. Stale actions and completed tasks cannot
  mutate unexpectedly. Baseline /tasks confirmed by owner; paging/toggle acceptance remains A01.
- Telegram UX T6/T10–T13/T15 is implemented/deployed, not actively being coded:
  callbacks/Stop/Retry, reply actions, reactions/progress, pickers/confirmations/
  HTML paging, reply/quote/forward/albums/audio/video-note baseline. Live A07–A11.
- Durable turns, explicit session ids, native streaming/fallback and capability
  API tests are in main. Unit/CI success does not close crash/client tests.

## Accepted designs — not implementations
| CP | Saved specification |
|---|---|
| 011 | [Sandbox runtime](superpowers/specs/2026-10-25-sandbox-runtime.md) |
| 012 | [Provider registry](superpowers/specs/2026-10-25-provider-registry.md) |
| 014 | [Three media queues](superpowers/specs/2026-10-25-telegram-media-input.md) |
| 016 | [Topic deletion and session retirement](superpowers/specs/2026-10-25-telegram-topic-deletion.md) |
| 018 | [Per-conversation providers](superpowers/specs/2026-10-25-per-thread-providers.md) |
| 019 | [Ephemeral commands/service/errors](superpowers/specs/2026-10-25-telegram-ephemeral-commands.md) |

Ordinary bot content must remain public in groups. CP-019 makes only service,
command replies and errors private, including every callback/page/failure path;
there is no implementation or privacy-flag activation yet.

## What's in progress
CP-020 implementation is complete and merged to main (PRs #66/#67): namespace/epoch/CAS,
protected roots, split personal/group images, read-only native-auth references,
isolated offline tools, exact-route HTTPS relay, local group memory/IPC and
confirmed owner controls. Local full tests/races/vet and real Docker tool checks
passed; CI/Sonar and independent review are green. The Pi offline-tool test passed
(6.22 s), and earlier-candidate real Claude new/resume two-turn qualification preserved file, context
and session identity. Controller deployment and both native TCP preflights passed; full two-turn
provider qualification and controlled-group activation remain open; use [the plan](superpowers/plans/2026-10-25-personal-group-container-isolation.md)
and [current policy/config](ISOLATION.md) for gates. Group speech and automatic
maintenance/extraction/compaction are unavailable; personal behavior is unchanged.
No T9 memory-cgroup change/reboot, account refresh or paid API fallback is included.
Native policy v2 passed 69 local cases across all 11 public catalog models, new/
resume and hostile scenarios. Sanitized public catalog default is `gpt-6.1-sol`;
ARM64 controller/Claude/Codex v2 images were built locally from exact merged main.
Production catalog/policy were installed with zero enabled groups. No production
group migration occurred. Fix Codex tree-budget admission and refresh Claude auth
through private management before rerunning full canaries.

Doctor output follow-up is merged and deployed: protect technical identifiers from
Markdown emphasis, label refresh eligibility as manual-only, and expose a scoped
historical startup observation without claiming current runtime health. Owner
provided a real doctor response; remaining A18 client checks stay open.

CP-017 initial private-DM slice is reviewed, merged and deployed as f1f4532.
The separate owner-startup runtime/self-diagnostic slice is merged and deployed;
see [startup contract](STARTUP.md) and D035. It restores selections before
recovery, warms the selected owner image, checks CLI/local auth and performs a
Codex management-only quota observation where supported. No model call or
automatic provider switch is included. Actual selected Claude startup is
verified; live Codex subscription quota and failure cases remain A19.
`/telegram` reads cached observations for registered private chats; owner chat alone
can use `/telegram doctor` and explicit `refresh`. Static `/help guest|inline|topics`
works without model/container/media processing. New commands silently suppress all
output in groups/unknown contexts; no new query handlers or subscriptions.
Five-second getMe uses a raw tri-state decoder, single-flight lifecycle cache,
15-minute freshness, 30-second cooldown and capped backoff. SDK constructor no
longer blocks startup on getMe. See [plan](superpowers/plans/2026-10-25-telegram-capability-diagnostics.md)
and D034. Real Telegram client acceptance remains pending (A18).

The live extraction stall is addressed by a one-minute system-run deadline and
same-chat foreground preemption; buffered user/system cancellation waits for a
bounded process-group cleanup attempt. Canceled compaction preserves context.
See [D033](DECISIONS.md#d033--bound-fresh-system-work-and-yield-to-foreground-runs)
and [incident](incidents/2026-10-25-background-extraction-starvation.md).
Remaining live checks use ACCEPTANCE.md; full maintenance re-arming/quiet hours
and per-topic providers are still planned work, not shipped by this bug fix.
Preserve other agents' work/worktrees and use reviewed, green-CI squash PRs.

## What's next — owner-approved order
1. Qualify current deployed functionality using A01–A17 in ACCEPTANCE.md.
2. Reliability: CP-017 diagnostics, CP-002 outbox, CP-004 task sessions,
   CP-007 automatic backup/restore, CP-006 maintenance/quiet hours.
3. CP-016 topic deletion and CP-018 per-topic providers.
4. CP-019 group private service UX plus T14 scoped menu/localized strings.
5. CP-014 media Q1 fidelity -> Q2 extraction/OCR -> Q3 video; CP-003/005 support
   relevant output/tool slices rather than blocking simple link preservation.
6. Runtime/provider extensions and selected bot-to-bot experiment; gateway,
   MCP/tool ecosystem, extra providers and observability remain deferred.

Queue agreement sets priority, not automatic implementation approval for every
unreviewed proposal. The [reliability wave](superpowers/plans/2026-10-26-reliability-wave.md)
now has source-backed specs/plans; its runtime implementation remains separate.
The owner specifically advanced CP-020 full implementation ahead of that wave.

## Known limitations and follow-ups
- [Acceptance register](ACCEPTANCE.md) is the actionable current live checklist.
  Host cgroup/reboot and parallel threads are paused operational/research work,
  not implicit parts of deploying the current code.
- Legacy IPC receipts are diagnostic. Owner-personal text now uses the durable outbox;
  ambiguous sends remain uncertain and are not replayed. Full foreground/artifact cutover remains CP-002/003. Voice-only reply memo and crash windows need
  A12 verification. Queue.Close still waits without its own timeout.
- Scheduled jobs use main's session; isolation is CP-004. System-run deadlines and same-chat preemption are deployed; full maintenance
  rescheduling/quiet hours remain proposed; compaction source-context/restart
  behavior needs A17 investigation, not a claim that old notes are all fixed.
- No documented Telegram topic-deleted update; current deleted/closed-topic
  fallback is not session retirement. CP-016 separates closure from deletion.
- Dated getMe research: Guest/topics on; inline/business off; group privacy on.
  Gopod subscribes only to message/callback/Stop, no guest handler. SDK inline
  capability JSON tag is wrong; CP-017 now bypasses it with the tested raw decoder.
- Media baseline is download/path/transcription, not universal understanding:
  ordinary video lacks a handler, album audio/video skipped, captionless-photo
  prompt path gap and hidden entity/button URL loss remain CP-014 queue1.
- [Bot-to-bot research](research/2026-10-25-telegram-bot-to-bot.md) is feasibility
  only. No peer contacted; enablement unknown, peer trust/loop budgets absent.
- New advanced interfaces do not imply per-thread filesystem secrecy: workspace
  and container stay chat-shared, turns serialize per chat.

Separate [table-rendering analysis](research/2026-10-25-telegram-table-rendering.md)
recommends a bounded Markdown-table parser with lossless row cards, then a native
Rich Table client spike. SDK already supports Rich Tables; no formatter change
is implemented or implicitly approved by that research.

[Group mention analysis](research/2026-10-25-group-mention-delivery.md) separates
privacy-mode command/reply delivery from unsupported Guest updates. Registration
alone does not make ordinary @mentions arrive. No rights/privacy/subscriptions
were changed. Use Reply for a supported group delivery test; Guest integration
and private unregistered-chat hints remain separately scoped.

## Open questions / blockers
Private-login hardening and owner-DM group confirmation are merged and deployed
as01c18fe. See [login delivery contract](LOGIN.md); actual private-client
visibility remains A20. Full container/service isolation remains separate.

CP-020 source implementation follows the [contract](superpowers/specs/2026-10-25-personal-group-container-isolation.md)
and [acceptance plan](superpowers/plans/2026-10-25-personal-group-container-isolation.md).
The full controller is deployed with all groups disabled. The new native CLI sees a fresh
inference directory, group-only state and an explicit read-only auth-only inode;
actual files execute in a separate no-network sandbox. Missing/expired auth fails
closed until private management refreshes the source. It is not a separate
account/quota. Final two-turn inference/tool/resume checks and Telegram acceptance remain gates.
SSH recovered; both native TCP preflights passed. See the deployment evidence
for the expired Claude token and Codex admission failure.
A future clean-epoch preparation preserves old scope data under retired keys,
pauses old group schedules and stays disabled if interrupted. No production
preparation was performed in this release. Timed-out host callbacks remain
tracked until drain proof; orphan cleanup cannot authorize replay or delete native
history. Controlled sandboxes permit incremental checks, not an unqualified real
group rollout. Personal services, channel/Guest transport, T9 reboot and automatic
group maintenance remain off. [One-way private reads](research/2026-10-25-one-way-private-group-access.md)
remain a separate future grant, never reciprocal access.

No open implementation decision for the initial CP-017 slice. Implementation-specific choices
are in accepted specs or pending proposals, not stale "should we build buttons?"
questions. Driver/sqlite-vec upgrade remains constrained by D017; do not change
storage dependencies as routine cleanup.

## Historical material
[Pre-housekeeping snapshot](history/HANDOFF-2026-10-25-before-housekeeping.md)
preserves the full old handoff, review notes and detailed test procedures. It is
historical, not a second task queue. DECISIONS.md retains ADR authority. Current
source/tests/deployment reports outrank old flags, versions and branch-status
claims. Update this short handoff when facts change; keep acceptance evidence in
its register and delivery priorities in ROADMAP.

## Nearby work noted but out of scope (2026-10-29)
- `store.Open` builds a SQLite file URI by concatenating the raw path. A `#` in a path
  can be interpreted as a URI fragment; repeated test names exposed cross-fixture DB
  aliasing. Formatting tests use distinct descriptive names. URI escaping is a separate fix.
