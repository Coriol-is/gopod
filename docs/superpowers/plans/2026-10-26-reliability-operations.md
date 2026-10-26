# CP-006 / CP-007 implementation plan

Date: 2026-10-26. Status: **proposed; documentation only; execution not authorized here**.
Source reviewed: `33e026a`. Each numbered slice is an independently reviewed atomic PR,
implemented in a separate worktree with synthetic tests. Recheck source names before execution.

Specifications: [maintenance](../specs/2026-10-26-maintenance-priority-quiet-hours.md),
[backups](../specs/2026-10-26-matched-store-backups.md),
[shared foundation](../specs/2026-10-26-codex-agent-adoption-common-contract.md).

## Order and integration contracts

CP-006 maintenance slices 1–2 and CP-007 DB primitive slice 4 can proceed independently.
Slice 3 consumes CP-002's outbox/ScopeRef; it must not build a parallel sender. Slices 5–7
form the first full backup feature and remain disabled until jointly qualified. CP-003 artifact
extension slice 8 gates artifact rollout but does not block artifact-free backup baseline.
CP-020 gates group enablement. CP-021 supplies compatible execution-unknown settlement; no
maintenance or backup change may treat a helper exit as universal process termination proof.

Use one Store transaction owner and transaction-bound helpers, following
`internal/store/task_requests.go`. The single pooled connection in `store.go` makes nested
ordinary Store calls inside a transaction unsafe. No provider request, embedding, file-copy,
Docker call or waiting for locks inside SQLite transactions. Scope foundation is CP-002-owned
minimal additive metadata, reused here without a broad historical-data rewrite.

## 1. Durable maintenance requests and revision fences

Files: new `internal/runner/maintenance.go`, `internal/store/maintenance.go` and tests;
`internal/store/schema.go`, `internal/runner/{runner,compact,agentkill}.go`, accepted-intake
hooks in `internal/queue/queue.go` and existing routing/history writers. Enumerate every
context-mutating path (clear, reset, history append, correction, accepted intake) before coding.

- First write tests proving revision mismatch prevents commit and requests coalesce per
  scope/kind. Add additive maintenance/revision/receipt fields or tables with stable keys,
  source high-water, state, attempt and bounded failure category. Use the shared ScopeRef.
- Atomically advance revision at accepted intake. Persist intent before starting inference;
  folder-wide cancellation/exclusion matches current runner locks. Keep queue → folder →
  gate order. Introduce 60s idle debounce and existing 60s generation deadline.
- Recover unresolved running state into reconciliation; no blind replay. Yielded re-arms
  after idle; failure retries only after a new source revision or explicit command.
- Do not reset turn counters or watcher last-success on admission/failure. Preserve existing
  compact count/interval/daily trigger values. Legacy compact_at initializes scheduling only.

Tests: same-folder cross-thread cancellation, other-folder independence, rapid intake,
restart, repeated idle tick, failure retry bounds, revision change after generation, unknown
execution retains slot. Extend `internal/runner/fresh_test.go` rather than duplicate it.
Migration: features off, additive records, no inferred successful historical receipts.
Rollback: disable new maintenance admission; retain records and reconcile running work.

## 2. Atomic memory application and recoverable compaction rotation

Files: `internal/memory/{memory,extract}.go`, new transaction helpers in `internal/store`,
`cmd/gopod/main.go`, `internal/runner/{runner,compact}.go`, provider command/policy
adapters `internal/runner/{provider,provider_claude,provider_codex}.go`; memory/runner tests.

- First add a maintenance-only generation adapter (proposed `RunMaintenanceCmd`) with
  immutable host policy and zero execution/filesystem/web/connector/service tools. Do not
  reuse powerful RunFresh flags, inherit native plugins/MCP/mutable settings or change
  foreground personal defaults/subscriptions. Supply only bounded host snapshot text.
  Prompt-only read-only instructions do not qualify as enforcement. Verify effective command,
  configuration and tool inventory for both pinned providers on fresh/new-session paths;
  unsupported/unknown enforcement leaves managed maintenance staged/off.
- Capture bounded scoped conversation history with source range/revision, then feed that
  explicit history to fresh summary generation. Compute embeddings outside the transaction.
- Replace ignored summary insert errors with propagated failures. Introduce a Store-owned
  atomic batch for replacement inserts, supersession, vector/FTS changes and source receipt.
  Extraction must never supersede before a replacement can commit. Reject stale revisions.
- Commit summary plus rotation_pending containing exact old mapping/session IDs; complete
  exact native cleanup under folder slot, then mark rotation done and update covered counters
  and watcher success. Failures preserve the previous valid context or committed rotation
  intent. Remove non-cancellation “clear anyway” and destructive no-callback fallback from
  maintenance. Keep explicit user `/clear` semantics separately authorized.
- On crash after summary commit, resume exact cleanup without inference. If process ownership
  is uncertain, expose the blocked state and require verified settlement before slot reuse.

Tests: provider command/config fixtures prove zero effective maintenance tools on both
fresh/new-session paths; malicious source cannot invoke OS/service tools, read extra workspace
files or mutate files. Do not make real model/account calls for these docs or policy fixtures.
Embedding/insert/vector/FTS/supersession failures roll back whole batch; deduplicated
source replay; cancellation before/after commit; new intake between snapshot and commit;
crash during each rotation phase; no unrelated provider/thread deletion. Optional existing
synthetic docker_integration cleanup test uses no auth or model. Review full transaction call
paths for single-connection deadlocks, with bounded test deadlines.
Rollback: no older binary against pending rotation intents; settle or retain feature-off state.

## 3. Quiet policy as an outbox adapter

Files: new `internal/delivery/quiet.go` and fake-clock tests (align actual package to CP-002),
`internal/config/config.go`, host IPC/scheduler/control admission adapters, operator docs.

- Add disabled-by-default validated policy: explicit IANA location and unequal HH:MM boundaries.
  Host classifies interactive/proactive/auth/error and persists source visibility/expiry.
- Set CP-002 not_before for proactive work; recheck eligibility and quiet interval at every
  send. Preserve original source/scope/time/provider binding. No new stdout delivery behavior.
- Apply proposed 24h ordinary notice TTL, 20 unstarted pending notices/conversation and
  one send/sec/conversation. Coalesce only compatible unstarted items; expiry releases
  references through CP-003, never uncertain effects. Auth eligibility is never extended;
  private failure has no public fallback.

Tests: disabled default, midnight, DST gap/fold, timezone change/restart, held expiry,
private challenge expiry, original thread, bounded queue/flush, agent cannot spoof class.
Migration: pending legacy items remain existing policy until explicitly classified; do not
invent private eligibility. Rollback: stop new delays and retain/reconcile held rows, rather
than flushing them all. Activation requires CP-002 acceptance and CP-019 route constraints;
group rollout waits CP-020.

## 4. Store-owned database snapshot primitive

Files: new `internal/store/backup.go`, `backup_test.go`; minimal private helper CLI entry
under `cmd/gopod-backup/` with no normal app startup; no timer yet.

- Verify pinned ncruces v0.20.0 API; use dedicated connection Raw/backup adapter with bounded
  incremental steps, context checks and close-on-all-paths. Test busy/cancel/error behavior.
- Distinguish database_only output in manifest/status. Destination root validation, 0700/0600
  permissions and private staging are required even for DB-only output.
- Offline helper opens without Store.Open migrations. Record application schema fingerprint
  or introduce additive application migration ledger; SQLite schema_version is insufficient.

Tests: temporary WAL database with concurrent commits produces an internally consistent cut;
cancel, disk full, existing destination, symlink escape, permission checks. DB counts and
foreign-key integrity are local evidence only. No credentials, actual backup or live service.
Rollback: remove optional helper/admission, keep generated tool-owned snapshots untouched.

## 5. Host fence and stopped-writer matched capture

Files: new `internal/maintenance/` host-only lease/fence package; integrate
`cmd/gopod/{main,startup,shutdown}.go`, runner lifecycle/admission, scheduler, IPC,
memory/auth/config writers; new host supervisor in `cmd/gopod-backup/` and deployment docs.

- Inventory all writers before enabling: raw DB mounts, provider containers, auth helpers,
  IPC producers, management starts, workspace tools and external host mutations. Unknown or
  unsupported writers fail/defer capture. Do not broaden agent mount permissions for backup.
- Implement a private Unix socket authenticated by peer UID, outside agent mounts. Atomic
  fence+idle check blocks new starts/writes. Defer on active work or unsettled send; acquisition
  30s, capture 120s proposed. Host supervisor owns lease while controller is stopped.
- Stop exact owned writer container IDs and controller, verify stopped state. Controller stop
  alone is insufficient because lifecycle cleanup preserves same-version live containers.
- Capture DB via slice 4 plus all required root inventories. No-follow copying, protected
  staging, pre/post inventory checks, fsync and completed-marker atomic publication. Record
  actual immutable image IDs/source/config/schema and scope foundation metadata.
- Failure never publishes success; restore prior verified running state only after lease and
  ownership reconciliation. Do not automatically replace live data or delete partial evidence.

Tests: atomic idle/admission race, busy defer, socket spoofing/permissions, controller crash,
unknown descendant/container, capture timeout, external writer detection, filesystem/root
escape/hardlink handling and interrupted publish. No production container operations in tests.
Gate: full writer inventory, operator-approved downtime window and target Pi resource evidence.

## 6. Offline restore validation and startup quarantine

Files: `cmd/gopod-backup/` restore subcommands, new `internal/backup/manifest.go` and validator,
startup gate before normal Store.Open/network/StartupCheck/recovery in `cmd/gopod`; tests/docs.

- Validate manifest, compatibility, DB integrity and inventory in a fresh scratch destination
  using read-only SQLite. Require a protected quarantine latch before copying runtime roots.
- Fail closed before migrations, poller, API listeners, auth warmup, runner cleanup/start,
  scheduler, IPC and queue Recover. All copied pending/uncertain effects and turns stay held.
- Introduce restore epoch invalidation for transient approvals. Provide an explicit host-side
  reconciliation report/activation operation; validation alone never activates the instance.
- Test valid synthetic credentials cannot cause network/account use. Activation checks exact
  configured instance ownership and no concurrent poller; this is a separate operator action.

Tests: missing/hash-mismatched file, foreign-key/vector/schema mismatch, corrupt marker,
unsupported future format, copied pending IPC/tasks/turns, latch bypass attempts, empty and
mixed-scope DB, no-network startup assertions. Restore never overwrites live roots by default.
Rollback: remain quarantined, retain both cuts and effect evidence; no automatic downgrade.

## 7. Opt-in timer, bounded retention and recovery runbook

Files: new `deploy/` host service/timer templates and scripts invoking the helper, operator
README/runbook, content-free status endpoint integration. Compose healthcheck is separate.

- Timer off by default. Explicit zone; proposed 03:00 daily, no catch-up, one capture slot,
  busy defer. Require selected quota/root/credential-storage policy and qualified offline
  duration. Timer cannot infer permission for remote uploads or forced user interruption.
- Retain 14 daily/8 weekly completed tool-owned captures, preserving last restore-verified
  point and all retained-manifest bytes. Never prune manual snapshots. Quota exhaustion fails
  safely; incomplete staging cleanup is bounded, tool-owned and separately observable.
- Record mode/age/verification/failure category. Optional CP-002 notices contain no secrets.
  Runbook distinguishes DB snapshot, matched capture, validation and production activation.

Tests: fake-clock retention/timezone, verified-point preservation, manual/incomplete exclusion,
quota/overlapping timer, service restart and failed lease release. Qualification uses synthetic
scratch data first; live drill/deployment remain separately gated, never part of docs acceptance.
Rollback: disable timer, reconcile lease, preserve verified backups and manifests.

## 8. Artifact extension at CP-003 rollout

Files: manifest/validator + CP-003 artifact Store/GC lease helpers, migration tests and runbook.

Add artifact IDs/digests and immutable bytes to matched inventory. Capture leases pin pending,
sending and uncertain references during copy; completed manifests own copied bytes without
indefinite live-archive pins. Validate every referenced artifact before activation. Share one
Store transaction API for leases; do not call backup filesystem operations inside DB transactions.
Test concurrent preparation/publication/GC and backup crash. Required gate: artifact delivery
must remain disabled until this matched-extension test passes. Baseline backups do not wait
for unimplemented artifact storage, and alerts do not become a dependency cycle.

## Completion evidence

Each implementation PR reports diff scope, targeted tests, additive migrations, feature defaults,
failure/rollback evidence and unresolved rollout gates. Documentation acceptance requires local
Markdown/link/diff checks and independent review, not waiting for CI or modifying CI settings.
No production deployment, timer, model request, real credentials read or destructive restore is
implied by merging this plan. Target qualification and explicit enablement are later operations.
