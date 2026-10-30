# CP-033 — Owner controls for provider connectors and proactive actions

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-30 |
| Origin | owner request after the codex-agent autonomy delta ([note](../research/2026-10-30-codex-agent-autonomy-prioritization.md)) |
| Size | M |
| Touches | `internal/control`, `internal/store`, `internal/runner` (per-run connector configuration for both CLIs), `internal/telegram` (panel and buttons), `cmd/gopod` (CLI parity) |
| Depends on | analysis A3a (can a run be limited to named connector tools, per CLI); CP-006 and CP-002 for quiet-hour delivery of proactive messages |
| Spec | `—` until accepted |

## Problem
Connectors (Gmail, Calendar, Drive and similar) are attached to the owner's Codex or Claude
account, not to gopod. The host does not know which are connected, cannot say which a given run
may use, and cannot stop a run from using a write tool. In the donor this is exactly how
unrequested mail summaries happen: a scheduled prompt says "review all connected sources" and
whatever the account has connected is read, with only prompt wording as a limit. The owner
decided on 2026-10-30 that connectors are acceptable in the private chat; there is no owner-facing
control over which ones, in which mode, for which runs.

## Proposal
`/connectors` (name open) through `internal/control` like every admin command (D011):
owner-only, private chat only, authorization in the Router `Perm`. Without an argument it shows
a panel, with inline-button pickers (CLAUDE.md "Commands with parameters"), of the connectors
visible to each provider and, for each: allowed for owner turns (on/off), allowed for
proactive runs (off by default), and mode (read-only or read-write; proactive runs can only be
read-only). Proactive actions that use connectors are listed as proactive jobs with an
on/off switch, schedule, source list, per-day cost cap and last outcome; `/connectors off`
(or `/proactive off`) stops all of them at once without touching explicit reminders or
ordinary schedules. Quiet hours apply only to messages flagged `proactive`. The host
translates the panel into per-run configuration for the CLI (tool allow/deny list or
equivalent) and records which connector tools a run was allowed to use. If a CLI cannot limit a
run to named connector tools, the panel shows the connector as "not restrictable" and proactive
runs get no access to it.

## Out of scope
The freefeed digest and any other existing schedule (unchanged), new proactive behaviors
(reflection, mail or calendar summaries; those need their own proposal after analysis A2),
connector sign-in or consent (done in the provider's own account), host-held mail credentials
and CP-027 approvals, groups and any non-owner scope, per-chat connector settings.

## Risks
The control is only as strong as the CLI lets a run be restricted: if allow/deny is unsupported
or changes between pinned versions, the panel could promise more than is enforced, so enforcement
state must be read from the effective run configuration, not from the stored setting. The host
sees connector use only as tool-call records from the CLI, not the connector's own audit log.
Connector availability is account state and can change outside gopod.

## Acceptance
- `/connectors` from a group, a non-owner or a forged callback outside the owner's private chat
  is refused by the Router, not by the handler.
- A connector set to "not allowed for proactive runs" is not callable in a scheduled run, shown
  by a synthetic run that attempts it and a recorded denial.
- A proactive run can only get read tools; a write tool call from it fails and is recorded.
- With the global switch off no proactive run starts and no `proactive` message is sent, while an
  explicit reminder still fires.
- Settings persist across restart and are visible in the panel and CLI; a connector that
  disappears from the account is shown as missing, not silently dropped.
- Existing schedules, including the freefeed digest, keep their schedule and next run time.
