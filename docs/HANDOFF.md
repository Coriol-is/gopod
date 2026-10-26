# gopod — current handoff

**Last updated:** 2026-10-26

Read this first; use [ROADMAP](../ROADMAP.md) for the
approved queue, [proposal registry](proposals/README.md) for CP status and
[ACCEPTANCE](ACCEPTANCE.md) for pending real-client verification.

## Current state
Core Telegram assistant, Docker runtime, Claude/Codex native sessions,
conversation-scoped history, memory, voice, scheduler and filesystem IPC are
implemented. No other runtime, channel, guest agent or bot-to-bot peer is enabled.

Reference Pi runs application **01c18fe**, deployed 2026-10-25 20:35 UTC.
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
unreviewed proposal. Pending proposed CP-002–007 still need their bounded design.

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

CP-020 [deep audit](research/2026-10-25-personal-group-isolation-audit.md),
[contract](superpowers/specs/2026-10-25-personal-group-container-isolation.md) and
[six-PR plan](superpowers/plans/2026-10-25-personal-group-container-isolation.md)
are saved and independently reviewed. CP-020 P0 now blocks all unqualified
non-owner agent execution/management and legacy IPC dispatch through live
host-owned owner-private registration checks. Non-owner mounts cannot inherit
vault, global skills or extras; container env grants remain personal only.
New non-owner agent files use matching restricted templates for both providers;
existing files remain preserved and cannot enable execution. These templates are
dormant until later immutable runtime policy qualification. No group re-enablement
or full filesystem/credential/network isolation is claimed. PRs 2–6 remain open.
Production activation/evidence is recorded separately.
The prior audit confirmed personal vault RO/global credential env exposure, plus
synthetic host-bind symlink escape and IPC basename leakage. Account connectors
need a real credential/network boundary, not only CLI flags. P0 containment
precedes identity migration/billing decisions. Trusted group/channel runtime policies are not yet enabled; channel_post support is not implemented. Dedicated scoped
integration grants are allowed only explicitly; no personal account inheritance.
[One-way read idea](research/2026-10-25-one-way-private-group-access.md) is deferred:
private-agent reads of approved group data never imply reverse access/publishing.

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
