# CP-002 durable Stop intent and late-result settlement

Status: accepted by owner 2026-11-05; D066. Implementation in progress.
Baseline: `85640de`, with #174 process-local handoff and #175 stopped-publication fence.
Acceptance does not imply implementation, activation or deployment.

## Accepted decision

Use a separate immutable cancellation-intent ledger, keyed by exact turn attempt
and bound to the complete admitted batch and conversation scope. Record it before
reporting Stop accepted or cancelling the native context. Keep execution outcome,
frozen result and delivery outcome separate from cancellation intent.

This extends D063's activation gates with concrete persistent Stop semantics.
It changes the meaning of successful Stop and adds durable evidence that survives
queue status rewrites; it is not just a new mutable `turns.status` value. On approval,
append the next available ADR without editing D063/D065, then implement and qualify.

## Current evidence

- `internal/telegram/stop.go`: `inflightTurn.stop` closes an in-memory channel,
  cancels the native context, and returns bool. There is no persistence step.
- `internal/telegram/handler.go`: `NewAgentHandler` receives all admitted items,
  but passes `pickBatchItem(items)` to the sender; binding only its ID loses the
  earlier coalesced members. Queue start supplies committed attempt counters.
- `internal/telegram/final_handoff.go`: exact in-process generation ownership is
  already bound across sync/stream/fallback, serializing successful final vs Stop.
- `internal/runner/result_freeze.go`: `completeResult` deliberately persists after
  cancellation using a bounded context without cancellation. Native completion
  plus a failed freeze is NoReplay; losing that evidence must never enable retry.
- `internal/store/result_publication.go`: new interactive publication checks
  current member statuses, but a status rewrite can erase this cancellation signal.
- `internal/store/turn_batch.go` fences frozen/terminal work, and ordinary boot
  recovery refuses replay of dispatched unknown work. Neither represents Stop intent.
- Existing `RecordForcedTurns` is tied to a restart request and is not a suitable
  ordinary Stop ledger. Managed task cancellation keeps its separate policy.

## Why this option

| Option | Benefit | Limitation |
|---|---|---|
| Reuse stopped/failed turn status | No schema change | Conflates request with native exit; subsequent writes erase intent; cannot prove exact batch/attempt identity |
| Add a mutable stop flag | Small schema change | Still needs immutable identity, retry/conflict and batch semantics; easy to clear during recovery |
| Immutable intent plus separate outcome evidence (recommended) | Stop survives status changes and late results; batch identity is reviewable | Additive schema, migration/backup qualification and explicit error handling |

## Persistence and authority

The host captures the complete committed batch before model invocation: namespace,
epoch, conversation/thread, visibility and every `(turn_id, attempt)` pair. Clone
membership, bind it to the exact registered generation, and carry it through all
delegation. Never reconstruct membership from the latest turn or model output.

A Store transaction validates the exact current ordinary attempts, scope and
absence of managed members, writes one immutable intent plus all members, and
returns its identity. Exact retries reuse it; partial/different membership is a
conflict. Constraints prevent one attempt belonging to conflicting intents.
Use a bounded persistence context independent of native cancellation. Do not
persist credentials, prompt text or arbitrary provider errors in the intent.

New publication checks the ledger in the same transaction as publication. Any
cancelled member holds the whole result. Existing delivery ownership is returned
unchanged; this operation cannot retract sending/sent/uncertain HTTP effects.
The reciprocal check is mandatory: intent creation transactionally rejects any
member whose frozen result already has a publication owner. Publication checks
intents, and intent creation checks publications, in the same Store serialization
domain. Local `beginFinal` alone does not fence boot/direct Store callers.
Final-owner/publication victory rejects a late ordinary Stop. Stop victory fences
publication and native replay. The production handoff and persistent transaction
must enforce one winner rather than relying on check-then-act reads.

## Stop API and failure semantics

Replace bool-only control results with explicit accepted/already-stopped/finished/
failed-or-unknown outcomes while retaining Router authorization. Apply the same
protocol to slash command, inline button and native draft-stop events.

Serialize Stop admission against final ownership for the exact generation. Mark
Stop persistence pending so a finalizer cannot run past the write; avoid holding
map/queue locks across SQLite or callbacks. Duplicate requests join/reuse this
operation rather than retargeting a replacement generation.

On confirmed commit, close the generation Stop channel and cancel native work,
then report acceptance. Acceptance means cancellation requested durably, not exit
or remote non-delivery. On failed/ambiguous commit, do not claim acceptance or
resume normal final output. Reconcile by exact immutable identity. A confirmed
absent intent after a definitive rollback may restore generation eligibility;
unknown/conflicting evidence stays held and NoReplay in the live process.

Crash boundary: an unsuccessful Stop with no committed intent is NOT a durable
cancellation. If authoritative reopen finds no intent, normal existing recovery
rules apply, including ready-plan publication. The UI must not have reported Stop
accepted. If the intent exists, all cancellation fences apply. This first contract
does not add another durable pre-admission journal and cannot promise cancellation
survives a crash after a write that never committed. Definitive rollback restoration
must not be confused with an inconclusive read after an ambiguous commit.

## Late result and recovery

Preserve the native result even if Stop already won. Freezing its bytes and
continuity remains idempotent under the existing result contract. Record/link
late-result settlement to the cancellation intent; never publish it automatically.
Do not repeat model/TTS work to reconstruct a missing result.

Recommended user-visible boundary: after accepted Stop, permit only a fixed Stop
acknowledgement and retiring existing preview controls. Do not emit new accumulated
answer text, extract/upload code, synthesize voice or set a final-answer memo from
the stopped path. Already delivered previews are not retracted and do not become
final delivery proof. This deliberately changes current `finishStopped` /
`finishStoppedEdit`, which can send accumulated text (possibly the full result)
and upload code after Stop. Preserving that old partial-output behavior would
require a separately specified immutable pre-Stop snapshot and delivery authority;
it is not included in this recommendation.

Execution completion and cancellation intent are separate. A result or a handler
return is not proof every native process/transport exited. Keep explicit unknown
exit/freeze failures held; retain the original NoReplay cause. A resultless crash
must not become a successful cancellation merely because an intent exists.

Boot reconciles intent/result/publication before dispatch. StartBatch refuses any
attempt with cancellation evidence even after mutable status rewrites. A stopped
coalesced result is held in full; unaffected conversations continue. Published
parts retain existing outbox reconciliation. Unresolved late native effects keep
existing runtime/topic quiescence barriers closed.

## Delivery plan

1. Accepted ADR and additive schema/API: immutable identity/membership,
   atomic/idempotent request, conflicts, publication/start/recovery fences.
2. Bind the complete batch to Telegram generation; use typed Stop outcomes and
   persist before cancel across all three entry points. Handle DB failure and
   ambiguous commits with explicit holds and exact-generation reconciliation.
3. Qualify late-result/NoReplay settlement and restart behavior, including the
   existing synchronous and streaming paths. Keep durable foreground producer
   disconnected until all other D063/D065 gates pass.
4. Independent review, full Go checks, relevant races, required CI, merge and
   documentation. Deployment remains separately authorized.

## Acceptance gates

- Complete coalesced membership; wrong scope/attempt, stale registration and
  managed-member rejection; mixed batches roll back atomically.
- Stop commit precedes native cancel and accepted UI; duplicate Stop is idempotent.
- Both Store race orderings of Stop versus publication (including direct/recovery
  callers), plus local final ownership; stale callbacks
  and sync/stream/fallback delegation cannot affect the successor.
- Definitive DB failure and ambiguous commit: no false acceptance, no normal
  final output or replay while live evidence is unknown. Crash with authoritative
  absence of intent follows ordinary recovery, never claims accepted cancellation.
- Stop paths send only acknowledgement/control retirement, with no new answer/code/
  voice delivery or final memo; already visible previews remain non-final evidence.
- Late successful result, freeze failure/unknown, native NoReplay and shutdown:
  preserve evidence, no second Result read, no model/TTS replay, no false exit proof.
- Close/reopen at every boundary; status rewrites cannot erase intent; unaffected
  conversations progress; sending/uncertain/acknowledged deliveries remain unchanged.
- Populated old-schema migration, backup table counts and verification, FK and
  immutability constraints; preserve rollback evidence. Older binaries must not
  dispatch unresolved intent-bound work because they cannot honor this ledger.
- Full build/vet/test and relevant race suites; required test/Docker CI green.

## Excluded from this decision

No task-credential activation, parallel execution, new provider, topic deletion,
backup scheduling, foreground freeze activation, paid/live provider experiment or
production rollout. Preview/reply-action parity remains separately qualified.

## Design review

Independent read-only review identified three ambiguities, now made explicit:
no-commit crash recovery follows ordinary rules; accepted Stop uses acknowledgement
only rather than the legacy partial-answer upload path; both Store transaction
orderings enforce intent versus publication ownership. This is design review,
not implementation or runtime acceptance.
