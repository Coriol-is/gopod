# CP-006 — Maintenance yields to the user; quiet hours

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-05 |
| Origin | feature survey of `rzaytsev/codex-agent` (2026-10-05) and the 2026-10-03 public-release audit |
| Size | S–M |
| Touches | `internal/runner` (compact watcher, extraction), `internal/scheduler`, `internal/telegram/ipc_sink.go`, `internal/config` |
| Depends on | Existing cancellation/admission; CP-002 for durable quiet-hour delivery |
| Plan | [Implementation slices](../superpowers/plans/2026-10-26-reliability-operations.md) |
| Spec | [Design](../superpowers/specs/2026-10-26-maintenance-priority-quiet-hours.md) |

## Problem
Same-folder cancellation and a one-minute fresh-run budget already exist. Durable re-arming,
transactional memory/checkpoint application, success-only compaction bookkeeping and quiet
proactive delivery remain incomplete. Failed summary storage must not clear native context.

## Proposal
Use revision-guarded durable maintenance requests and exact-session recoverable rotation.
A new disabled operator quiet policy with explicit IANA zone delays proactive CP-002 outbox
items within bounded expiry/queue limits; direct responses and valid private auth flows stay
immediate. No existing timezone env setting or general priority scheduler is assumed.

## Out of scope
Per-chat quiet hours; changing personal defaults or enabling group execution before CP-020.

## Risks and acceptance
Cancellation, stale source, insert failure and restart must preserve valid context or a durable
rotation receipt. Test idle re-arm, atomic fact supersession, success-only counters and bounded
DST-aware private-safe outbox delay. Exact slices and release gates are in the linked plan.

## 2026-10-25 reconciliation
Agent admission and same-chat serialization now bound background work, but did
not implement user-priority preemption or quiet-hour delivery. GOPOD_TIMEZONE
in this proposal is a future setting, not an implemented deployment variable;
current cron supports explicit CRON_TZ. Define retention/expiry for private
CP-019 replies before delaying any delivery window.

## Live-incident implementation slice
The 2026-10-25 extraction stall prompted a narrow bug fix: one-minute RunFresh
budget, same-chat foreground cancellation, process cleanup and preservation of
context on canceled compaction. See the dated incident report and release
handoff for merge/deploy evidence. This does not complete maintenance re-arming,
quiet-hour delivery or the full acceptance of this proposal; status stays proposed.

## 2026-10-26 adoption design

The linked spec now defines this candidate in the [adoption map](2026-10-26-codex-agent-adoption.md),
wave **1 support**. Status remains proposed; a saved design does not approve
implementation or change the existing delivery queue. New acceptance/migration
requirements in the dated spec supersede conflicting early shorthand here.
