# Reliability specification wave: integration and execution order

Date: 2026-10-26. Scope: CP-002/003/004/006/007/021/022.
Status: source-backed specifications and implementation plans; **no runtime
implementation or rollout is claimed or authorized by merging this document**.
The owner selected this documentation wave. Other adoption proposals remain
separate, and existing CP lifecycle states are not promoted automatically.

## Ownership and canonical documents

| Cluster | Canonical specs | Implementation plan |
| --- | --- | --- |
| Delivery | [CP-002](../specs/2026-10-26-telegram-outbox.md), [CP-003](../specs/2026-10-26-immutable-artifact-delivery.md) | [Delivery plan](2026-10-26-reliability-delivery.md) |
| Task execution | [CP-004](../specs/2026-10-26-isolated-task-sessions.md), [CP-021](../specs/2026-10-26-task-outcomes-checkpoints.md), [CP-022](../specs/2026-10-26-schedule-execution-policies.md) | [Tasks plan](2026-10-26-reliability-tasks.md) |
| Operations | [CP-006](../specs/2026-10-26-maintenance-priority-quiet-hours.md), [CP-007](../specs/2026-10-26-matched-store-backups.md) | [Operations plan](2026-10-26-reliability-operations.md) |

These plans refine the [adoption map](../../proposals/2026-10-26-codex-agent-adoption.md),
not a second independent feature design. Their executable slices specify source
files, migrations, failure tests and rollout gates. Source baseline is gopod
33e026a; concurrent documentation merges do not change that runtime baseline.

## Cross-cluster contracts

1. **One host scope identity.** CP-002's initial store foundation owns the minimal
   additive namespace UUID/epoch/registration-revision metadata on registrations.
   Tasks, outcomes, publications and backups reuse it; CP-020 later consumes the
   same identity rather than inventing another registry. Folder names are not
   authorization. Thread 0 is a literal conversation, never a scope wildcard.
   Metadata alone does not qualify CP-020 filesystem/account isolation.
2. **Two snapshot boundaries.** Freeze source intent, route and authority at
   occurrence creation. Resolve the effective provider/model/policy at actual
   execution admission under the chat slot, consistent with CP-018. An admitted
   attempt keeps its own snapshot; a later preference change is not a new source
   or permission grant. Scope revision is revalidated at consequential boundaries.
3. **Task identity is not a Telegram thread.** Task/run/attempt keys stay separate
   from conversation IDs. Managed tasks cannot batch with interactive messages or
   other runs. They must not enter foreground native-session adoption or its
   maintenance-only one-minute execution path.
4. **One delivery owner.** A run records legacy agent IPC or explicitly opted-in
   controller result publication. Scheduled stdout does not create a new send.
   Existing IPC-delivered jobs are not silently migrated, duplicated or rewritten.
   Current tasks remain non-owner service work; folder `owner` does not mint
   owner/admin capabilities.
5. **Truth has separate dimensions.** Execution exit, model goal claim, host
   check, external effect, local queue commit, Telegram acknowledgement and owner
   acceptance are distinct. Model metadata cannot manufacture host verification.
   Requested cancellation is not settled execution; unknown effects do not
   authorize retry, provider fallback or post-crash replay.
6. **One store transaction context.** Store owns SQL and transaction-aware helpers.
   Current Store has one connection: calling a plain DB-backed helper from inside
   another open transaction can deadlock. Scope/run/outbox writes must participate
   in the same transaction; no network, embedding, native execution or waiting
   for chat slots takes place while a store transaction is held.
7. **Prepared files are not published files.** Artifact preparation/fsync occurs
   before the bounded publication transaction. DB/filesystem are not one atomic
   resource. Preserve/reconcile orphan preparations and verify exact bytes and
   route before upload. Matched backup/restore includes referenced snapshots.
8. **Retry requires nonacceptance evidence.** Explicit transport/API rejection
   and ambiguous post-dispatch errors are not interchangeable. A retry budget
   never makes a possibly accepted send safe. Multipart final delivery records
   each part/attempt so replay cannot duplicate an acknowledged prefix.
9. **Background work yields safely.** Maintenance computes from a revision-bound
   snapshot, then atomically commits a valid replacement before native rotation.
   Failed/cancelled work cannot erase source context. Managed maintenance uses a
   verified data-only native policy with no tools, inherited plugins/configuration
   or implicit profile/history discovery; only bounded host snapshots are input.
   Personal-context canary fixtures qualify that boundary before enablement. Quiet-hour policy is checked
   by the durable delivery path; private eligibility/expiry never turns into a
   main-chat/public fallback.
10. **Recovery starts quarantined.** Full restore validation precedes polling,
    scheduler, IPC watchers, auth/model warmup and delivery. A live DB-only backup
    is useful but not a matched filesystem/auth disaster-recovery guarantee.
    An idle/stopped full cut needs actual owned-writer settlement, not an empty
    in-memory map or a single SQLite connection. Unknown external writers/effects
    remain an explicit operator reconciliation gate.

## Proposed implementation ordering (not a code-execution approval)

- **R0 — safe recovery foundation:** define startup quarantine, capture manifest
  and isolated restore checker. Establish the CP-007 baseline/fence before a
  production schema/filesystem rollout. A scheduled timer stays disabled until
  an idle safe cut is qualified; never force-stop active owner work to get a backup.
- **R1 — shared identity and durable text/IPC delivery:** add CP-002 identity,
  outbox/attempt state and dispatcher with conservative ambiguity handling.
  Personal-only controlled admission can precede full CP-020. Group managed
  capabilities remain disabled pending CP-020; legacy group behavior is not
  relabeled isolated by these changes.
- **R2 — artifacts and foreground parity:** implement CP-003 preparation,
  inventory, quotas/GC and export verification. Integrate final streaming,
  existing long-code attachments and voice before claiming the entire foreground
  delivery path migrated. Keep unmigrated paths explicitly legacy, not broken.
  CP-007 artifact manifests and matched restore qualification are a joint activation
  gate for this slice; implementation order never waives that release gate.
- **R3 — managed task/session ledger:** implement CP-004 fresh task execution,
  singleton queue claims and CP-021 run/attempt/outcome authority. Disable unsafe
  started-task retry/recovery in the managed path before enabling its schedules.
  Preview legacy task migration; no automatic digest recreation or session import.
- **R4 — scheduler policies:** CP-022 occurrence admission/advancement, bounded
  overlap/misfire and model-free reminder outbox admission. Integration tests use
  the shared scope/run/publication contracts rather than another scheduling store.
- **R5 — maintenance commits and quiet hours:** CP-006 source-revision and commit
  safeguards are correctness gates before re-arm/quiet-delivery automation. The
  optional CP-007 scheduled idle cut remains off until independently qualified.

Independent slices may proceed in parallel only when their shared store/schema
contract is agreed. The three cluster plans define the atomic PR boundaries;
this ordering is their integration dependency map, not permission to skip their
migration, privacy or acceptance gates.

## Acceptance of the document wave

The owner explicitly waived CI waiting for **Markdown-only** task acceptance.
For each worktree: verify every changed path is Markdown, inspect the full diff,
validate local links and canonical CP/spec/plan pointers, review source claims and
cross-cluster dependencies independently, then merge that document PR on readiness.
Do not change CI configuration or manufacture passing check results. Automatic
checks may run normally, but they are not a gate for this document-only exception.
Any source/config/runtime change falls outside the exception and requires its
normal implementation review/test/CI and deployment procedure.

This specification task performs no DB mutation, credential inspection, real
model call, Telegram send, scheduled-job edit, backup/restore, host security change
or production feature activation. Those belong to the separately approved code
and acceptance phases.
