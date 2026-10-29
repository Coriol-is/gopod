# gopod — current handoff

**Last updated:** 2026-10-29

Read this first; use [ROADMAP](../ROADMAP.md) for the
approved queue, [proposal registry](proposals/README.md) for CP status and
[ACCEPTANCE](ACCEPTANCE.md) for pending real-client verification.

## Resume here (2026-10-27 evening)
- **Deployed 2026-10-29:** Pi runs `07d9edd` (CP-002 PR1–3, flags off); see [evidence](deployments/2026-10-29-07d9edd.md). Pi LAN address is 192.168.1.128 when the Tailscale name is offline. In-flight: CP-003 PR4 (artifacts) in worktree `gopod-cp003-pr4`.
- **Repo is clean:** `main` has no open PRs, no stale local/remote branches and no
  extra worktrees (all 67 merged remote branches and 40 worktrees were removed after
  verifying their PRs merged). CI on `main` is green; PR #70 moved actions to
  Node 24 majors (`checkout`/`setup-go`/`setup-node` v7) and pinned `ubuntu-24.04`.
- **No CD workflow:** deploys are manual per [deployments/](deployments/). `main` has no
  branch protection, so green CI is a convention, not a gate (owner decision pending).
- **Either harness can continue:** Claude Code reads `CLAUDE.md`, Codex reads `AGENTS.md`
  (points at `CLAUDE.md`); both start here. No work is in flight, nothing is unpushed.
- **First action next session:** owner runs live Telegram acceptance A08–A11, A18, A20,
  A02 (see [ACCEPTANCE](ACCEPTANCE.md)); in parallel unblock CP-020 gates (refresh Claude
  auth via private management, fix Codex tree-budget admission, rerun two-turn canaries,
  enable one controlled test group). Then start CP-002 outbox from the
  [reliability wave](superpowers/plans/2026-10-26-reliability-wave.md), each CP in its
  own worktree/PR. Order after that: CP-004, CP-007, CP-006, CP-016, CP-018, CP-019.
- **Not authorized without the owner:** account logins, quota/paid fallbacks, T9 reboot,
  production group activation, schedule changes (freefeed digest stays as is).

## CP-003 PR 5 in progress (2026-10-29)
- Branch `feat/cp003-voice-code-parity` (worktree `gopod-cp003-pr5`), based on PR 4 (`6548bd6`); not pushed. Code documents
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

## CP-002 PR 3 in progress (2026-10-28)
- Branch `feat/cp002-ipc-durable` (worktree `gopod-cp002-pr3`), based on PR 2 (`e7eaeee`): personal text IPC
  durable admission (`internal/ipc/durable*.go`, `store.RecentIPCRequests`), silent scheduled turns
  (`telegram.runTaskSilently`), ADR D038. Not pushed yet.
- Off by default: needs `GOPOD_OUTBOX_DISPATCHER=1` and `GOPOD_OUTBOX_IPC=1`; only the owner's personal chat,
  thread 0. Everything else (groups, shared chats, cross-chat owner sends) is the unchanged legacy path.
- Not done: bounded live smoke on the real bot; operator handling of uncertain rows (diagnostics only, no
  resend); PR 4 artifacts. CP-002 stays `in-progress`.

## CP-002 PR 2 in progress (2026-10-28)
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

The reference Pi controller now runs **a3a9c53**, deployed 2026-10-27 08:58 UTC.
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
- IPC receipts are diagnostic, not a crash-safe delivery outbox. Network/crash
  send ambiguity remains CP-002. Voice-only reply memo and crash windows need
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
