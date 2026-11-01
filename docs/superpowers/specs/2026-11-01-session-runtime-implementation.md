# CP-037 implementation contract B: runtime ownership and scheduling

Status: implementation specification for review, 2026-11-01; no activation.
[State](2026-11-01-session-state-implementation.md) ·
[Restart](2026-11-01-session-restart-implementation.md).

## B1. Runtime eligibility and ownership

One dedicated runtime per active session; one native attempt writer per session.
Bind immutable session, authority profile, provider, image digest and runtime
generation in host records and Docker labels, and inspect actual mounts/env/
resource configuration before reuse. Model is an attempt setting. Reuse a warm
runtime only for the same compatible session/profile. No arbitrary remount of a
live runtime and no shared writable native CLI home. Credential refresh and
provider session migration must be separately qualified; nil qualification denies.

All container mutations MUST pass one lifecycle coordinator: ensure/reconcile,
provider/image change, idle eviction, startup cleanup, policy change and restart.
No low-level stop/remove bypass for a known busy or uncertain runtime. Registered
operations include starting, executing, waiting on a tool/approval, result capture
and auxiliary auth/maintenance commands. Acquisition and draining mark occur under
the same runtime lock/CAS so a new exec cannot enter after the idle check.

Idle means no operation claims, no unknown execution, no unfinished result capture,
and elapsed TTL since last settled activity. Silence of LLM output is not idle.
Heartbeats diagnose health; they never establish permission to delete a runtime.
Scale-down/image update sets draining, admits no new operations and waits. At
completion persist result/checkpoint references before releasing execution claims.
External delivery can finish through the durable outbox without keeping a worker.
Detached children must be reaped or explicitly registered; unknown children block
planned teardown. Container limits include subprocesses and in-container agents.

## B2. Admission and fairness

`serial` preserves current chat serialization, `parallel` uses a fixed cap,
`auto` adapts under that cap. Proposed config lives in internal/config with
validated bounds and no secret values. Default serial; max-running retains the
existing operator ceiling. Do not alter feature flags or schedules on migration.

Select FIFO heads per session with round-robin fairness across conversations.
Acquire session/project/admission claims transactionally; a blocked project holds
no worker slot. Project claims are canonical host IDs, not path strings supplied
by agents. RW aliases into shared projects must be reconciled before parallel
writes; otherwise refuse that profile. Git worktrees are distinct write resources
but common repository metadata/publication requires its own coordination.

With cap >=2 reserve one slot for interactive demand; background active count
<= cap-1. Offer background dispatch after at most four interactive dispatches
when an eligible background request can fit; cap=1 uses the same weighted choice
at release, without preemption. No completion-latency promise for unbounded jobs.
Retain CP-004 task timeouts and no implicit timeout extension; show explicit task
expiry/cancellation separately from planned restart. Maintenance uses its own
existing bounded policy. Legacy-main tasks retain chat-wide exclusion, including
all newly parallel topic lanes, until explicitly migrated; they cannot race those
lanes merely because they use an old adapter.

Group jobs, maintenance and parser inference count against the same host budget.
External connector conflicts require scoped operation IDs/idempotency/conflict
rules; project locks alone cannot prevent duplicate publications or messages.
Provider/account concurrency and throttling form a separate shared bound.

## B3. Adaptive capacity qualification

Fixed cap ships before auto mode. Auto requires enforced memory/CPU/PID limits,
fresh host telemetry and measured runtime envelopes including tools. Missing or
stale telemetry freezes scale-up; low capacity does not trigger fail-open extra
starts. Budget host reserve, startup reservations and unconsumed growth headroom
without subtracting resident usage twice. Sustained swap/IO pressure and disk
watermarks veto scale-up. Numeric envelopes/watermarks must be supplied by a
qualification report, not guessed defaults. Idle runtime count and retained disk
are bounded separately from active agents. No automatic deletion of session data.

Controller and Stop/reconciliation have resources reserved outside worker
admission. Bounded queues reject before acceptance with a visible reason, never
drop committed input. Define disk high-water rejection and emergency metadata
reserve in the host profile. Persist cooldown/backoff so restart does not create
an account-throttling storm. Scale down by draining only; TTL never kills busy work.

## B4. Ownership recovery and observability

Acquire controller instance lock before reconciling labeled containers and durable
claims. Do not infer death from an expired lease, missing stream or version mismatch.
Uncertain execution retains session/project reservations. Generation fencing
rejects stale events but does not stop bind-mount writes: physical death evidence
or storage isolation is mandatory before a replacement writer starts. No automatic
retry of started side-effecting work. Unknown resource consumers count against
capacity until reconciled. No provider switch after quota failure without existing
explicit fallback policy and effect reconciliation.

Record attempt/runtime IDs, queue reason/duration, process-tree peak usage,
startup/resume failures, OOM/throttle events and held claims without payloads or
credentials. UI shows queued/running/waiting-on-tool/draining/uncertain distinctly.
Persist environment manifest (image/CLI/skills/plugins/dependencies); make loss of
transient processes on eviction explicit. Native formats are version-qualified.

B-tests: B01 concurrent same-provider topics; B02 mixed providers; B03 session
exclusion and shared project lock; B04 legacy task chat exclusion; B05 fairness and
background cap; B06 startup reservation race; B07 missing telemetry/pressure;
B08 no busy TTL eviction; B09 denied role/credential/policy; B10 daemon outage and
unknown writer; B11 control-plane responsiveness at cap; B12 limits on child tools;
B13 profile/image change drains; B14 native continuation/file checksum after idle
eviction. Docker/native tests supplement Go tests; they do not use production jobs.

## B5. Provider-aware eligibility

[D: routing and failover](2026-11-01-session-routing-implementation.md) defines
provider/account bounds, blocked-head skipping across sessions, continuity and
fallback transitions. Claims are store transactions plus idempotent runtime
reconciliation, never an assumed atomic SQLite/Docker transaction. A pending
switch holds a session admission barrier without consuming a worker slot.
