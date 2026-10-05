# Change proposals (CP)

A CP is the step before a design. It records *why* we might change gopod,
how big the change is, and what "done" means, so the idea survives a
session boundary without turning into scope creep. ADRs record decisions
already taken ([DECISIONS.md](../DECISIONS.md)); specs record approved
designs (`docs/superpowers/specs/`); a CP records a candidate.

## Lifecycle

```
proposed ──► accepted ──► in-progress ──► done
   │             │
   └─► rejected  └─► superseded
```

| Status | Meaning | Who moves it |
|---|---|---|
| `proposed` | Written down, not yet discussed to a decision | anyone |
| `accepted` | Operator said go. Next step is a spec + plan via the superpowers flow (brainstorm → spec → plan → execute) | operator |
| `in-progress` | Spec exists, work started; CP links the spec and plan | session doing the work |
| `done` | Shipped on `main`; ROADMAP has a row; ADR written if a real decision was made | session that shipped it |
| `rejected` | Decided against; keep the file with the reason so it is not re-proposed | operator |
| `superseded` | Replaced by another CP or ADR; link it | anyone |

## Rules

- One file per CP: `docs/proposals/CP-NNN-<slug>.md`, numbers never reused.
- The index below is the registry. `/handoff` cross-checks it the same way
  it cross-checks ROADMAP statuses.
- A CP never contains the design. If the "Proposal" section grows past a
  screen, stop and write the spec instead.
- Size is a gut call: S = one session, one package; M = a few sessions, a
  few packages; L = needs its own milestone row in ROADMAP.
- "Acceptance" must be checkable by someone who did not do the work.

## Template

```markdown
# CP-NNN — <title>

| | |
|---|---|
| Status | proposed |
| Date | YYYY-MM-DD |
| Origin | where the idea came from (review, incident, other project, user) |
| Size | S / M / L |
| Touches | packages / files |
| Depends on | other CPs, milestones, or `—` |
| Spec | `—` until written |

## Problem
What hurts today, with evidence (file:line, log line, incident).

## Proposal
What changes, in a paragraph. No design detail.

## Out of scope
What this CP deliberately does not do.

## Risks
What could go wrong, what it costs if it does.

## Acceptance
Bullet list someone else can verify.
```

## Index

| CP | Title | Status | Size |
|---|---|---|---|
| [CP-001](CP-001-memory-api-capability-token.md) | Capability token for the memory API | proposed | S |
| [CP-002](CP-002-telegram-outbox.md) | Telegram outbox with retry and `uncertain` state | proposed | M |
| [CP-003](CP-003-typed-replies-via-ipc.md) | Files and voice as typed replies via IPC | proposed | S |
| [CP-004](CP-004-isolated-task-sessions.md) | Scheduled tasks run in their own session | proposed | S–M |
| [CP-005](CP-005-tools-base-image.md) | Shared tools base image for agent containers | proposed | M |
| [CP-006](CP-006-yielding-maintenance-and-quiet-hours.md) | Maintenance yields to the user; quiet hours | proposed | S |
| [CP-007](CP-007-store-backups.md) | Scheduled store backups on the production host | proposed | S |
