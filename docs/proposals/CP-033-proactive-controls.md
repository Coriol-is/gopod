# CP-033 — Built-in recurring presets for the main connectors

| | |
|---|---|
| Status | accepted (2026-10-30; first set Gmail and Calendar, preset list approved) |
| Date | 2026-10-30 |
| Origin | owner request after the codex-agent autonomy delta ([note](../research/2026-10-30-codex-agent-autonomy-prioritization.md)) |
| Size | M |
| Touches | `internal/control` (new `/proactive` command), `internal/store` (preset marker on scheduled tasks), `internal/scheduler`, `internal/telegram` (picker buttons) |
| Depends on | existing scheduled tasks (CP-015) and `/tasks`; CP-006 and CP-002 for quiet-hour delivery of proactive messages |
| Spec | `—` until accepted |

## Problem
Today the owner gets recurring connector work (a morning mail summary, a daily agenda) only by
describing it to the agent in a dialog, once per task, and re-describing it to change it.
Nothing ships ready-made, and nothing lets the owner turn such a routine on or off, change how
often it runs or when its notification arrives, from one place. The donor runs such routines
unasked from hard-coded prompts; gopod has none.

## Proposal
The gateway ships a small catalog of **preset recurring tasks** for the main connectors the
owner has connected to Codex or Claude (first set, owner decision 2026-10-30: Gmail and Google Calendar; Drive and others later). A
preset is a normal scheduled task, created from a built-in prompt template with a default
schedule, and runs through the same scheduler, queue and delivery as a task the owner set up in
dialog; the only difference is that it is defined in the gateway, not in conversation. It shows
up in `/tasks` like any other task and keeps working if the owner edits it there.

`/proactive` (name open), through `internal/control` like every admin command (D011), owner-only
in the private chat, with inline-button pickers (CLAUDE.md "Commands with parameters"): with no
argument it lists connectors and their presets with the current state; for each preset the owner
can switch it on or off, pick how often it runs (a fixed set of choices such as every 30 minutes,
hourly, daily, weekly) and at what time of day the notification is delivered, and set quiet
hours. A preset is off until enabled. The text form `/proactive <connector> <preset> on|off`
keeps working. Nothing is restricted for the owner: presets use the connector as connected,
read and write, as the agent would in dialog. A global `/proactive off` pauses every preset at
once without touching other tasks.

Connectors are account state, not gopod state, so some will not be activated. For each connector
and provider (Claude, Codex) the gateway works out whether it is connected, from what the CLI
reports in the container (the init event and `claude mcp list` for Claude; `codex plugin list`
for Codex), and `/proactive` shows one of: ready, not connected, plugin not installed, or
unknown. For anything not ready the panel offers a **setup guide** in the private chat: the
exact steps on the provider's side (for Codex: connect the app at the ChatGPT apps page with
the same account, then install the plugin such as `gmail@openai-curated-remote`; for Claude:
turn the connector on in the claude.ai account settings), a link, and a "check again" button
that re-reads the state. The Google consent itself is always given by the owner in the
provider's own account; gopod never performs the sign-in or stores the Google token. Presets
for a connector that is not ready stay off and say why. Exact link targets, plugin names and
the CLI commands are verified in the spec, not taken from this paragraph.

**Other connectors, configured by the agent.** Connectors outside the preset catalog are not
shipped as presets. The gateway probes periodically (at most hourly, and on "check again") with
commands that need no model call (`claude mcp list`, `codex plugin list --json` inside the
container), keeps the last seen set, and when a connector or plugin appears it sends the owner a
private message offering to set up recurring actions. If the owner agrees, the gateway hands the
agent a ready-made request in dialog and the agent creates the recurring task through the existing
scheduling (CP-015), so it appears in `/tasks`. A declined offer is remembered and not repeated;
a connector that disappears is marked unavailable and its tasks are paused. Observed 2026-10-30
on the owner's machine: Claude lists connectors by name and health; Codex lists plugins with
installed/enabled flags (including `gmail@`, `google-calendar@`, `google-drive@`, `slack@`) and
its `mcp list` shows only local servers; container behavior is verified in the spec.

## Out of scope
Restricting what a preset may do in the owner's own chat; new connectors beyond Gmail and Calendar
catalog (Gmail and Calendar); performing connector sign-in or consent for the owner (only the setup guide is in scope); autonomous reflection
or learning; groups and any non-owner scope; changing the existing freefeed digest or other
tasks; a free-form editor for preset prompts (a later spec may allow it).

## Risks
Presets spend subscription quota unattended, so each needs a visible schedule and an easy off
switch. Preset prompts depend on the connector being connected: a missing connector must show
as "unavailable", not fail silently. A preset edited in `/tasks` must not be silently reset by a
later upgrade of the built-in template.

## Acceptance
- `/proactive` is refused by the Router outside the owner's private chat, including forged
  callbacks, not by the handler.
- Enabling a preset creates one scheduled task visible in `/tasks`; disabling it pauses or
  removes that task; enabling twice does not create a duplicate.
- Changing frequency or delivery time changes the task's next run accordingly and survives restart.
- Quiet hours delay `proactive`-flagged notifications and do not delay ordinary replies or
  explicit reminders.
- A preset whose connector is not connected is shown as unavailable and is not scheduled.
- A connector newly connected in the provider's account is noticed within the probe interval,
  the owner gets one offer, accepting it ends with a task created by the agent and visible in
  `/tasks`, and declining it is remembered.
- For each provider, a connector that is not ready is reported with its reason and a setup guide;
  after the owner connects it in the provider's account, "check again" reports it ready without
  a restart. The guide contains no credential, and gopod performs no provider sign-in.
- `/proactive off` pauses all presets and leaves other tasks, including the freefeed digest,
  untouched.
