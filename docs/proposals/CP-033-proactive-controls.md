# CP-033 — Owner controls for proactive jobs

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-30 |
| Origin | owner request after the codex-agent autonomy delta ([note](../research/2026-10-30-codex-agent-autonomy-prioritization.md)) |
| Size | M |
| Touches | `internal/control`, `internal/store`, `internal/scheduler`, `internal/telegram` (panel and buttons), `cmd/gopod` (CLI parity) |
| Depends on | CP-006 (quiet hours, `proactive` delivery flag), CP-002 (outbox), CP-022 (schedule policy) for full behavior; analyses A1-A6 in the note before any new source is added |
| Spec | `—` until accepted |

## Problem
The donor runs daily/weekly reflection and mail/calendar summaries unasked, with no owner-facing
control beyond environment switches (`PROACTIVE_ENABLED`, cron strings in `config.js`). gopod
has one unattended owner job today (the 09:00 freefeed digest) managed only through the generic
`/tasks` panel. Before any proactive behavior is added, the owner needs one place to see,
enable, pause and bound every job that speaks without being asked, and an emergency stop.

## Proposal
A proactive job is a scheduled job marked `proactive` (read-only profile, owner private chat
only, default off). `/proactive` goes through `internal/control` like every admin command
(D011): owner-only, private chat only, authorization in the Router `Perm`. Called without an
argument it shows a panel of jobs with per-job enable/disable, schedule, sources, a per-day
cost cap, the last outcome and "run now" (result to the owner only), using inline-button
pickers (CLAUDE.md "Commands with parameters"). `/proactive off` stops every proactive job at
once without touching explicit reminders or ordinary schedules; quiet hours are set here and
apply only to the `proactive` flag. The CLI exposes the same operations. The existing freefeed
digest is the first job placed under it.

## Out of scope
New proactive sources or behaviors (reflection, mail, calendar, learning), connector
enablement, CP-027 approvals, groups, per-chat proactive jobs, changing `/tasks` for ordinary
schedules, any change to the existing digest's schedule or content when it is brought under
control.

## Risks
A control surface that looks complete before enforcement exists: the panel must show a job as
stopped only if the scheduler really refuses to start it. Migrating the freefeed digest must not
pause or alter it by assumption. A kill switch that fails open would be worse than none.

## Acceptance
- `/proactive` from a group, from a non-owner and from a callback forged outside the owner's
  private chat is refused by the Router, not by the handler.
- With the global switch off, no proactive job starts and no `proactive` message is sent, while
  an explicit reminder still fires.
- A job's enable, schedule and cost cap persist across restart and are visible in the panel and CLI.
- Moving the freefeed digest under `/proactive` keeps its existing schedule and its next run time.
- A job over its daily cap is skipped with a recorded reason, not silently dropped.
