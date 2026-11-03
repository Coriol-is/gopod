# CP-037 implementation contract D: mixed providers and controlled failover

2026-11-01. Implementation specification for review, not implemented or enabled.
Complements [A: state](2026-11-01-session-state-implementation.md),
[B: scheduling](2026-11-01-session-runtime-implementation.md) and
[C: restart](2026-11-01-session-restart-implementation.md).
This is the consolidated contract for the subsequent owner discussions. It refines
CP-018 routing/CP-009 continuity and retains explicit CP-010 fallback confirmation.
Background automatic fallback is excluded from the initial implementation.

## D1. Selection, account bounds and mixed execution

Resolve interactive provider: conversation override -> chat default -> system
default. Resolve model within that provider's namespace using explicit topic model,
per-provider chat default, then supported provider default. Never carry a model ID
from one provider into another or silently translate incompatible choices. Task
selection is explicit task policy or inheritance from its immutable source scope;
freeze actual provider/model at attempt admission, not schedule creation.

Add opaque host-owned credential-binding/account-budget identity and policy
revision to the attempt snapshot. No credentials in records or labels. Multiple
bindings of one account share its budget identity; aliasing credentials must not
multiply capacity. Bounds may be account-wide, model-specific or endpoint-specific
according to reliable provider evidence. Unknown rejection scope uses a conservative
binding/account bound, not a global outage claim. Different authorized accounts
are not an automatic quota bypass; account changes require explicit policy.

One host pool, no fixed Claude/Codex partition by default. Pick eligible session
heads under B2 fairness. Eligibility includes session/project claims, host resources,
provider/account cooldown, qualified authority/runtime profile and restart state.
A blocked provider head is skipped across other sessions, never within the same
session's FIFO. Quota/project waits consume no agent slot. No polling process or
warm-runtime affinity may reserve a runnable slot while waiting on a provider.
Pin resolved policy and target image digest before spawn; revalidate at dispatch
without silently replacing the snapshot. A stale binding returns to explicit
resolution or fails visibly. Running attempts never read mutable chat selection.

Illustrative fixed cap 3: topic A/Codex, topic B/Claude and one Claude task can
run simultaneously. A new A message waits on A; topic C waits on the shared cap.
Claude cooldown blocks eligible Claude starts only, allowing ready Codex sessions.
Per-provider health state is separate from scheduler fairness and host pressure.

## D2. Manual model/provider changes

Selection changes do not interrupt a running attempt. Record a pending selection
operation with expected conversation/session/epoch/settings revision; establish a
session admission barrier so queued turns cannot overtake the change. Existing
attempt finishes and captures its result, then commit settings under the barrier.
Pending input resolves the new choice afterwards. Show pending versus effective
selection separately. If preparation fails, preserve old effective selection and
report the pending operation failure; no silent execution on an unintended target.
Stop remains a separate explicit action.

Same-provider model change reuses native context only for a qualified pinned CLI/
model combination and supported context size. Smaller context windows, unsupported
resume or incompatible tool/history formats require a visible bounded handoff or
explicit fresh-context choice; never truncate native data silently or promise
lossless continuation. Auto-compaction, if supported, is a generation-checked
operation and cannot erase context on failure.

Cross-provider switch keeps the logical session/workspace and source native
context. Drain/recreate the compatible runtime; never run source and target
writers together. Resume the target provider's context for this session/epoch if
valid, otherwise create a fresh one. A -> B -> A must catch A up with committed
conversation events since its last successful consumption, not merely the last
provider name. Preserve CP-018 explicit chat-default and quota-confirmation scope.
Already running tasks retain their snapshots; switching a topic does not rewrite
other topics or a task definition's explicit provider.

## D3. Continuity envelope and progress cursor

Maintain monotonic committed conversation event sequence plus per-native-context
last-consumed sequence. Handoff is bounded, versioned and labeled historical data,
never higher-priority instructions. Include original request, partial reply,
verified completed steps, unresolved effects, result/artifact references and the
unconsumed conversation interval. Explicitly flag missing/truncated history and
stale file observations. Do not transfer private chain-of-thought, credentials or
raw native tool transcripts as an assumed interoperable format.

Scope/role checks apply to every included item and artifact. Working files are
shared within the logical session but must be reread after another provider ran.
Mark the consumed cursor only after native state/result commit evidence; failed
attempts cannot falsely advance it. A handoff envelope ID tied to attempt/interval
allows repeat detection after a crash; where native acceptance is unknown, hold
for reconciliation instead of reinjecting tools or assuming exactly-once ingestion.
`/clear` invalidates all cursors/contexts for the old epoch; `/new` changes session.
Task attempts stay CP-004 ephemeral; continuation uses explicit bounded checkpoint
input under a new attempt, not this interactive native-resume mechanism.

## D4. Failure classification and retries

| Evidence | Action |
|---|---|
| Confirmed rate limit | Respect retry-after/bounded cooldown; no busy loop |
| Confirmed quota exhaustion | Block affected account/model scope and offer wait/switch/cancel |
| Temporary service failure | Shared backoff/circuit state; offer explicit alternate provider |
| Authentication failure | Mark credential binding unavailable; offer login or explicit switch |
| Ambiguous timeout/disconnect after dispatch | Execution/effect reconciliation; not evidence of no work |
| Unsupported model/context format | Configuration/continuity error; do not label as account quota |

Persist reason, evidence class, cooldown deadline and revision; do not invent a
quota reset time. Circuit cooldown expiration permits a bounded single half-open
admission; it is not proof of health. No hidden billable health inference. Native
or HTTP retries are permitted only where known not-started or provider-idempotent;
outer orchestration does not blindly replay a partially executed native attempt.
Use bounded retry count/backoff and show wait status. An unclassified error never
triggers automatic provider/account/payment-route substitution.

## D5. Explicit fallback transition

Offer exact target provider/model/account payment route with Switch and continue,
Wait and Cancel controls. Confirmation binds actor, ScopeRef, session/epoch,
source turn/attempt, failed provider, target, selection/policy revision and expiry.
Check target model availability and permitted payment route; never auto-select a
paid API fallback. The confirmation authorizes one target attempt, not an unlimited
chain. Rejection by the target ends that attempt with visible next choices.

1. Freeze this session lane at the failed turn; preserve queued successor input.
2. Record fallback operation and cancellation/settlement requirements durably.
3. Confirm source process death and reconcile relevant external effects. If unknown,
   block; a quota error or closed network stream alone cannot release write claims.
4. Prepare target runtime/profile and authorized continuity envelope, reserving
   capacity only when eligible. Keep source context intact.
5. Commit topic override and a new target attempt binding in one store transaction
   using expected revisions. This commits the routing decision, not proof of Docker
   start. Reconcile a crash before/after spawn using durable attempt/runtime IDs;
   never spawn twice from repeated button presses.
6. Run target continuation before successor messages. The target is the topic's
   new selection after commit, even if its start later fails; show that explicitly.
   There is no automatic switch-back. Precommit failure preserves source selection.

The resumed logical turn has one result lineage; partial output from the source
and final target output must not create duplicate final sends. Delivery settlement
uses existing outbox identity; transport-unknown messages remain unknown. Before
any continuation, show that full native tool history cannot be transferred.

Wait keeps the failed turn ahead of successors with explicit waiting status and
Stop/cancel control. Cancel closes the failed turn without changing selection;
successor messages stay queued and may still be blocked by provider cooldown.
Switch/Wait/Cancel are mutually exclusive CAS transitions. Stop while fallback is
preparing cancels that operation and any reservation; after attempt commit it
cancels the current attempt. Late preparation completion cannot spawn after Stop.

## D6. Background tasks, control races and restart

Initial mode: no automatic task fallback. A blocked/not-started task follows its
existing expiry/misfire rules and exposes status; a started attempt follows the
existing failed/unknown settlement rules. Explicit continuation is a new attempt
of the same occurrence with checkpoint/effect reconciliation. Preserve delivery
owner, task role and grants; never elevate to interactive owner credentials or
change the source topic provider from a task failure. Any automatic fallback policy
requires a separate contract covering allowlists, cost, retries and side effects.

All selection/fallback operations compare session/epoch/settings and restart
admission generation. Clear/new/retirement invalidates stale offers and pending
operations; no fallback resurrects an archived session. A competing explicit
provider change makes an old quota button stale. Only one session control operation
may own the admission barrier. If a failed turn is waiting, a manual selection
change must offer either continuation of that turn (the D5 protocol) or closing
it before applying the choice; it must not wait on a turn that is itself waiting
for that choice. A newer control cannot silently steal a pending operation; return
its status or explicitly supersede it through the same revision-checked transition. A pending operation holds a lane
barrier, not a worker slot. Chat-default changes use revision validation for
inherited attempts; override-bearing topics remain unaffected.

Ordinary restart drains actual executing work; blocked provider/fallback waits and
pending selection operations are durable queue state, not busy executions, and
must not make drain wait forever. Stop ephemeral preparation and return it to
not-started durable state only when no exec/effects began. Confirmations during
drain may persist intent but cannot dispatch until reopen, when expiry and all
revisions are rechecked. Force cancels the active logical turn and its fallback
operations before process signals; these cannot reappear on recovery. Durable
provider cooldown/account bounds survive both restart modes.

## D7. Acceptance and implementation boundaries

D01 mixed-provider fairness/no blocked-provider global head-of-line stall.
D02 account aliases share bounds; account/model errors have correct scope.
D03 model change during stream does not interrupt; queued turns use committed choice.
D04 A -> B -> A catches up without cross-topic history or repeated final delivery.
D05 smaller context/unsupported native resume reports degradation and preserves data.
D06 rate/quota/auth/transport-unknown classification and half-open single admission.
D07 duplicate/stale quota buttons, Stop during target preparation, target failure.
D08 clear/new/provider-default/retirement races with switch and fallback.
D09 source death unknown or unresolved effect prevents unsafe continuation.
D10 crash at preparation, selection/attempt commit, native dispatch and result commit.
D11 task fallback preserves role/delivery and never changes interactive selection.
D12 graceful drain with blocked provider completes without starting queued work.
D13 force tombstones fallback so recovery cannot dispatch it.
D14 bounded handoff provenance/cursors, no secrets or unauthorized artifacts.
D15 target start fails after commit: selected provider remains visible; no silent rollback.
D16 Wait/Cancel/switch race: one winner, successor ordering and explicit status.

Source testing must include store/queue/runner/control/outbox fault injection and
race tests. Qualify actual native model switch/resume separately for each pinned
CLI; fake-provider tests do not establish native continuity or account policy.
