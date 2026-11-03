# CP-037 implementation slices

2026-11-01. Planning only; no source implementation or rollout authorized.
Contracts: [A: state](../specs/2026-11-01-session-state-implementation.md),
[B: runtime](../specs/2026-11-01-session-runtime-implementation.md),
[C: restart](../specs/2026-11-01-session-restart-implementation.md),
[D: routing](../specs/2026-11-01-session-routing-implementation.md).

| Slice | Ownership / change | Depends on | Exit gate |
|---|---|---|---|
| P0 lifecycle safety | internal/runner lifecycle/agentkill/exec_guard, cmd/gopod restart/shutdown, queue drain API, control restart parser; runtime operation claims even for legacy mode | none | C01-C14; no busy TTL cleanup, graceful drain and explicit force; normal response keeps streaming during drain |
| P1 durable identity | internal/store, queue binding adapters, recovery | P0 | A01-A02/A06/A11, additive migration, serial-mode compatibility |
| P2 scoped controls | internal/control and telegram, runner session/compact, store operation journal | P1 | A03-A08; clear/new/help/pickers and stale callback handling |
| P3 dedicated runtime | runner mounts/provider/lifecycle, host migration manifest, CP-018 resolver integration | P1 | B01-B03/B09-B10/B13-B14; native migration and credential qualification per provider |
| P3b routing and continuity | store/runner/provider resolver, control selection/fallback, outbox lineage | P2/P3 | D02-D11/D14-D16; qualified native model switch, explicit fallback only |
| P4 bounded parallel queue | queue admission/fairness, runner process accounting, task adapters | P3b | B04-B06/B11-B12/D01, task role qualified, legacy chat exclusion, fixed-cap Docker checks |
| P5 restart integration | restart inventory and per-session generation mapping, outbox/result settlement | P4 | rerun all C cases plus D12-D13 with multiple topics/providers/tasks; force suppression adapters cover all task classes |
| P6 adaptive policy | internal/config, admission telemetry, status UI, qualified host profile | P5 | B07-B08, pressure/cooldown/limits; no arbitrary numeric defaults |
| P7 activation evidence | deployment/backup runbooks and ACCEPTANCE, superseding ADRs | P6 plus CP-004/007 gates | matched restore/rollback, explicit real-client acceptance, separately authorized activation |

P0 can deliver protection for the current serial deployment before enabling
parallel sessions; it must not depend on a completed new session schema. Avoid
two lifecycle implementations: extend its runtime claims with session IDs later.
Each source slice uses build/vet/full tests, affected concurrency race tests, PR
review and green CI including Docker isolation. Use deterministic barriers and
inject failures around durable intent, spawn, termination and result commit.
Documentation-only checks do not substitute for these future gates.

Before implementation resolve concrete provider credential provisioning and native
home migration adapters for each pinned provider/version. Auto admission above
serial capacity requires a measured host profile and enforced cgroups; T9 reboot
remains separately scoped. Recurring native task continuation, multi-host serving,
live controller stream reattachment and group restart expansion are excluded.

## Follow-up scope recorded after review

P0 remains the first implementation target: lifecycle safety and both restart
modes, including C15-C17 operator-visible partial failure, cancel-drain races and
external-effect guards. Do not wait for autoscaling to fix current interruptions.
A5 defines archive listing/export/deletion boundaries; a separate bounded follow-up
must design the command/API and integrate existing backup/artifact retention before
shipping archive management. It does not expand P0 into session archive work.

## Telegram interruption status recovery

P0 includes C8 basic durable placeholder binding/repair for the existing serial
runner and C18-C25; implement with current turn/attempt identity without depending
on the future session schema. P1/P2 extend binding to session epochs. P5 verifies
C26 across parallel sessions and fallback. Ownership: internal/telegram status,
stop, streaming and wait placeholders; internal/store bindings; existing delivery
adapter and cmd/gopod startup recovery. No new competing dispatcher. Pending UI
repair cannot retain execution locks; actual process uncertainty still does.

C9 is the canonical recovery presentation/action matrix. Include C27-C29 alongside
C18-C26 in the corresponding P0/P5 recovery acceptance gates; do not implement a
generic Retry that conflates provably not-started requests with started attempts.
