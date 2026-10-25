# gopod — current handoff

**Last updated:** 2026-10-25

Read this first; use [ROADMAP](../ROADMAP.md) for the
approved queue, [proposal registry](proposals/README.md) for CP status and
[ACCEPTANCE](ACCEPTANCE.md) for pending real-client verification.

## Current state
Core Telegram assistant, Docker runtime, Claude/Codex native sessions,
conversation-scoped history, memory, voice, scheduler and filesystem IPC are
implemented. No other runtime, channel, guest agent or bot-to-bot peer is enabled.

Reference Pi runs application **8df259d**, deployed 2026-10-25 16:42 UTC.
Agent runtime tags contain **Claude Code2.1.310** and **Codex0.164.0**. The
application version/image, startup/polling, SQLite integrity and CLI versions
in hardened ARM64 Pi containers were verified. At the deployment check:
restarts0, OOMKilledfalse, sessions/history/tasks preserved7/54/1. An actual
upgraded authenticated model turn/native resume is still A02, not proven by
--version. Native Claude accepting NODE_OPTIONS is not a qualified V8/RAM cap;
existing admission checks remain in place, Pi memory cgroup remains unqualified.

[Latest deployment evidence](deployments/2026-10-25-8df259d.md) contains image ids,
backup/rollback tags, exact tests and limitations. Subsequent documentation-only
commits do not require rebuilding/restarting this same application payload.

The user's **freefeed-daily-digest is paused**, cron 09:00 Europe/Belgrade.
It was created/paused through the user's live agent, not by this investigation.
Do not resume it, create a replacement, or change its timezone as housekeeping.

## What's done
- CP-001: per-exec memory capability, header-only authorization and chat/thread
  scope; stale registry status closed. Authenticated real skill acceptance A14.
- CP-008: /model selection/persistence and provider flags; stale accepted status
  closed. /model restart/use acceptance remains A11. Per-topic choice is CP-018.
- CP-009/010 (D030/D031): bounded per-topic handoff preserves native sessions;
  provider selection is still chat-wide. Quota switching needs explicit consent
  and queues one retry; no unattended fallback chain. /clear invalidates this
  topic's history and both provider mappings, not a complete data purge.
- CP-013: successful speech transcripts carry source/automatic-ASR/duration
  metadata after raw command/login routing. Real new voice acceptance A07.
- CP-015: agent scheduling guide, validated recurring task operations and
  scoped receipts; explicit IPC delivery, canonical tg: JID adapter and message
  error receipts. Live delivery API acceptance was verified, not user reading.
- /tasks now uses Unicode-safe truncation and three-task pages with scoped
  Pause/Resume/Refresh/navigation. Stale actions and completed tasks cannot
  mutate unexpectedly. Real user callback acceptance remains A01.
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
| 017 | [Telegram capability diagnostics](superpowers/specs/2026-10-25-telegram-capability-diagnostics.md) |
| 018 | [Per-conversation providers](superpowers/specs/2026-10-25-per-thread-providers.md) |
| 019 | [Ephemeral commands/service/errors](superpowers/specs/2026-10-25-telegram-ephemeral-commands.md) |

Ordinary bot content must remain public in groups. CP-019 makes only service,
command replies and errors private, including every callback/page/failure path;
there is no implementation or privacy-flag activation yet.

## What's in progress
No feature implementation is currently active. Documentation/status
reconciliation is complete in this change; no active work is inferred from old
branch names. Independent reviews and CI
must pass before squash merge. Preserve other agents' work/worktrees; do not
turn housekeeping into branch deletion, reset or production task mutation.

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
- Scheduled jobs use main's session; isolation is CP-004. Maintenance does not
  yet implement user preemption/quiet hours; compaction source-context/restart
  behavior needs A17 investigation, not a claim that old notes are all fixed.
- No documented Telegram topic-deleted update; current deleted/closed-topic
  fallback is not session retirement. CP-016 separates closure from deletion.
- Dated getMe research: Guest/topics on; inline/business off; group privacy on.
  Gopod subscribes only to message/callback/Stop, no guest handler. SDK inline
  capability JSON tag is wrong; true fixture decodes false (CP-017 fix planned).
- Media baseline is download/path/transcription, not universal understanding:
  ordinary video lacks a handler, album audio/video skipped, captionless-photo
  prompt path gap and hidden entity/button URL loss remain CP-014 queue1.
- [Bot-to-bot research](research/2026-10-25-telegram-bot-to-bot.md) is feasibility
  only. No peer contacted; enablement unknown, peer trust/loop budgets absent.
- New advanced interfaces do not imply per-thread filesystem secrecy: workspace
  and container stay chat-shared, turns serialize per chat.

## Open questions / blockers
No blocker to the documentation reconciliation. Implementation-specific choices
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
