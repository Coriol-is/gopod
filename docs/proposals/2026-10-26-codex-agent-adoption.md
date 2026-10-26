# codex-agent adoption: saved candidates and delivery slices

Date: 2026-10-26. Status: **planning only; all candidates remain proposed**.
This map records the owner's request to save recommendations and deferred ideas.
It does not reorder the approved 2026-10-25 delivery queue or authorize runtime
changes. [Registry](README.md) tracks CP status; [ROADMAP](../../ROADMAP.md)
tracks approved priority; [shared contract](../superpowers/specs/2026-10-26-codex-agent-adoption-common-contract.md)
applies to every spec below.

## Inventory

| Proposal | Capability | Candidate wave | Size | Contract |
| --- | --- | --- | --- | --- |
| [CP-002](CP-002-telegram-outbox.md) | Durable delivery and uncertain outcomes | 1 | M | [Spec](../superpowers/specs/2026-10-26-telegram-outbox.md) |
| [CP-003](CP-003-typed-replies-via-ipc.md) | Typed artifact replies and immutable snapshots | 1 | M | [Spec](../superpowers/specs/2026-10-26-immutable-artifact-delivery.md) |
| [CP-004](CP-004-isolated-task-sessions.md) | Task-scoped native sessions and working sets | 1 | S–M | [Spec](../superpowers/specs/2026-10-26-isolated-task-sessions.md) |
| [CP-005](CP-005-tools-base-image.md) | Lightweight bases and optional tool runtimes | 4 | L | [Spec](../superpowers/specs/2026-10-26-optional-tool-images.md) |
| [CP-006](CP-006-yielding-maintenance-and-quiet-hours.md) | Foreground priority and quiet-hour delivery | 1 support | S–M | [Spec](../superpowers/specs/2026-10-26-maintenance-priority-quiet-hours.md) |
| [CP-007](CP-007-store-backups.md) | Matched backups and isolated restore qualification | 1 support | M | [Spec](../superpowers/specs/2026-10-26-matched-store-backups.md) |
| [CP-021](CP-021-task-outcomes-checkpoints.md) | Task outcomes, evidence and checkpoints | 1 | M | [Spec](../superpowers/specs/2026-10-26-task-outcomes-checkpoints.md) |
| [CP-022](CP-022-schedule-execution-policies.md) | Schedule overlap, misfire and model-free reminders | 1 | M | [Spec](../superpowers/specs/2026-10-26-schedule-execution-policies.md) |
| [CP-023](CP-023-research-dossiers.md) | Source-backed research dossiers and reviewed reports | 2 | L | [Spec](../superpowers/specs/2026-10-26-research-dossiers.md) |
| [CP-024](CP-024-memory-provenance-corrections.md) | Versioned memory provenance, corrections and forgetting | 3 | L | [Spec](../superpowers/specs/2026-10-26-memory-provenance-corrections.md) |
| [CP-025](CP-025-managed-profile-updates.md) | Managed profile sections and concurrent edit protection | 3 prerequisite | M | [Spec](../superpowers/specs/2026-10-26-managed-profile-updates.md) |
| [CP-026](CP-026-reversible-learning.md) | Sourced reversible learning with host evidence | 3 after memory/profiles | L | [Spec](../superpowers/specs/2026-10-26-reversible-learning.md) |
| [CP-027](CP-027-scoped-action-approvals.md) | Exact scoped action approvals and publication grants | cross-cutting before integrations | M | [Spec](../superpowers/specs/2026-10-26-scoped-action-approvals.md) |
| [CP-028](CP-028-telegram-account-reading.md) | Scoped Telegram user-account reading | 4 deferred | L | [Spec](../superpowers/specs/2026-10-26-telegram-account-reading.md) |
| [CP-029](CP-029-scoped-location.md) | Scoped location context and expiry | 4 deferred | M | [Spec](../superpowers/specs/2026-10-26-scoped-location.md) |
| [CP-030](CP-030-agent-mailboxes.md) | Authenticated agent mailboxes and bounded delegation | 4 deferred | L | [Spec](../superpowers/specs/2026-10-26-agent-mailboxes.md) |

CP-002–007 already existed and now have fuller specs. Their IDs are retained.
CP-003, CP-006 and CP-007 estimates grew with snapshots/recovery/retention scope.
CP-005 explicitly evolves the old no-sidecar draft into a lightweight base plus
optional controller-owned tool runtimes; it does not change personal defaults.
CP-021–030 are new candidates, not accepted implementation work.

## Candidate first wave: reliable results

Goal: a scheduled/agent task has an honest status, a safe execution session and a
result that is not lost, duplicated blindly or exported from a mutable path.

1. CP-007 baseline backup/restore protection can be prepared independently;
   extend its manifest when artifact storage arrives. This is a rollout gate,
   not a circular dependency on CP-003 implementation.
2. CP-002 durable text/notice delivery first; preserve streaming and existing
   private login behavior. Core personal delivery does not wait for all future
   CP-019 transport features, but cannot claim those features are enabled.
3. CP-003 immutable files/voice over that outbox, with disk quota/GC and matched
   backup support before rollout. Queue acceptance is not Telegram delivery.
4. CP-004 isolated task sessions and CP-021 outcome/checkpoints can be developed
   as bounded independent slices; integrate source/run IDs and final publication.
5. CP-022 schedule policies and literal reminders follow outcome/admission truth.
   Do not rewrite existing schedules, including the owner's digest, by assumption.
6. CP-006 foreground-first maintenance and quiet delivery supports these slices;
   no proactive model loop is enabled merely to demonstrate scheduling.

Group rollout needs the qualified CP-020 boundary. Personal-only reliability
work need not wait for every group or integration feature. CP-020 core model/tool
isolation does not depend on the later generic CP-027 approval UI: until that UI
and broker exist, deny the corresponding privileged external actions.

## Later value and quality waves

**Wave 2 — research:** CP-023, after scoped task sessions and reliable artifacts.
Start with Markdown/evidence export; PDF is optional until a qualified document
pack exists. Keep research corpus separate from personal-fact memory. A reviewer
checks retained passages, not the truth/exhaustiveness of the entire Internet.

**Wave 3 — durable quality:** CP-025 managed profile/version updates and CP-024
memory provenance/corrections precede CP-026 learning. Memory core can evolve
without first projecting profile sections; only projections depend on CP-025.
Learning starts proposal-only and requires a separate policy for automatic trials.
Model review or silence is not evidence that a lesson helped.

**Cross-cutting before privileged integrations:** CP-027 exact action approvals.
It is useful for publishing from private context into a group and for peer tasks;
it governs only brokered surfaces, not arbitrary full-access personal shell use.

## Second-echelon integrations and optional tooling

- **CP-028 Telegram user-account reader:** opt-in personal read broker with narrow
  peers/time/size grants; a user session never enters group tool execution.
- **CP-029 location:** explicit scoped pins/defaults, expiry and clear; no inferred
  home or private-location fallback in groups. Maps billing is separately chosen.
- **CP-030 peer mailboxes:** authenticated known peers, exact task acceptance and
  scoped outgoing approval; no unbounded delegation or automatic host/SSH access.
- **CP-005 optional tool images:** small common base where useful, heavy office,
  browser, OCR and media packs only through named capabilities. Pi cold-start,
  cache, memory, disk and foreground responsiveness are release gates. No giant
  default image or runtime package installer is introduced.

These are deliberately described rather than silently dropped from backlog.
They are not dependencies of first-wave text delivery. Browser sessions and a
private/shared skill catalog are possible follow-ups inside CP-005/CP-011 and
CP-025; no additional standalone work item or account grant is accepted here.

## Deferred decisions, not blockers to saving designs

Before implementation plans: agree first-wave slice order relative to current
priorities; choose data retention/storage caps; define unknown-effect operator
reconciliation; select exact permitted integration actions; validate native
account/tool grants and Pi resource budgets. Conservative defaults and checkable
acceptance are already in each spec; account/billing/credential decisions remain
explicit when the relevant later feature is considered.

## Source/evidence boundary

Reviewed local donor checkout: rzaytsev/codex-agent at
b231ed572c6b4acaff057e715570b867aa49a75b, especially docs/features.md,
docs/task-outcomes.md, docs/artifacts.md, docs/research.md, docs/learning.md and
the referenced src modules. Existing gopod proposals, runtime prototype, memory,
scheduler and roadmap were checked for duplicates. No private instance material,
authentication files, live Telegram data or deployed behavior was imported.
Source mechanics are not blanket proof of donor or gopod production acceptance.
