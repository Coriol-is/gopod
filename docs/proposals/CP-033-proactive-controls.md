# CP-033 — Owner controls for provider connectors and proactive actions

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-30 |
| Origin | owner request after the codex-agent autonomy delta ([note](../research/2026-10-30-codex-agent-autonomy-prioritization.md)) |
| Size | M |
| Touches | `internal/control`, `internal/store`, `internal/scheduler`, `internal/runner` (tool-call record), `internal/telegram` (panel and buttons), `cmd/gopod` (CLI parity) |
| Depends on | CP-006 and CP-002 for quiet-hour delivery of proactive messages |
| Spec | `—` until accepted |

## Problem
Connectors (Gmail, Calendar, Drive and similar) are attached to the owner's Codex or Claude
account, not to gopod. The host does not know which are connected, cannot say which a given run
may use, and cannot stop a run from using a write tool. In the donor this is exactly how
unrequested mail summaries happen: a scheduled prompt says "review all connected sources" and
whatever the account has connected is read, with only prompt wording as a limit. The owner
decided on 2026-10-30 that nothing is restricted in the private chat or the owner's own
sessions; there is no owner-facing view or switch over which connectors exist, which proactive
actions use them, and when they run.

## Proposal
`/connectors` (name open) through `internal/control` like every admin command (D011):
owner-only, private chat only, authorization in the Router `Perm`. It is a management and
visibility surface, not a restriction on the owner. Without an argument it shows a panel, with
inline-button pickers (CLAUDE.md "Commands with parameters"), of the connectors visible to each
provider, whether each is on for owner turns and for proactive runs (both on by default for the
owner), and the proactive actions that use them. Each proactive action has an on/off switch,
schedule, source list, optional per-day cost cap and quiet hours (applied only to messages
flagged `proactive`) and the last outcome. `/connectors off` pauses all proactive actions at
once without touching explicit reminders or ordinary schedules. The host records which connector
tools ran (audit, not enforcement). An optional per-job read-only switch may be added later
where the CLI can enforce it; it is never required and never applies to the owner by default.
Non-owner scopes keep their existing isolation.

## Out of scope
The freefeed digest and any other existing schedule (unchanged), new proactive behaviors
(reflection, mail or calendar summaries; those need their own proposal after analysis A2),
connector sign-in or consent (done in the provider's own account), host-held mail credentials
and CP-027 approvals, groups and any non-owner scope, per-chat connector settings.

## Risks
The panel must show real state: a job shown as paused must really not start. The host sees
connector use only as tool-call records from the CLI, not the connector's own audit log, and
connector availability is account state that can change outside gopod.

## Acceptance
- `/connectors` from a group, a non-owner or a forged callback outside the owner's private chat
  is refused by the Router, not by the handler.
- Connectors work in owner turns and in proactive runs with no extra gate by default.
- A proactive action switched off does not start; with the global switch off no proactive run
  starts and no `proactive` message is sent, while an explicit reminder still fires.
- Settings persist across restart and are visible in the panel and CLI; a connector that
  disappears from the account is shown as missing.
- The host keeps a record of which connector tools each run used.
- Existing schedules, including the freefeed digest, keep their schedule and next run time.
