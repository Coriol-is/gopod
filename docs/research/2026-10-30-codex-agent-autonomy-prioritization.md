# codex-agent autonomous mode: delta analysis and adoption priority

| | |
|---|---|
| Date | 2026-10-30 |
| Status | recommendation for owner decision; nothing here is accepted, built or enabled |
| Question | Which autonomous (unrequested) behaviors of codex-agent are worth taking into gopod, in what order, and what must be analysed further first? |
| Builds on | [2026-10-26 adoption map](../proposals/2026-10-26-codex-agent-adoption.md) (CP-002..007, CP-021..030) |
| Donor | `rzaytsev/codex-agent` at `b231ed5` (2026-10-10), the same commit the adoption map reviewed. No newer donor commits exist, so this is a delta, not a new survey. |

## 1. Conclusion

The adoption map already captures the donor's reliability machinery (outbox, outcomes,
schedule policies, backups, memory, learning, mailboxes). What it does **not** contain is the
donor's *proactive layer*: the scheduled model turns that look at recent history and push
suggestions or digests to the owner unasked (daily/weekly/monthly reflection), and the rules
that make that tolerable (quiet hours on a `proactive` flag, idle-only maintenance, one
learning question per day). gopod has the building blocks queued (CP-006, CP-021, CP-022) but
no candidate that says what proactive behavior to build on top of them.

Recommendation: finish the unblockers that are already in flight, take three cheap donor
mechanisms now, add **one** new candidate for a proactive digest framework built
deterministically (not the donor's "ask the model to look at every connected source"), and
keep automatic self-modification out.

### Owner correction, 2026-10-30 (supersedes every restriction below)

In the owner's private chat and the owner's own sessions nothing is forbidden or gated: provider
connectors (Gmail, Calendar, Drive and the rest) work as connected, in owner turns and in
proactive runs, read and write, with no read-only requirement, no CP-027 approval step and no
"connectors off" rule. This restates the standing decision "lower paranoia for the owner's own
actions, keep the boundary against group members". Anything below that reads as a prohibition
or a gate (A3a as a precondition, "connectors off in Claude", the host-side Gmail fallback as a
requirement, read-only profiles) applies only to non-owner scopes such as groups, or is an
optional owner-chosen switch. Controls the owner wants are visibility, on/off switches, quiet
hours, a cost cap and an audit record, not enforcement against the owner.

## 2. Donor autonomy inventory (static read, nothing run)

"Evidence" means tests or docs in the donor repo. The donor repo holds no run logs by design,
so none of this proves it runs in production.

| # | Behavior | Trigger | Mechanism | Evidence |
|---|---|---|---|---|
| D1 | Reflection: daily, weekly, monthly review that suggests actions and saves findings to memory | cron (default 19:00 daily, Sun 18:00, 1st 18:00) | schedule row becomes a job; prompt built from history since a coverage mark; coverage advances only on success | scheduling and quiet-hour tests; model output quality unverified |
| D2 | Reminders and recurring tasks | owner request, 1 s tick | cron plus overlap/misfire policy, DST aware; reminder text sent with no model call | about 30 scheduler tests |
| D3 | Memory consolidation | cron 03:15 / Sun 03:45 | read-only model review, host applies; aborts when the owner writes, resumes by cursor | tests |
| D4 | Self-learning | cron 03:30 | propose, second-model validate, host applies; versioned, rollback, promotion needs a host outcome receipt | tests; benefit unproven |
| D5 | Workspace cleanup | cron 03:00 | model follows a prompt with `danger-full-access` | tests; safety is prompt-level |
| D6 | Durable delivery | long poll | outbox, retries, `uncertain` never replayed | tests |
| D7 | Background workers, deep research | owner request | bounded workers, reviewed dossier | tests |
| D8 | Agent mailbox polling | 5 s / 15 s | inbound task needs owner `accept`; outbound needs preview | tests; no live receipt |
| D9 | Auth/plugin watcher | 60 s | idle-time login check | tests |
| D10 | Hourly encrypted backups | systemd timer | restic with SQLite online backup | files only, no install evidence |
| D11 | Quota awareness | on demand | disables retry on usage limit | tests |

Not present in the donor: no email/calendar/RSS code, no webhooks, no heartbeat. "Email and
calendar summaries" exist only because reflection tells the model to review "all connected
sources" and the owner's account may have connectors. That makes it non-deterministic and
unauditable.

## 3. Mapping to gopod

| Donor behavior | gopod today | Candidate | Gap |
|---|---|---|---|
| D2 reminders, schedule policy | agent scheduling done (CP-015); managed tasks off | CP-022 | none: designed |
| D6 delivery | outbox live for the owner chat | CP-002 PR 1-5 merged | PR 6-8 pending |
| D7 tasks, research | managed tasks blocked on credential adapter | CP-004, CP-021, CP-023 | adapter decision taken 2026-10-30 (B2), not built |
| D3 idle maintenance, quiet hours | one-minute system deadline and same-chat preemption live (D033) | CP-006 | designed, not built |
| D10 backups | foundation merged | CP-007 | timer, restore drill |
| D4 learning | none | CP-024, CP-025, CP-026 | deliberately later |
| D8 mailbox | none | CP-030 | deferred |
| **D1 reflection / digests** | one owner schedule (freefeed digest, 09:00 Belgrade) | **none** | **no candidate** |
| D5 model-driven cleanup | none | none | not wanted (see section 6) |

## 4. Further analysis: specs to run before deciding

Each item is read-only or design-only and produces a short note under `docs/research/`.
None needs a live account, a flag change or a deploy.

**A1. Baseline of what gopod does unattended today.**
Question: what runs without a request, how often, at what quota cost, and how do we know it
worked? Method: list schedules and system runs from the store and logs (counts only, no
content), the freefeed digest history, extraction/compaction runs. Output: table of unattended
jobs with frequency, outcome rate and provider-turn cost. Acceptance: every unattended job is
listed with a way to observe its last outcome.

**A2. Proactive value test with the owner (no code).**
Question: would the owner actually read a daily/weekly reflection? Method: write three sample
messages from real (owner-approved) history by hand and ask the owner to rate usefulness and
interruption cost; pick the time window and quiet hours. Output: yes/no, preferred cadence,
categories wanted (calendar, mail, tasks, repos), categories refused. Acceptance: an owner
decision recorded in HANDOFF.

**A3. Source strategy for digests.**
Owner decision 2026-10-30: provider connectors (mail, calendar) are allowed in the private
chat only. The question is therefore how to make that acceptable, not whether: what the host
can still record (which connector tools ran, counts, timing), which scopes to request
(read-only), and when a host-side deterministic collector that hands the model a bounded,
redacted structure is worth building instead.
Method: compare on auditability, group-isolation fit (CP-020), credential exposure (the
credential adapter spec), failure visibility, and cost. Include what mail/calendar access the
owner already has through connectors on the Mac versus on the Pi. Output: a recommendation
with the minimum credential scope. Acceptance: states where each credential lives and who can
read it.

**A3a. Can a proactive run be made read-only on connectors? (optional; owner scope needs no restriction)**
Question: for each pinned CLI, can a single run be given only the read tools of the Gmail and
Calendar connectors (tool allow/deny list, per-run config, or a read-only Google scope)?
Method: read the CLI documentation and configuration schema for tool allowlists; if unclear,
a design-only note listing what must be tried on the Pi with synthetic data and no account
send. Output: yes/no per provider and the exact mechanism. Acceptance: a documented answer per
provider. It is needed only if the owner chooses an optional read-only switch for a job, or
for non-owner scopes; it is not a condition for owner-scope connector use.

**A4. Quota and budget model for proactive turns.**
Question: what do N proactive turns per day cost on the shared subscription, and what happens
at the 20/10/5 percent thresholds? Method: use observed foreground turn sizes and the donor's
history-injection design (whole history JSON in the prompt) to estimate a worst case.
Output: a cap proposal (turns per day, token ceiling, skip-on-low-quota rule). Acceptance:
a proactive job has a stated maximum cost and an automatic stop.

**A5. Safety review of autonomous turns.**
Question: which autonomous donor turns run with `danger-full-access`, and what is the gopod
equivalent given per-chat containers and the task-role policy (D044)? Method: map each donor
autonomous profile to a gopod role, mounts, network and credentials. Output: allowed
capability set per proactive job (read-only by default). Acceptance: no proactive job has a
write or send capability beyond the owner chat.

**A6. Idle and preemption fit.**
Question: can gopod's queue run maintenance only when idle and yield to a user turn (donor
D3)? Method: read the queue, preemption code and CP-006 spec; list what is missing.
Output: either "CP-006 as written is enough" or a delta. Acceptance: one paragraph per
missing piece.

## 5. Prioritization

Scoring: value to the owner (H/M/L), effort (S/M/L, gopod sizing), risk (H/M/L), and what it
needs first. Rank is my recommendation, not an approval.

| Rank | Item | Value | Effort | Risk | Needs | Recommendation |
|---|---|---|---|---|---|---|
| 1 | Task credential adapter (B2) then CP-004 PR 4-5 | H (unblocks every managed task) | M | M | owner answers (given 2026-10-30) | Take. Already decided. |
| 2 | CP-022 model-free reminders and schedule policy | H (zero quota, reliable) | M | L | CP-004 admission truth | Take. Cheapest autonomy win. |
| 3 | CP-006 quiet hours and idle-only maintenance | H (prerequisite for anything proactive) | S-M | L | CP-002 outbox (merged) | Take before any proactive turn. |
| 4 | A1, A2, A4 (analysis only) | M | S | L | none | Do now; they decide item 5. |
| 5 | **New candidate: proactive digest framework** ([CP-033](../proposals/CP-033-proactive-controls.md), built-in recurring presets for the main connectors, toggled with `/proactive`) | M-H | M | M | items 1-3, A2-A5 | Take as proposal-only digests, see below. Mail/calendar via connectors in the private chat is allowed. |
| 6 | CP-021 task outcomes and checkpoints | M | M | L | CP-004 | Take after item 1. |
| 7 | CP-007 timer and restore drill | M | M | L | owner approval for Pi work | Continue as planned. |
| 8 | CP-023 research dossiers | M | L | M | CP-004, CP-003 | Later. |
| 9 | CP-025 then CP-024 | M | M-L | M | CP-020 | Later; prerequisites for learning. |
| 10 | CP-026 learning | L-M | L | H | item 9 | Proposal-only if ever; no automatic trials. |
| 11 | CP-030 mailboxes, CP-028, CP-029 | L | L | M-H | CP-027 | Keep deferred. |

### Proposed shape of the digest candidate (for item 5, not a design)

- Cadence and categories come from A2; default off, owner-enabled per digest.
- Sources: the owner's private chat may use provider connectors for mail and calendar
  (owner decision 2026-10-30), read-only, with an explicit list of sources in the prompt
  instead of "review all connected sources", and a host record of which tools ran. Sources
  that do not need a connector (freefeed, repos, tasks) use host collectors with a fixed
  output schema and redaction, and the model only summarizes what they return.
- Delivery goes through the outbox with the `proactive` flag so CP-006 quiet hours apply;
  explicit reminders bypass quiet hours, digests do not.
- Owner private chat only, never a group. Read-only is an optional per-job switch, not a rule.
  A cost cap and a skip when quota is low (the 20/10/5 percent rules) are owner-configurable.
- The existing freefeed digest is not moved (owner decision 2026-10-30). CP-033 covers built-in
  recurring presets for the main connectors, managed with `/proactive`.
- Mail and calendar are the second step through connectors in the private chat. They must be
  read-only only if the owner chooses that switch (the owner scope has no such requirement).
  CP-027 does **not** cover connectors: it governs only brokered
  actions whose credentials the host holds (spec: "Full-access personal shells/native
  integrations that can bypass the broker are not silently claimed to be covered"). A
  provider connector holds its own Google consent, so the host cannot intercept a send made
  through it. Read-only therefore has to be enforced by what the proactive run is allowed to
  call (see A3a), and any send, reply or event change is available only in a normal owner
  turn or through a host-held mail credential under CP-027.

## 6. What not to take

- **Automatic profile or memory self-modification (D4).** The donor edits its own instruction
  files within a trial. gopod's CP-026 already says proposal-only first; keep it.
- **Model-driven cleanup with full access (D5).** Do cleanup as deterministic host code or
  not at all.
- **Provider connectors for mail/calendar outside the owner's private chat.** Owner decision
  2026-10-30: connectors are allowed in the private chat. They stay out of groups and out of
  every non-owner scope, because their reads cannot be audited by the host and do not fit
  per-chat isolation. The private-chat use carries the residual risk that a connector read is
  visible to the host only as the model's own account of it.
- **Donor operational choices:** one-second SQLite tick across services, inline schema
  changes without migrations, single 790-line service file, hard SDK/ChatGPT coupling.

## 7. Worth borrowing as design rules

1. A schedule occurrence is unique on `(schedule, time)`, and admitting it and advancing the
   due time are one transaction (restart cannot duplicate or lose a run).
2. Quiet hours are a delivery-time filter on a `proactive` flag, so explicit replies are never
   delayed by a backlog of suggestions.
3. Maintenance runs only when idle and resumes from a cursor.
4. A goal is closed by the owner's authenticated command, never by the model's claim.
5. A coverage mark advances only after a successful run, so a failed review is retried over
   the same window.
6. Uncertain outcomes are never replayed (already in gopod).

## 8. Open questions for the owner

1. Do you want proactive messages at all (A2), and which categories: tasks/repos, calendar,
   mail, project status? (Mail and calendar through connectors are allowed in the private
   chat; which connectors are actually enabled on your account?)
2. (Answered 2026-10-30: the freefeed digest stays as is; CP-033 covers connector and proactive-action controls only.)
3. May A1 read the production store and logs (counts only, via the existing read-only
   diagnostics), or should it use only repo evidence?
4. CP-033 now exists as `proposed` and covers only the owner controls; the digest/reflection behaviors stay uncreated until A2 gives a yes.

## 9. Result of A3a (2026-10-30, documentation and source only, nothing run with an account)

Question: can one run be limited to read-only connector tools for each pinned CLI? Both CLIs
were checked against current official docs (not the pinned tags) by two read-only research
passes; the Codex part also reads source on `main`. Treat "inferred" as unverified until the
synthetic test in the last paragraph passes.

| | Claude Code 2.1.310 | Codex 0.164.0 |
|---|---|---|
| Do restrictions survive the bypass flag gopod uses? | Yes, documented: "Deny rules block in every mode, including `bypassPermissions`"; `--disallowedTools` is a deny source | Probably yes, inferred from source: the app/tool enabled check runs before any approval. Approval modes (`prompt`, `writes`) are likely auto-approved under bypass, so rely on enable flags, not approvals |
| Allowlist of tools | **No.** `--allowedTools` only auto-approves; `--tools` does not touch MCP tools; an allow rule cannot carve out of a deny | Yes: `apps.<id>.default_tools_enabled=false` plus `apps.<id>.tools.<tool>.enabled=true` |
| Read vs write classification | None; write tools must be denied by exact name, so a tool added by a connector update is allowed by default (fail-open) | By hints: `destructive_enabled=false` blocks tools with `destructiveHint` true **or missing**, but not write tools that only carry `openWorldHint`; `readOnlyHint` is not used. Use the allowlist, not the hints |
| Per run | CLI flags per run; `disableClaudeAiConnectors` / `ENABLE_CLAUDEAI_MCP_SERVERS=false` turn all connectors off | `-c key=value` (dotted use inferred), `--profile`; avoid a per-run `CODEX_HOME` |
| Host-enforced layer the agent cannot override | Managed-settings deny; a `PreToolUse` hook (deny by exit 2) but whether it fires under bypass is not documented | `/etc/codex/requirements.toml`; pinning `features.apps=false` is the likely kill switch |
| Seeing what is available | `system/init` event lists MCP servers and tools in `stream-json`; denials arrive as `permission_denied` | `codex plugin list --json`, `codex mcp list`; no documented tool listing for apps |
| Audit of what ran | `tool_use` blocks in `stream-json` (main-thread form not quoted in docs) | `--json` emits `mcp_tool_call` events with server, tool, arguments, status |
| Unknown | whether connectors load in `-p`; exact server names for Gmail and Calendar | the app id of Gmail; whether dotted `-c apps.*` overrides work; behavior on the pinned tag |

Conclusions (for the optional read-only switch and for non-owner scopes only; see the owner
correction above):
1. **Claude cannot be made read-only fail-closed with deny rules alone.** Only deny-by-name is
   available, and connector updates can add write tools that pass. A default-deny `PreToolUse`
   hook would fix that if it fires under bypass, which is undocumented. Until a synthetic
   test shows it fires, a Claude proactive run must have connectors **off**.
2. **Codex can be made read-only with an explicit tool allowlist**, subject to learning the app
   id and exact tool names and confirming the settings apply on the pinned version.
3. For the owner's private chat nothing needs to change: connectors work as connected in owner
   turns and in proactive runs. The restriction mechanisms above matter only if the owner opts
   into read-only for a job, or for a non-owner scope.

Optional live check, needed only for the read-only switch or non-owner scopes (synthetic data, no send, owner approval needed): run one
headless turn per provider with a deny/allowlist configuration, ask the model to call one
read tool and one write tool of a throwaway connector or a local fake MCP server, and confirm
(a) the write call is blocked and recorded, (b) the read call runs, (c) a newly added tool on
the fake server is blocked by default, (d) a `PreToolUse` hook fires under the bypass flag.
A local fake MCP server needs no Google account.

### 9.1 Owner observation, 2026-10-30

The owner checked on a running agent (which one is not recorded) and confirmed that it reaches
Gmail through the provider's Gmail connector for Codex and Claude. So connectors **do** load in
a headless CLI on a subscription login; the "loads in `-p`" unknown in the table above is
answered yes for the observed setup. The exact tool names and the app id are still not
recorded, and this says nothing about restriction: the conclusions above stand. What changes:
the connector route is a real option, not a hypothetical, and the live check below is the one
remaining gate before a read-only connector configuration can be trusted. The host-side Gmail
API (section 10) remains the fallback if that check fails for a provider.

## 10. Optional: host-side Gmail and Calendar (not required for owner scope)

Owner request 2026-10-30. Own OAuth application, read-only scopes (`gmail.readonly`,
`calendar.readonly`), token held by the host as an environment secret (D012), a collector with a
fixed output schema and redaction that gives the model a bounded structure. Sending uses a
separate token and scope and goes through CP-027 approvals, which do apply because the host
holds the credential. To verify before choosing it: the Google consent-screen status needed so
refresh tokens do not expire after about seven days for a personal account, and the warning
for restricted scopes. IMAP with an app password is weaker because it cannot be scoped to read.

## 11. Prior art: Hermes Agent (Nous Research, MIT, commit `8dfc96d`, 2026-04-09)

Static read of a local clone; nothing run. Hermes has no Gmail or Calendar tool, MCP server or
read-only mode.

- **Gmail and Calendar** are a skill (`skills/productivity/google-workspace/`): a single Python CLI
  (`scripts/google_api.py`) over Google's client libraries, with its own OAuth desktop client.
  Scopes are all-or-nothing (`gmail.readonly`, `gmail.send`, `gmail.modify`, full `calendar`,
  Drive, Sheets, Docs); a token missing any is rejected, so a narrower read-only token is
  blocked, not allowed. "Confirm before sending" is one sentence of prompt text, with no code
  gate. The token file is written without a chmod and the 7-day testing-mode refresh-token expiry
  is not handled beyond a `REFRESH_FAILED` re-auth path.
- **Third-party route:** the same skill sends users who only want mail to the `himalaya` skill
  (an open-source IMAP/SMTP command-line client, app-password or OAuth). There is also an
  optional `agentmail` skill (a hosted mailbox service for the agent itself) and an IMAP/SMTP
  chat channel (`gateway/platforms/email.py`), which is the agent's own mailbox, not the
  owner's.
- **Proactive:** only a cron scheduler (60 s tick with a lock, fresh agent per job, 600 s
  inactivity timeout, jobs cannot create jobs, delivery-target whitelist, `[SILENT]` reply
  suppresses an empty digest but still saves output). A job can run a **pre-step script** whose
  stdout is injected into the prompt: deterministic collection and change detection before any
  model call. No quiet hours, cost caps, rate limits, per-job tool restriction or read-only
  profile. Management: `hermes cron list|create|edit|pause|resume|run|remove`.
- **MCP filtering:** per-server `tools.include` / `tools.exclude` lists in config (include
  wins), and each server is its own toolset. This is the shape a read-only list should have.
- **Safety:** command approval covers terminal commands only, not Google or MCP calls. Secrets
  are masked in logs, not email bodies; there is no untrusted-content handling for mail. Prompt
  injection scanning applies to cron prompt text at creation, not to fetched content.
- **Evidence:** cron and MCP have real tests; the Google skill has 9 tests covering scope
  handling, none for Gmail, Calendar or send.

What we take: the pre-step collector + change detection + silent-when-empty pattern, the
tick lock and inactivity timeout, a per-source include list, `list|pause|resume|run|remove`
job commands (CP-033 already has this shape), the PKCE paste-the-redirect OAuth flow (suits a
bot with no local browser), `gmail.readonly` and `calendar.readonly` only, and Gmail search
syntax. What we add ourselves: code-enforced read-only, an audit record of every Google call,
treating mail as untrusted data, quiet hours and a cost cap. Nothing is reusable as code
(Python, tied to its home layout); MIT permits copying with the notice, but gopod forbids
Python in the repo.

Third-party tools worth evaluating before building our own Gmail client (not yet checked, no
claims made): himalaya (Rust, IMAP/SMTP, Gmail OAuth). Evaluate on read-only enforcement
(it cannot scope an app password), audit, and whether it can run as a host subprocess instead
of inside the agent container.

## Sources

Donor `docs/features.md`, `src/service.js`, `src/conversations.js`, `src/scheduler.js`,
`src/config.js`, `src/store.js`, `src/learning.js`, `src/agent-mail.js`, `templates/CLEANUP.md`,
`systemd/codex-agent-backup@.timer` (read 2026-10-30, static). gopod: [adoption map](../proposals/2026-10-26-codex-agent-adoption.md),
[proposal registry](../proposals/README.md), [ROADMAP](../../ROADMAP.md), [HANDOFF](../HANDOFF.md),
[credential adapter spec](../superpowers/specs/2026-10-29-task-role-credential-adapter.md).
