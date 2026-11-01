# CP-037 second design review and restart audit

2026-11-01. Baseline main cee5cac. Reviewer: same authoring agent; this is a source-
backed self-review, not independent approval. No code/runtime was changed.

## Verdict

Implementation contracts are coherent for staged work with qualification gates.
They do not prove uninterrupted runtime today. Critical current restart/idle
behavior needs P0 before parallel activation. No implementation/deploy approval
is inferred from the request to write specifications.

## Findings reconciled in the specifications

| Finding | Severity | Resolution / remaining boundary |
|---|---|---|
| Idle watcher can remove busy foreground runtime; chat mutex does not span run | high | B1/C4 operation claims; C01-C03 tests required; current code unchanged |
| /restart cancels root context, not natural completion | high | C2 separate drain/admission lifecycle; cancel app only at ready |
| Startup cleanup treats old/dev version as permission to stop | high | C4 reconcile instead of blanket cleanup; unknown runtime held |
| Proposed persistent task context conflicts with CP-004 ephemeral attempts | high | A3 explicitly preserves CP-004; durable files/results are separate |
| New parallel topics can bypass legacy-main task exclusion | high | B2 requires chat-wide legacy exclusion until migration |
| /new pointer change can race ingress and late memory/result writes | high | A2 barrier/cutoff/transition inbox and A3 event generations |
| Force could resurrect cancelled background work through recovery/misfire | high | C3 cancellation intent/tombstones and suppression through resume watermark |
| Snapshot before admission barrier can miss a newly spawned runtime | high | C2 freeze then reconcile inventory/claims before ready |
| Docker unreachable must not become fabricated successful cancellation | high | C3 execution_unknown retains reservations; force blocked |
| Filesystem preparation is not atomic with SQLite | medium | A2 manifest prepare -> pointer commit -> idempotent finalize |
| Reserved user slot conflicts with full background utilization | medium | B2 explicit reservation; no idle borrowing/preemption in first version |
| Runtime eviction loses installed tooling/process state | medium | B4 environment manifest and explicit transient-state boundary |

## Evidence and verification performed

Read runner/lifecycle.go, runner.go, task_run.go, exec_guard.go, continuity.go;
cmd/gopod/restart.go, shutdown.go and main.go; queue.Close; existing task schema
and CP-004 native execution contract. Lifecycle comments themselves acknowledge
missing busy protection. Foreground cancellation watcher kills on context cancel;
managed tasks refresh activity periodically, which is not a mutual-exclusion lock.

Ran on the documentation worktree:
`go test ./internal/runner ./cmd/gopod ./internal/queue -run 'Test.*(Idle|Leftover|Restart|Shutdown|Recycle|ProviderSwitch)' -count=1`
All three packages passed. This is existing targeted unit/regression coverage,
not a new drain implementation test, complete suite, native-provider run or live
Docker interruption experiment. No production inference, restart or task mutation.
Markdown local links and diff whitespace checked separately.

## Remaining gates

- Native provider home extraction and credential-refresh concurrency qualification.
- CP-004 task role adapter and CP-007 matched native/workspace restore.
- Concrete host envelopes, disk reserve and cgroup enforcement before auto mode.
- Test C01-C14 against actual implementation with disposable Docker workloads;
  native-provider/Telegram acceptance remains distinct.
- Graceful deployment must precede supervisor replacement; external hard kill,
  host outage and OOM cannot guarantee uninterrupted LLM inference.

No exact-once external effects, arbitrary active CLI migration or blanket group
restart support is claimed. Source implementations remain at the audited baseline.

## Owner-requested final additions

Recorded C6: partial force failure is distinct from successful restart; cancelling
a drain has a CAS boundary; external effects can survive local process termination
and retain independent conflict guards. Added C15-C17 acceptance cases.
Recorded A5: scoped archive viewing/export, explicit guarded deletion, resumable
purge, retention/backups and disk-limit behavior. Added A13-A17; detailed archive
UI/API remains a separate bounded follow-up. No implementation or live tests added.

## Third consolidated logic review: routing and balancing

Owner requested consolidation of balancing, mixed providers, model changes and
quota/outage transitions. Added normative contract D, linked A6/B5/C7 and P3b.
This is another same-author consistency review, not independent verification.

| Scenario / finding | Resolution |
|---|---|
| Account aliases could bypass limits | D1 opaque shared account-budget identity; no automatic account substitution |
| Claude blocked at queue head could idle Codex capacity | D1 skip ineligible session heads across sessions, preserve per-session FIFO |
| Returning provider misses intermediate conversation | D3 per-native consumed-event cursor and bounded provenance-aware envelope |
| Smaller model cannot accept old context | D2 qualified compatibility or explicit degraded handoff; no silent truncation |
| Persisted selection is not atomic with Docker spawn | D5 durable intent/attempt and reconciliation; visible postcommit target failure |
| Source timeout mistaken for safe retry | D4/D5 unknown execution and effect reconciliation gate |
| Pending provider wait deadlocks graceful restart | D6/C7 distinguishes queued waits from actual runtime operations |
| Force restarts a pending fallback on recovery | D6 cancellation of fallback lineage before signals |
| Manual selection waits for failed turn that waits for selection | D6 one control-operation barrier; explicit continue-or-close resolution |
| Confirmation races clear/new/default change | Expected scope/session/epoch/settings and admission revisions; stale offer rejected |
| Task fallback elevates authority or mutates interactive provider | D6 retains CP-004 role/delivery; no automatic task fallback in initial scope |
| Target failure causes unbounded provider ping-pong | D5 one authorized alternate attempt; no automatic switch-back |

Reviewed paths conceptually: queued request -> mixed admission -> frozen attempt;
A/B/A continuation; rate-limit wait -> graceful drain; quota -> fallback -> Stop;
quota -> fallback -> new/clear; force during target preparation; crash before/after
selection commit and native start; task failure while source topic switches.
Acceptance D01-D16 records executable checks required later. No new source code
exists to run these cases against, and no native/provider probes were performed.

Remaining implementation gates unchanged: pinned CLI migration/model compatibility,
credential concurrency, account-bound classification, complete backup/restore and
measured host envelopes. Automatic task failover, active stream reattachment and
multi-host placement remain out of scope. Review verdict: suitable for staged
implementation review, not a claim that all runtime risks have been eliminated.

## Telegram controls after emergency interruption

Owner requested durable repair of messages with Stop buttons. Source inspection:
agentwait.go records early placeholders but streamNotStarted returns without UI
repair when the parent context is cancelled; stop.go inflight state is in-memory.
These observations do not establish a complete current crash-repair mechanism.
C8/A7 now require desired/acknowledged presentation, exact attempt bindings,
unknown-versus-dead wording, markup clearing and recovery of terminal edit failures.

Logic checks covered old button vs new attempt, terminal vs late stream writes,
unknown execution vs killed process, completed result vs delivery failure, lost
message-ID persistence, Telegram outage/429 and deleted topics. C18-C26 records
future tests. No new runtime tests or live Telegram mutations performed. A network
outage or lost creation receipt prevents a guarantee of immediate removal; this
limit is explicit and stale callbacks must remain harmless regardless of UI repair.

C9 consolidates queued/started/unknown/terminal states and safe Start/Continue
semantics into one normative UI/recovery table. C27-C29 cover recovery dispatch
races and stale callback acknowledgement despite failed message edits. No runtime
verification is claimed for these newly specified cases.
