# Matched backups and isolated restore qualification

Date: 2026-10-26. CP-007. Status: **proposed design**, not implemented or enabled.
Planning wave: 1 support; estimate: M–L after consistency review.

[Proposal](../../proposals/CP-007-store-backups.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Implementation plan](../plans/2026-10-26-reliability-operations.md).

## Current evidence and boundaries

Reviewed at `33e026a`: `internal/store/{store,schema,task_requests}.go`,
`internal/runner/{mounts,lifecycle}.go`, `internal/config/config.go`,
`cmd/gopod/{main,startup,shutdown}.go`, `docker-compose.yml`.
SQLite uses WAL and one pooled connection; neither guarantees filesystem consistency.
ChatsDir can be outside DataDir. Provider homes, auth, workspaces and IPC are mounted separately.
Stopping the controller alone does not stop same-version agent containers left by startup
cleanup. Existing manual DB snapshots and a healthy process do not prove full recovery.

CP-007 baseline does not depend on CP-003. When artifacts ship, their matched manifest extension
is a **joint rollout gate** before artifact delivery is enabled, not a circular dependency.
Include CP-002's additive scope metadata even before artifacts exist. Preserve personal defaults;
CP-020 gates restored group execution. No backup or production action is authorized by this spec.

## Capture modes and cut

1. `database_only`: Store-controlled SQLite backup API, useful for DB protection but explicitly
   **not complete disaster recovery**. Use the pinned `github.com/ncruces/go-sqlite3 v0.20.0`
   driver's connection API, bounded incremental copying and deadline/close handling. No live
   `cp store.sqlite`, guessed WAL copying or dependency upgrade is required by design.
2. `matched_stopped`: first full recovery mode, opt-in with a declared maintenance window.
   A new host-only admission fence atomically checks idleness and blocks new exec, scheduler,
   IPC dispatch, memory/config/auth writes and container starts. Default action when any
   foreground/background/auth writer or unsettled external effect exists is **defer**, never
   force-stop user work. Drain known writers, stop the controller and all exact-ID owned
   writer containers, verify stopped state, then copy. Fence acquisition and idle check cannot
   be separate racing operations. Exclude unrelated containers and reject unknown writers.

Implement the fence through a new mode-0600 host-local Unix socket with peer-UID verification,
not a Telegram command or agent-writable IPC message. A host supervisor holds the capture lease
while the controller is stopped. Proposed acquisition budget is 30 seconds and capture budget
120 seconds. Failed acquisition releases admission; expired capture is failed/incomplete and
requires verified safe normal-service recovery, not starting against a partial restore. No
zero-downtime guarantee: qualify actual offline duration on the Pi before timer activation.

Use SQLite's backup API even for the stopped source; its helper opens without migrations or
runtime startup. Filesystem writers outside the fence must be excluded by deployment contract
and detected by inventory verification. Pre/post identity/size/hash checks reject observed
mutations; they are defense in depth, not a substitute for stopping writers.

## Inventory, publication and confidentiality

The versioned private manifest records capture ID/mode/start/end, actual running binary/source
revision and immutable controller/agent image IDs, application migration ledger or schema
fingerprint, scope inventory, restore format version, root mappings, exclusions and per-file
sizes/hashes. SQLite `schema_version` alone is not application compatibility evidence.

A matched cut includes the database, required ChatsDir workspaces/memory, provider session and
auth homes, IPC pending/failed/results, policy/profile/config inputs and control-plane state.
Config/auth copies are credential material: do not print resolved env, token values, auth file
contents or digests in public reports. Record external mounts/vault dependencies explicitly;
unavailable required external bytes prevent a claim of complete coverage. Code/images need
not be copied but exact compatible revisions and their availability must be recorded.

Backup root is host-controlled, outside DataDir, ChatsDir, RepoRoot and every agent allowlisted
mount; directories 0700, files 0600, including staging, SQLite outputs and failure reports.
Reject path traversal, destination/source overlap and symlink escapes. Copy critical auth and
artifact bytes without following symlinks or retaining source hardlinks. Workspace external
symlinks remain recorded dependencies; never silently traverse them or claim their targets
were captured. Restore destinations may remap roots only through validated manifest mappings.

Copy to a unique private staging directory; fsync bytes and inventory, validate hashes and DB,
then publish by atomic rename with a completed manifest marker. Interrupted/incomplete captures
are never restore candidates and cannot replace the latest good marker. Enforce operator-chosen
byte quota and reserve before capture; storage exhaustion fails closed. No silent partial backup.

When CP-003 exists, include stable artifact IDs/digests and immutable snapshot bytes for all
DB references, including pending and uncertain sends. A capture lease pauses relevant GC;
release only after publication or failed-capture cleanup. Completed backups own their bytes,
so they do not pin the live archive indefinitely. Retention must preserve the bytes referenced
by every retained backup manifest. Snapshot preparation is fsynced before outbox publication;
that protocol still does not make DB plus filesystem one atomic transaction.

## Offline restore and activation

A separate validator restores into a fresh disposable destination and opens SQLite read-only;
**do not call Store.Open**, which applies migrations. Verify integrity/foreign keys, required
rows/scopes, FTS/vector compatibility, file hashes, permissions and source/schema/image support.
No live credentials are used for validation. Reject incomplete, tampered, missing or newer
unsupported manifests before touching a live directory.

A protected restore-quarantine latch must be checked before migrations, network listeners,
StartupCheck/provider warmup, container cleanup/start, queue recovery, scheduler, IPC consumption,
Telegram polling or auth. A restored instance refuses normal startup until explicit host-side
reconciliation/activation. Test that no second poller, account login, model call, task or send
can start even if copied configuration contains valid credentials. The validator does not
start the normal application just to count messages.

Retain unfinished turns, pending/sending/unknown effects and IPC files as quarantined evidence;
never auto-replay them after restore. Reconcile effects since the capture cut against external
reality before selective activation. Invalidate transient approvals/challenges using a new
restore epoch without exposing their contents. Restoring a DB cannot undo an external send.
Production activation and moving/replacing live roots require a separate operator operation,
with rollback data preserved. Do not launch an older binary that ignores quarantine/new intents.

## Schedule, retention and release gates

Timer defaults **off**. Proposed policy after qualification: one run daily at 03:00 in an explicit
IANA zone, no catch-up storm, one capture at a time; busy instances defer to the next configured
window. Retain 14 daily and 8 weekly completed tool-owned snapshots. Never delete manual
backups or the last restore-verified point, even if that exceeds retention; report quota pressure
and stop new capture when necessary. Unverified snapshots must not evict the last verified one.

Before activation select protected root/quota, accepted offline window and local plaintext
credential-storage policy; run the offline restore drill and qualify resource/latency on Pi.
Off-host transfer/encryption requires a separate destination/key/retention decision and is off
by default. Status contains capture ID, age, mode, verification and bounded failure category,
not secrets. CP-002 alerts are optional; backup correctness does not depend on notifications.
Compose healthcheck work remains separate and is not recovery acceptance.

Tests use synthetic auth/config and local files only: concurrent WAL DB-only backup, capture
fence race/busy/unknown writer, interrupted publication, low disk, root escapes, permissions,
missing artifacts, retained bytes, schema mismatch, offline quarantine and effect reconciliation.
Rollback disables timer/fence admission, reconciles any held lease and retains manifests/verified
snapshots. Never prune data or downgrade schemas automatically as rollback.
