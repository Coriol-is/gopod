# Task outcomes, evidence and checkpoints

Date: 2026-10-26. CP-021. Status: **proposed design**, not implemented or enabled.
Planning wave: 1; size estimate: M.

[Proposal](../../proposals/CP-021-task-outcomes-checkpoints.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

Native process exit, model completion, goal achievement, verified external effects and Telegram
delivery are different outcomes; a single done flag obscures partial work and unsafe replay.

## Dependencies and boundaries

CP-004 session scope; CP-002 delivery states.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Store execution state independently of goal outcome achieved/partial/blocked/unknown and
  delivery state. Goal claims default to model_reported; the model cannot set host_verified or
  user_accepted. Host evidence is a separate typed receipt with its own
  checker/version/authority.
- Accept bounded structured outcome metadata through a provider-neutral side channel or final
  result adapter without replacing text streaming. Record checks/evidence
  references/limitations and a checkpoint: plan revision, last supported milestone, next safe
  step, unresolved effects. Do not invent progress not returned and committed.
- Bind every outcome/checkpoint to namespace/epoch, conversation, task and run. Commit final
  results, history and delivery references atomically with CP-002; a terminal model response
  cannot imply a confirmed Telegram send.
- Cancellation is requested before it is settled. Do not release the affected execution
  reservation or claim stopped while owned execution remains unknown; unrelated namespaces
  keep their independently safe admission limits. Unknown external effects require
  reconciliation and cannot be erased by clearing a status flag.
- Retain checkpoints across restart, but never blindly replay external actions. Default post-
  crash continuation is stopped pending reconciliation. Existing explicit owner authorization
  may cover bounded safe continuation only when scope/budget remain valid and no unresolved
  side effects require fresh consent; a model assertion or reused admission key is not
  authority.
- Status views show execution, goal authority, verification and delivery separately. This
  feature does not introduce a new general worker engine.

## Acceptance

- Exit 0 plus model achieved remains model_reported, not host verified or delivered.
- Partial/blocked/unknown persist across restart and are visible by exact scoped task ID.
- Cancel-requested execution with missing exit proof cannot be reported settled or used to
  blindly replay effects.
- Foreign namespace IDs, forged verification fields and oversized checkpoints fail closed;
  streaming remains usable on both providers.

## Migration, retention and rollback

Add nullable metadata; legacy results become unknown authority, never retroactively achieved.
Preserve old logs and pending uncertain effects. Back up before schema changes; old code must
not silently discard requested/unknown states.

## Risks and decisions before implementation

Overstating verification, unsafe replay and indefinitely reserved unknown work; reconciliation
needs explicit operator procedures.
Defaults/limits above are proposed product policy, not existing configuration.
The implementation contract below and linked executable plan define the initial schema,
APIs, defaults and slices. Deployment, native-account/resource acceptance and unresolved
privacy/effect evidence remain rollout gates; unknown execution fails closed.

## Out of scope

Automatic universal verification, automatic task replay, cancellation of already completed
remote actions or a new orchestration engine.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/outcomes.js; docs/task-outcomes.md; test/task-outcomes.test.js`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.

## Implementation contract — task reliability slice

This dated refinement is the concrete initial design, not evidence that code is
implemented. [Executable plan](../plans/2026-10-26-reliability-tasks.md).
It supersedes ambiguous early shorthand above. CP-002 owns the shared additive
`ScopeRef` foundation; this slice does not create a competing namespace registry.
`ScopeRef` carries `namespace_id`, `namespace_epoch`, `registration_revision`,
`chat_jid`, `thread_id`, `visibility` and `profile`, plus diagnostic folder data.
Use host `SnapshotScope`/`ValidateScope` at admission, dispatch and publication.
A shared owner DM or a model-supplied folder is not a replacement for that scope.

### Additive run/attempt ledger

Keep existing `turns` enum values (`pending`, `running`, `done`, `failed`,
`stopped`, `interrupted`) for interactive compatibility. Do not globally rename
or reinterpret them as goal outcomes. Add authoritative task ledger tables:

| Record | Required fields / constraints |
| --- | --- |
| `task_runs` | `run_id` UUID; task ID; definition revision; nominal UTC milliseconds; ScopeRef; trigger kind; delivery mode; execution state; publication state; nullable frozen `result_id`; goal/outcome/checkpoint JSON; creation/settlement times |
| `task_attempts` | `attempt_id` UUID; run FK; monotonic attempt number; unique durable turn FK; execution state; concrete provider/model/profile fingerprint; native session UUID if observed; start/exit evidence; `reservation_state`; bounded fixed failure code |
| `task_verifications` | host-generated receipt ID; run/attempt/result reference; checker name/version; checked input/artifact digest; narrow check outcome; time; no model-writable verification authority |

A logical occurrence has one run, while an admitted attempt has one own durable
turn. `task_run_logs` remains a compatibility/diagnostic view, not authority for
goal or delivery. Map managed completion from this ledger, not solely the old
`OnDone(Item, error)` callback: today `ErrStopped` is passed to hooks as nil and
would otherwise be mislabeled success.

Run execution states are `queued`, `running`, `cancel_requested`, `completed`,
`failed`, `cancelled`, `interrupted`, `execution_unknown`. Publication is separate:
`not_requested`, `pending`, `published`, `failed`; delivery is read from CP-002's
outbox. A completed native process can have partial/unknown goal and failed or
uncertain delivery. Task schedule state active/paused/done is another axis.

`reservation_state=held` accounts for a started attempt until owned local exit is
observed; `released` requires trusted settlement or explicit operator proof.
Reconstruct held reservations after restart before any task or interactive work
is admitted into the affected namespace. They consume the existing global budget;
other namespaces may use genuinely free capacity, not invented freed slots.
Known local exit can release execution capacity even if remote effects remain
uncertain, but those effects still forbid replay. Do not claim full detached-tree
or remote-process termination from closing a Docker attachment.

### Model report side channel

Introduce versioned `task-results` IPC with one request <=32 KiB:

```json
{"version":1,"runId":"<uuid>","attemptId":"<uuid>","runToken":"<opaque>",
 "outcome":{"status":"partial","checks":[],"evidence":[],"limitations":[]},
 "checkpoint":null}
```

The watcher accepts it only for a live host-minted task-report grant bound to the
exact ScopeRef/run/attempt and source IPC namespace. It cannot target another task
by changing JSON identifiers; ending/canceling the attempt revokes the grant.
Reject unknown fields, trailing JSON, wrong types and stale/foreign grants. Each
list has at most 8 strings, each <=1024 UTF-8 bytes; checkpoint text fields are
<=2048 bytes, `planVersion` is 1..1,000,000 and the entire request is bounded.
Checkpoint keys are `planVersion`, `lastSupportedMilestone`, `nextSafeStep` and
`unresolvedEffects`. Outcome and checkpoint may be null. Evidence strings are
untrusted claims and are never automatically fetched or opened as host paths.

Store valid reports as provisional metadata. A bounded latest revision can be
returned to the same actor, but it never sets execution completion, delivered,
`hostVerified` or owner acceptance. Unknown/malformed optional reports do not
justify rerunning already executed work: record `invalid_report`, retain the
frozen result and show unknown goal. Source stdout is not parsed opportunistically
as this JSON; both providers keep normal text streaming.

The host verification API is separate and not registered as an agent tool.
Receipts name what was actually checked (for example artifact integrity), not a
global claim that the goal is true. Model `achieved` remains model-reported even
when an unrelated upload/hash check passed. Explicit owner acceptance may be
recorded from authenticated exact task/run intent, never inferred from silence,
a forward, a generic thanks or another model's review.

### Freeze, publish and recovery

Reuse CP-002 `turn_results` and its result membership references; do not store a
second divergent final-text blob. Result freezing is durable preparation. Then
`FinalizeTaskRun` atomically publishes its result/outcome/checkpoint references
and, **only for controller_result**, the final outbox publication intent using
the shared store transaction helpers. Legacy IPC produces no controller send.
Execution/exit state can settle separately from response preparation; failed
publication remains visible without changing known execution into a retryable
provider failure. A prepared orphan result has no delivery authority.

Queue dispatch must distinguish managed task control from interactive retry.
Once an attempt may have started, neither exponential handler retry nor
`Queue.Recover` may invoke the model again for that occurrence. A frozen result
recovers publication/delivery only. A prepared-but-provably-unstarted attempt
may receive bounded pre-exec retries only when the host boundary reports
`not_started`; provider prose/error class is not that proof. Admission/scope
rejection is not retried by pretending it was transient execution.

A running attempt found after crash becomes execution_unknown, not resumed.
Cancellation first persists `cancel_requested` and revokes report/tool grants;
request owned-process termination, await exit evidence, then settle cancelled.
If exit cannot be established, keep execution_unknown/reservation held. Preserve
checkpoint/effect evidence; an operator must establish stopped runtime and
reconcile effects before releasing the reservation. A status reset or restoring
an empty in-memory map is not that procedure. No automatic retry/resume command
is introduced by this slice.

### Compatibility and acceptance

Activation requires a dry-run inventory of pending/running legacy task turns.
Keep definitions, schedule times and delivery owners unchanged. Never fabricate
historical achieved/verified/delivered values. Legacy task turns that were already
started at the upgrade cut require an approved hold/reconciliation step before
task recovery is enabled; retain their old records and unknown authority. Pending
never-started rows can be adopted only with unambiguous ScopeRef/run linkage.
Interactive recovery semantics are not silently changed by this task-only slice.

Test failpoints before spawn, after exec request, after exit but before freeze,
after freeze but before publication, and after a Telegram send without ack.
Assert no model replay for any started/unknown/frozen task; invalid reports cannot
mint success. Cancel-requested/unknown work retains the affected reservation,
while another namespace uses available capacity. Matched backup/restore and
binary downgrade must not reactivate quarantined started work.
