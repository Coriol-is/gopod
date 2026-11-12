# Change proposals (CP)

A CP is the step before a design. It records *why* we might change gopod,
how big the change is, and what "done" means, so the idea survives a
session boundary without turning into scope creep. ADRs record decisions
already taken ([DECISIONS.md](../DECISIONS.md)); specs record approved
designs (`docs/superpowers/specs/`); a CP records a candidate.

Status reconciled 2026-11-05 against [code-backed BACKLOG](../BACKLOG.md),
[HANDOFF](../HANDOFF.md) and
[ROADMAP](../../ROADMAP.md#current-backlog--2026-11-04). CP-002/003/004/007/016
retain incomplete scope; CP-020/038/040 have implemented code with named acceptance
gaps; CP-035 remains proposed. A status refresh does not activate deferred work.

Completed slices and superseded choices are kept in the
[backlog archive](../history/BACKLOG-2026-11-05-completed.md). The registry retains
all CP identities; an archived slice does not complete its partial parent proposal.

## Lifecycle

```
proposed ──► accepted ──► in-progress ──► done
   │             │
   └─► rejected  └─► superseded
```

| Status | Meaning | Who moves it |
|---|---|---|
| `proposed` | Written down, not yet discussed to a decision | anyone |
| `accepted` | Operator said go. Next step is the superpowers flow: brainstorm, then for S an in-chat design and direct execution, for M/L a spec + plan | operator |
| `in-progress` | Spec exists, work started; CP links the spec and plan | session doing the work |
| `implemented` | Implementation shipped; named provider/client qualification remains separate (CP-020/038/040) | session that shipped it |
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

## Reliability documentation wave — 2026-10-26

CP-002/003/004/006/007/021/022 have source-backed specifications and bounded
implementation plans. See the [integration order](../superpowers/plans/2026-10-26-reliability-wave.md).
Documentation readiness does not mark implementation started or features enabled.
The owner waived CI waiting for this Markdown-only wave; independent review and
local consistency/link checks remain required.

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

Priority/order lives in [ROADMAP execution priority](../../ROADMAP.md#execution-priority--2026-11-04),
reprioritized 2026-11-04 after the code audit; the 2026-10-25 order is historical.
The [2026-10-26 adoption map](2026-10-26-codex-agent-adoption.md) groups new and
updated designs for discussion; it does not replace that approved queue.
`done` means implemented on main; live acceptance remains a separate register.
`accepted` entries explicitly marked design-only authorize their saved design,
not enabling the feature merely by merging that document.

| CP | Title | Status | Size |
|---|---|---|---|
| [CP-001](CP-001-memory-api-capability-token.md) | Capability token for the memory API | done | S |
| [CP-002](CP-002-telegram-outbox.md) | Telegram outbox with retry and `uncertain` state (PRs 1-5 of 8 merged, plus rich-message formatting P1 / D045; PR 6a/6b and immutable final-plan/ready recovery and D065 conditional code-inline slices merged; process-local final-owner handoff merged in PR #174, D066 durable Stop merged in PR #176; durable ACK reply-action readthrough merged in #183; local control/final arbitration shipped in #187; D069 durable repair barrier shipped in #189; D070 private direct edits shipped in #191; D071 private placeholder deletion shipped in #193; historical/compatibility/non-edit reconciliation, full lifecycle parity, foreground cutover and PR 7-8 pending) | in-progress | M |
| [CP-003](CP-003-typed-replies-via-ipc.md) | Typed IPC replies with immutable file/voice snapshots (PRs 4-5 of 8 merged) | in-progress | M |
| [CP-004](CP-004-isolated-task-sessions.md) | Scheduled tasks run in their own session (task-reliability PR 1-3 of 5 merged: storage, managed dispatch/recovery, policy-guarded execution and reports; PR 4-5 pending) | in-progress | S–M |
| [CP-005](CP-005-tools-base-image.md) | Lightweight bases and optional tool runtimes | proposed | L |
| [CP-006](CP-006-yielding-maintenance-and-quiet-hours.md) | Maintenance yields to the user; quiet hours | proposed | S–M |
| [CP-007](CP-007-store-backups.md) | Matched scheduled backups and restore qualification (slices 4-6 foundation in PR 1; timer/full capture/restore pending; leases needed for online matched mode) | in-progress | M |
| [CP-008](CP-008-model-per-chat.md) | `/model` per chat | done | S |
| [CP-009](CP-009-provider-continuity.md) | Continue conversations across providers | done | M |
| [CP-010](CP-010-quota-switch-confirmation.md) | Confirm switching after quota exhaustion | done | M |
| [CP-011](CP-011-sandbox-runtime.md) | Sandbox runtime contract (design only) | accepted | L |
| [CP-012](CP-012-provider-registry.md) | Provider registry and quota choices (design only) | accepted | M |
| [CP-013](CP-013-transcript-provenance.md) | Speech transcript provenance | done | S |
| [CP-014](CP-014-telegram-media-input.md) | Telegram media — three delivery queues (design only) | accepted | L |
| [CP-015](CP-015-agent-scheduling.md) | Generic agent scheduling and IPC receipts | done | M |
| [CP-016](CP-016-telegram-topic-deletion.md) | Telegram topic deletion and conversation retirement (store/outbox and native/queue/transport fences deployed; main-control/noncontrol and private topic text ownership deployed; PR197 adds registered-private nontext ownership, gated command orchestration and exact deletion observation; historical/unknown reconciliation and native/client activation pending) | in-progress | M |
| [CP-017](CP-017-telegram-capability-diagnostics.md) | Telegram capability diagnostics (initial private-DM slice) | done | M |
| [CP-018](CP-018-per-thread-providers.md) | Provider selection per conversation (deployed in56d1a7d; full native acceptance open) | in-progress | M |
| [CP-019](CP-019-telegram-ephemeral-commands.md) | Private command/service/error replies in groups (design only) | accepted | M |
| [CP-020](CP-020-personal-group-isolation.md) | Personal/group isolation (controller deployed; groups admitted by default once registered, D047; final provider/client qualification open) | implemented | L |
| [CP-021](CP-021-task-outcomes-checkpoints.md) | Task outcomes, evidence and checkpoints | proposed | M |
| [CP-022](CP-022-schedule-execution-policies.md) | Schedule overlap, misfire and model-free reminders | proposed | M |
| [CP-023](CP-023-research-dossiers.md) | Source-backed research dossiers and reviewed reports | proposed | L |
| [CP-024](CP-024-memory-provenance-corrections.md) | Versioned memory provenance, corrections and forgetting | proposed | L |
| [CP-025](CP-025-managed-profile-updates.md) | Managed profile sections and concurrent edit protection | proposed | M |
| [CP-026](CP-026-reversible-learning.md) | Sourced reversible learning with host evidence | proposed | L |
| [CP-027](CP-027-scoped-action-approvals.md) | Exact scoped action approvals and publication grants | proposed | M |
| [CP-028](CP-028-telegram-account-reading.md) | Telegram user-account connector, read and write, Go port (spec written; owner decisions recorded) | proposed | L |
| [CP-029](CP-029-scoped-location.md) | Scoped location context and expiry | proposed | M |
| [CP-030](CP-030-agent-mailboxes.md) | Authenticated agent mailboxes and bounded delegation | proposed | L |
| [CP-031](CP-031-telegram-restart.md) | Owner Telegram restart with durable recovery notices (baseline owner-accepted; edge-case acceptance separate) | done | M |
| [CP-032](CP-032-telegram-reaction-reading.md) | Read reactions and custom emoji with explicit private/non-admin limitations (design only) | accepted | M |
| [CP-033](CP-033-proactive-controls.md) | Built-in recurring presets for the main connectors, toggled and scheduled with `/proactive` (first set: Gmail and Calendar) | in-progress | M |
| [CP-034](CP-034-private-chat-plugin-install.md) | Install official skills and plugins from the private chat; installed set survives container recreation | accepted | M |
| [CP-035](CP-035-agent-cli-hooks-research.md) | Research which in-container behaviors can move onto Claude Code and Codex hooks | proposed | S |
| [CP-036](CP-036-private-chat-stop.md) | Explicit inline Stop and /stop for owner-private answers | done | M |
| [CP-037](CP-037-parallel-sessions.md) | Parallel private topics and background sessions (P0 lifecycle implementation) | in-progress (P0 and P1 merged, deployed in legacy mapping) | L |
| [CP-038](CP-038-elevated-host-session.md) | Elevated host session deployed; bounded Codex check passed, Claude browser completion open | implemented | M |
| [CP-039](CP-039-full-backup-restore-migration.md) | Automatic full backup/restore (files, sessions, auth) and device migration (draft) | proposed | L |
| [CP-040](CP-040-agent-attachments.md) | Agent attachments: discoverable file delivery from the agent on top of CP-003; honest refusal; document presentation ([spec](../superpowers/specs/2026-11-02-agent-attachments.md), [D058](../DECISIONS.md); real-client acceptance pending) | implemented | M |
| [CP-041](CP-041-telegram-widgets.md) | Private-chat status widgets ([product](../superpowers/specs/2026-11-11-telegram-widget-panel.md), [protocol](../superpowers/specs/2026-11-11-telegram-widget-protocol.md)); design only, implementation not authorized | accepted | M |
