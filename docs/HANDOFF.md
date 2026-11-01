# gopod — current handoff

**Last updated:** 2026-11-01 (`/silent` merged as `6d11765`, CP-038 design merged as `2ca967c`; production unchanged)

Read this first; use [ROADMAP](../ROADMAP.md) for the
approved queue, [proposal registry](proposals/README.md) for CP status and
[ACCEPTANCE](ACCEPTANCE.md) for pending real-client verification.

## Resume here — `/silent` progress-only streaming merged, 2026-11-01

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

**Act on this first, independently of CP-038.** An independent review found a defect on
`main`: `callbackCaller` sets no `MessageID` (`internal/telegram/cmdbuttons.go:180-191`),
and `internal/control/restart.go:28` rejects a caller without one, so `/restart force`'s
confirmation, `/restart status` and `/restart cancel` are all refused. Scope precisely:
those forms and their buttons arrived with `RegisterRestartLifecycle` in `5c36b81`, which
is **not deployed**. The Pi runs `8a3ce75`, which wires `RegisterRestartCommand` — no
arguments, no buttons — so the Pi is unaffected and nothing in production is broken.
Slice `E-pre` is that one-field fix plus a test; the owner chose to populate the field
rather than weaken the check. Worth merging whether or not CP-038 is ever built.

**Corrections already made — do not re-derive them.** Spec section 13 records what the
first draft got wrong about the source. A stale policy version *denies*
(`internal/runner/exec_guard.go:211-213`), it does not recycle, and no `policyRevision`
bump is needed because `Fingerprint` hashes only the policy's own role and scope. The
guard does need new logic: a role mismatch currently reaches `actRecreate`, and
`ContainerFacts` never inspects `PidMode`/`NetworkMode`/`IpcMode`. The policy digest runs
only under `GOPOD_MANAGED_TASKS`, off by default. There is no session entity to extend,
so the runtime binds to one reserved `elevated` chat folder. Boot removal belongs between
`restart.recycle` and `CleanupLeftovers`, or a survivor blocks all execution.

**Noted, deliberately not done.** Code comments cite `docs/ISOLATION.md §6.1`
(`internal/runner/docker_args.go:36`, `:41`, `:102`, `:202`) for a section that no longer
exists; the comment at `internal/runner/exec_policy.go:45-46` says "recycled" where the
code denies; the top-level `container/Dockerfile` is stale and built by nothing; and
`/stop` omits the `MessageID` check that `/restart` applies. Only the §6.1 drift is in
the CP-038 plan's `E5`; the other three are not in any slice. None were touched.

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
5. Fix the button `MessageID` fence (CP-038 slice `E-pre`): it repairs
   `/restart force|status|cancel` on `main` (`5c36b81`, not deployed; the Pi's `8a3ce75`
   has no argument forms) and does not depend on CP-038 acceptance. Everything else in
   CP-038 waits for the owner to accept the CP.

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

## Latest specification consolidation

C9 adds the canonical execution-state -> Telegram text/actions -> recovery matrix,
including not-started versus interrupted, safe Start/Continue and stale Stop
acknowledgement independent of edit success. C27-C29 supplement C18-C26. All remain
specification-only; source behavior and deployment are unchanged.
