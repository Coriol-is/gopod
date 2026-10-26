# Immutable attachment and voice delivery through IPC

Date: 2026-10-26. CP-003. Status: **proposed design; not implemented or enabled**.
Planning wave: 1. [Proposal](../../proposals/CP-003-typed-replies-via-ipc.md) ·
[Outbox/ScopeRef contract](2026-10-26-telegram-outbox.md) ·
[Common contract](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Executable plan](../plans/2026-10-26-reliability-delivery.md).

## Outcome and current implementation

An agent can request text, generated files and approved voice output without the host uploading a
mutable agent-controlled path later. A queued receipt proves durable acceptance; delivery status is
separate. Personal defaults remain intact during migration; groups cannot borrow personal credentials.

Reviewed source at `33e026a291fbe577efabf43e9d0b6e760fd19ece`:
`internal/ipc/{messages,namespace,authz,receipts}.go` currently accepts only `{chatJid,text}`;
rooted request reads already enforce regular files, size limits and final-component nofollow.
`internal/telegram/{document,longcode,voice}.go` sends uploads directly; automatic long-code documents
and personal TTS exist. Voice TTS uses configured OpenAI API credentials, independently of subscription
model inference. These are source observations, not proof of live configuration or billing approval.

## IPC contract and authority

V0 `{chatJid,text}` remains accepted for compatibility, text-only and thread zero. Never guess its
thread from the latest conversation. V1 uses strict JSON (unknown fields rejected):

```json
{"version":1,"requestId":"host-compatible-uuid","capability":"opaque-run-token",
 "text":"optional text","files":[{"path":"/workspace/chat/report.csv",
 "filename":"report.csv","mime":"text/csv","caption":"optional",
 "presentation":"document"}],"voice":false}
```

At least text, files or voice must be requested; voice requires text. Optional `chatJid` must match the
host-bound destination. No JSON field grants an owner role, arbitrary recipient, private visibility or
thread. Host-issued per-run capability binds actor, ScopeRef, execution/run, delivery owner, destination,
allowed artifact root, permitted methods and expiry. Admission resolves trusted registration, profile
and grants, rather than treating folder `owner` as authority. Capabilities are revoked on run completion,
cancellation or policy change. Agents must wait for durable accepted/rejected receipt before ending;
late unaccepted requests fail closed. Accepted durable intents may dispatch after normal run completion,
subject to dispatch-time scope/policy validation. Do not revoke accepted work merely because its run ended.

Legacy agents retain `legacy_agent_ipc`; stdout has no second automatic delivery. Controller-owned tasks
cannot acquire a second publication by selecting a JSON flag. Groups remain closed until CP-020 and
CP-019 protect filesystem scope and service-response visibility. CP-002 owns the only ScopeRef foundation.

## Bounds and preparation

Proposed defaults: existing 64 KiB JSON request limit; text at most 48 KiB UTF-8; at most four files,
20 MiB each, 40 MiB aggregate; at most 32 total rendered outbox parts. Reject the complete request if
any component fails validation. Limit captions/filenames to renderer-validated Telegram lengths before
publication. Sanitize display filenames (no path components/control characters); preserve source bytes.
MIME and photo presentation are hints subject to host validation; document is the safe default.

Translate `/workspace/chat/...` using the authorized run's host mapping, never arbitrary host paths.
Only that run's ordinary workspace output root is readable. Deny state/database/WAL, IPC, profiles,
authentication, backup, hidden credential/config trees and external mounts. Path policy cannot detect
secrets copied into innocent files; this is not content DLP or full CLI containment.

Walk every component rooted at the authorized directory with nofollow checks; a final-component
O_NOFOLLOW alone is insufficient. Reject symlinks, nonregular files, devices/FIFOs/sockets, hardlinks
(link count other than one), protected roots and traversal. Require stable descriptor identity/metadata
before and after bounded max+1 copying. Fail if the agent changes a file during preparation. Namespace
ancestors must be host protected; group rollout cannot substitute path checks for CP-020 isolation.

Snapshot into host-owned UUID directories (0700), files 0600, inaccessible to agent writes. Compute
SHA-256 while copying, fsync bytes and containing directories. Source mutation/deletion after accepted
publication never changes an upload. Generate code attachments through this same preparation path.

## Inventory, crashes and publication

Add `artifacts(artifact_id PK, namespace_id, epoch, state, relative_blob_key UNIQUE, sha256, size,
media_type, sanitized_filename, created_at, last_reference_at, preparation_id)` and
`artifact_refs(artifact_id FK, ref_kind, ref_id, UNIQUE(artifact_id,ref_kind,ref_id))`.
Use `artifact_reservations` for bounded preparation bytes with owner/expiry and a conservative upper bound.
States: preparing, ready, quarantined, deleting. Published ownership is represented by references,
not mutable agent paths. Only the host can mutate inventory.

1. Validate capability/scope and reserve all requested bytes under quota in SQLite.
2. Outside SQL transaction, securely read sources and prepare/fsync private snapshots.
3. Revalidate scope/capability; commit ready inventory after persisted snapshots exist.
4. CP-002 publication transaction attaches all references and all parts, deduplicates request key/hash,
   and commits an accepted queued receipt outcome. Write filesystem receipt from this durable outcome.
5. Release unused reservation; wake dispatcher after commit.

**Filesystem and SQLite are not one atomic transaction.** Startup reconciliation scans only host-owned
UUID preparation directories without following links: remove expired unreferenced preparations, release
stale reservations, quarantine missing/corrupt ready blobs, and fail affected publication visibly.
A crash between publication and receipt recreates the receipt from the durable request record without
recopying files or republishing. An uncertain send pins its artifact. Never guess remote delivery from
blob existence. Concurrent duplicate requests converge on request key/hash and one publication.

At dispatch, open the immutable snapshot without following links, verify regular identity/size/hash
into a bounded buffer, then upload that same buffer/descriptor; never verify one path and reopen a
mutable pathname. One upload worker bounds memory to the largest allowed file plus renderer overhead.
Reject corrupted snapshots. Each network part acknowledges separately; local all-or-nothing publication
cannot make a multi-message Telegram upload atomic. Do not resend an acknowledged prefix.
Photo-to-document alternatives use identical snapshot bytes and only explicit known refusal, following
CP-002 classification; timeout/5xx never triggers a blind second upload.

## Voice preparation and personal compatibility

Voice is host TTS intent, not a model-supplied URL or shell command. Require explicitly approved backend,
credentials and cost policy for that scope. Personal API keys must not enter group containers or group
TTS authorization. Native subscription inference availability does not imply permission for billed API
TTS or fallback to another provider. No silent provider/billing fallback.

Prepare generated voice as an immutable artifact before publication. A TTS request with ambiguous
charge/result becomes `preparation_unknown`; do not automatically rebill by retrying. Expose failure and
allow an explicit new intent under policy. Fake backend tests suffice for implementation acceptance;
live TTS/billing qualification is a separate authorized gate. Keep existing personal automatic voice and
long-code attachment paths until equivalent snapshot preparation is qualified; do not cut foreground
final delivery over to CP-002 while those paths still bypass durable ownership.

## Quotas, GC and backups

Defaults: 512 MiB reserved+stored per namespace, 2 GiB globally; configurable admission ceiling and
minimum free-space threshold. Count reservations, prepared bytes, quarantine and uncertain artifacts.
Quota/full disk rejects new admission; it must not evict pending/uncertain evidence. RPi memory/disk
qualification must validate defaults before rollout; limits are not a performance claim.

Unreferenced preparations expire after 24 hours. Terminal sent/cancelled/failed artifacts become
eligible 30 days after the last reference; pending/sending/uncertain references remain pinned until
explicit reconciliation. GC marks deleting in a transaction, unlinks only host-owned immutable blobs,
then commits deletion; crash reconciliation finishes that state idempotently. No agent-path deletion.

CP-007 backup takes a bounded lease over referenced artifacts while copying, records manifest ID,
DB snapshot identity, every blob hash/size and inventory relationships. The completed backup owns its
copied bytes; it does not pin live originals forever. Release lease only after manifest verification;
stale leases require explicit backup reconciliation. Restore must verify every required blob and
quarantine missing/mismatched data before enabling senders or GC. Baseline DB backup work can ship
independently; matched artifact backup/restore is a gate before enabling artifact delivery.

## Acceptance and exclusions

Verify symlink/hardlink/traversal/protected-root attacks, concurrent source mutation, max+1 limits,
quota reservation races, ENOSPC, source deletion after acceptance, duplicate request/hash conflicts,
crash in every preparation/publication/receipt/GC window, corrupt blob, partial upload and uncertain
pinning. Verify group closure, actor scope, capability revocation, exact thread retention and legacy
single delivery owner. Test fake TTS ambiguity without remote calls. Receipts/status must distinguish
queued from sent and redact secret paths/errors. New behavior stays disabled until rollout gates pass.
This does not implement arbitrary Telegram media ingestion, transcoding, content scanning or read access
to personal integrations. CP-003 sends authorized generated outputs only.
