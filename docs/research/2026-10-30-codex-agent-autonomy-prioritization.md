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
Question: provider connectors read by the model (donor) versus a host-side deterministic
collector that fetches a bounded, redacted structure and hands only that to the model.
Method: compare on auditability, group-isolation fit (CP-020), credential exposure (the
credential adapter spec), failure visibility, and cost. Include what mail/calendar access the
owner already has through connectors on the Mac versus on the Pi. Output: a recommendation
with the minimum credential scope. Acceptance: states where each credential lives and who can
read it.

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
| 5 | **New candidate: proactive digest framework** (proposed as CP-033, not created) | M-H | M | M | items 1-3, A2-A5 | Take as proposal-only digests, see below. |
| 6 | CP-021 task outcomes and checkpoints | M | M | L | CP-004 | Take after item 1. |
| 7 | CP-007 timer and restore drill | M | M | L | owner approval for Pi work | Continue as planned. |
| 8 | CP-023 research dossiers | M | L | M | CP-004, CP-003 | Later. |
| 9 | CP-025 then CP-024 | M | M-L | M | CP-020 | Later; prerequisites for learning. |
| 10 | CP-026 learning | L-M | L | H | item 9 | Proposal-only if ever; no automatic trials. |
| 11 | CP-030 mailboxes, CP-028, CP-029 | L | L | M-H | CP-027 | Keep deferred. |

### Proposed shape of the digest candidate (for item 5, not a design)

- Cadence and categories come from A2; default off, owner-enabled per digest.
- Collectors are host code with a fixed output schema and redaction; the model only
  summarizes what the collector returned. No "review all connected sources" prompt.
- Delivery goes through the outbox with the `proactive` flag so CP-006 quiet hours apply;
  explicit reminders bypass quiet hours, digests do not.
- Read-only role, owner chat only, never a group; one stated cost cap and an automatic skip when
  quota is low (the 20/10/5 percent rules).
- The first target is the existing freefeed digest moved onto the same framework, so no new
  data source is needed to prove it.
- Mail is a second step and needs its own credential decision (A3) and CP-027 approvals.

## 6. What not to take

- **Automatic profile or memory self-modification (D4).** The donor edits its own instruction
  files within a trial. gopod's CP-026 already says proposal-only first; keep it.
- **Model-driven cleanup with full access (D5).** Do cleanup as deterministic host code or
  not at all.
- **Provider-side connectors as the source of mail/calendar truth.** They cannot be audited
  and do not fit per-chat isolation.
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
   mail, project status?
2. Is the freefeed digest the right first workload to move onto a digest framework?
3. May A1 read the production store and logs (counts only, via the existing read-only
   diagnostics), or should it use only repo evidence?
4. Create CP-033 now as `proposed`, or hold until A2 gives a yes?

## Sources

Donor `docs/features.md`, `src/service.js`, `src/conversations.js`, `src/scheduler.js`,
`src/config.js`, `src/store.js`, `src/learning.js`, `src/agent-mail.js`, `templates/CLEANUP.md`,
`systemd/codex-agent-backup@.timer` (read 2026-10-30, static). gopod: [adoption map](../proposals/2026-10-26-codex-agent-adoption.md),
[proposal registry](../proposals/README.md), [ROADMAP](../../ROADMAP.md), [HANDOFF](../HANDOFF.md),
[credential adapter spec](../superpowers/specs/2026-10-29-task-role-credential-adapter.md).
