# Foreground priority and quiet-hour delivery

Date: 2026-10-26. CP-006. Status: **proposed design**, not implemented or enabled.
Planning wave: 1 support; estimate: M after source review.

[Proposal](../../proposals/CP-006-yielding-maintenance-and-quiet-hours.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Implementation plan](../plans/2026-10-26-reliability-operations.md).

## Source baseline and outcome

Source reviewed at `33e026a`: `internal/runner/{runner,fresh,agentkill,gate,compact}.go`,
`internal/memory/{extract,memory}.go`, `internal/store/{store,task_requests}.go`,
`cmd/gopod/main.go`. RunFresh already has a one-minute budget; same-folder foreground
admission cancels background execution and waits for its cleanup. Canceled compaction
preserves context. These mechanisms remain the baseline, rather than a new scheduler.

Remaining defects: automatic compaction resets counters before success; its watcher advances
`compact_at` after errors; non-cancellation summary failure still clears context; the callback
ignores summary-storage errors. Summary replacement and extraction supersession are not one
transaction. A fresh summarizer currently receives no explicit bounded history snapshot.

The outcome is responsive foreground admission, resumable maintenance with truthful commits,
and bounded durable delay of proactive output. Existing personal-chat behavior stays default.

## Dependencies and ownership

Reuse CP-002's minimal additive ScopeRef and outbox foundation. CP-006 owns maintenance state
and quiet policy, not another delivery queue or namespace migration. CP-020 gates group
rollout; CP-019 governs private transient eligibility; CP-021 shares execution settlement.
Personal-only maintenance can ship before quiet delivery. Quiet delivery requires CP-002.

## Maintenance protocol

- Persist one coalesced request per scope and maintenance kind. Scope includes folder and
  thread; execution exclusion remains **folder-wide**, matching the shared container and
  existing lock. Interactive work in any thread cancels maintenance in that folder; another
  folder does not. Manual `/compact` remains an interactive operation.
- Proposed idle debounce is 60 seconds after accepted foreground intake. Only one maintenance
  run per folder is eligible, with the existing 60-second generation budget. Re-arm on an
  idle transition, source change or explicit retry, not an unbounded timer/model retry loop.
  Repeated arrivals coalesce; a continuously busy chat may legitimately defer maintenance.
- Durable states: `pending → running → committed`; interruption becomes `yielded`, ordinary
  error becomes `failed`, and missing process-exit proof becomes `execution_unknown`.
  Yielded work waits for idle; failed work permits one retry after the next source change
  or explicit command. Restart turns unresolved running work into reconciliation, never
  simultaneous duplicate execution. No additional periodic model calls are introduced.
- Before running, read an explicit bounded history/source snapshot, monotonic conversation
  revision and source high-water IDs. Accepted intake and context-changing operations advance
  the revision through Store helpers. Use the shared scope, never a provider-global history.
  The maintenance prompt includes the snapshot; “summarize our conversation” in a fresh
  native session is insufficient. Truncation and the covered source range are recorded.
- Perform generation and embeddings outside SQLite transactions. Commit only if the revision
  and requested source range still match. Insert replacement memory, FTS/vector effects,
  supersession and the idempotency/checkpoint receipt in **one Store-owned transaction**.
  Existing `MaxOpenConns(1)` means calling ordinary Store/Memory methods inside that transaction
  can deadlock: use transaction-bound helpers, following `task_requests.go`. No model,
  Docker or filesystem waits inside the transaction.
- Extraction commits a complete validated batch keyed by source turn and kind. Never retire
  an old fact before its replacement is durably inserted. Invalid/failed batches preserve
  prior facts and checkpoint. Replays return the receipt without new inference when committed.
- Compaction commits the summary and durable `rotation_pending` intent before changing native
  state. Retain exact old provider/session IDs; under the folder slot finish only that native
  cleanup, then record rotation completion and reset covered counters/last-success time.
  DB plus native files are not atomic: restart resumes cleanup from the intent, without
  regenerating the summary. New turns must see either the old mapping or the committed new
  mapping consistently. No global session wipe, “clear anyway”, or failed-summary replacement.
- Cancellation before commit changes no summary/checkpoint/counters. After commit it preserves
  the receipt and pending cleanup. A stale revision discards computed output and coalesces a
  fresh request. Legacy `compact_at` is only a scheduling baseline, not proof of success.
- Preserve lock order: queue admission, folder slot, global gate. Release generation resources
  before an idle wait. A kill-helper exit code alone does not prove every descendant stopped;
  unknown execution reserves the affected slot until CP-021-compatible reconciliation or
  verified owned-container stop. Report this state; never promise normal latency while unknown.

## Quiet policy and delivery lifecycle

Proposed operator policy has `enabled`, local start/end `HH:MM`, and explicit IANA timezone.
Default is disabled. Equal boundaries or invalid zones fail configuration validation. Start
is inclusive, end exclusive; overnight windows are valid. This is new configuration, not an
existing `GOPOD_TIMEZONE` contract. Per-chat windows are deferred.

The host derives delivery class from the accepted action: interactive result, proactive
notice, auth challenge or service error. Agents cannot mark arbitrary IPC as an interactive
reply. Direct user responses, current auth flows and immediate command errors remain exempt.
Scheduled output is proactive unless an explicit host-owned interactive eligibility applies;
this does not introduce scheduled stdout auto-delivery or supersede CP-015.

For proactive notices CP-002 stores original scope, source event/time, visibility, eligibility,
`not_before`, expiry and coalescing key. Proposed ordinary notice TTL is 24 hours, at most
20 unstarted pending notices per conversation, and flush rate one per second per conversation.
Coalesce same-key notices or expire the oldest unstarted eligible notice with a recorded
reason; never overwrite `sending`/uncertain effects. Expiry/lease release uses CP-002/003
rules and cannot garbage-collect an artifact referenced by uncertain delivery.

Recompute the allowed send time before each attempt using the configured location and fakeable
clock. Calendar boundaries must survive DST gaps/repeated hours; never implement “tomorrow”
by adding 24 hours. If ambiguous, delay until local time is outside the quiet interval.
A restart or timezone edit recalculates pending notices without extending their expiry.
No catch-up burst or silent rebinding to the current thread/provider is allowed.

Auth challenges and ephemeral/private routes retain their original host expiry and destination.
Never extend token validity or delay them beyond eligibility. Expire and ask for a new challenge
through the authorized private route when needed; do not fall back to a public chat. Ordinary
bot messages do not become ephemeral as part of this feature.

## Acceptance and rollout

Use synthetic providers, fake clocks and temporary stores. Prove same-folder yield, other-folder
independence, idle coalescing, restart/revision races, failed embeddings/storage, exact native
cleanup recovery and unchanged state after every pre-commit failure. Test counter/watcher success
semantics and all-fact transactional replacement. Extend existing `fresh_test.go`, memory
supersession tests and optional synthetic `docker_integration` cleanup tests.

Quiet tests cover disabled policy, midnight, both DST transitions, config changes, restart,
expiry while held, per-thread routing, bounded flush and uncertain effects. No real auth,
Telegram or provider quota probe is an acceptance dependency. Measure foreground wait through
cleanup; exclude separately measured cold-container/global-memory admission from the yield claim.

Migrate additively with features off. Preserve legacy trigger settings and personal defaults;
reconcile interrupted records before enabling a scope. Rollback disables scheduling/quiet
admission, retains receipts/intents/outbox rows and drains or explicitly reconciles effects.
Do not downgrade to a binary that ignores pending rotation intents. Group enablement waits for
CP-020; deployment and activation are separate from this documentation approval.
