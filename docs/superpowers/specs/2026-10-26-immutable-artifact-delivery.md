# Typed artifact replies and immutable snapshots

Date: 2026-10-26. CP-003. Status: **proposed design**, not implemented or enabled.
Planning wave: 1; size estimate: M.

[Proposal](../../proposals/CP-003-typed-replies-via-ipc.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

A mutable file path is neither an immutable deliverable nor authority to export data.
Files/voice need scoped diagnostic receipts and safe delivery without replacing streaming text.

## Dependencies and boundaries

CP-002 durable publication; CP-014 media queues; CP-007 matched backups.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Extend the existing IPC message contract with versioned optional files and voice intent;
  retain old text-only requests. Product defaults proposed for the first slice: at most 4
  files and 20 MiB per file, with a bounded total request budget. These are product caps, not
  claims about current Telegram limits; verify transport limits before implementation.
- The host validates the namespace-scoped source and copies ordinary bytes into private service-
  owned snapshot storage before publishing an outbox reference. Record artifact ID, SHA-256,
  size, original filename/MIME, namespace/epoch, conversation, task/run and visibility. The
  agent never chooses a host snapshot directory.
- Use rooted/no-follow source access, reject nonregular files, symlink/hardlink aliases and
  protected auth/state/backup/profile exports. Bind/mount policy remains necessary: path
  checks cannot detect credentials deliberately copied to an innocent filename.
- Fsync prepared snapshots then atomically commit outbox references and applicable task/history
  results. Filesystem and SQLite are not one atomic transaction: retain bounded orphan
  preparations for host reconciliation. At send time verify the snapshot and route, then
  upload the exact buffered/stream-verified bytes without reopening an unchecked source path.
- Keep normal text streaming; final artifacts use the outbox. Photo/document fallback uses the
  same verified bytes. Voice explicitly requested through IPC uses the host TTS path, with its
  own prepared snapshot and failure receipt. Private auth challenges do not enter ordinary
  artifact storage.
- Define storage quotas, expiry and reference-aware GC in this feature; do not copy the donor's
  indefinite retention. Pending/uncertain or backup-referenced artifacts must not disappear
  because a cleanup task guesses they are unused.

## Acceptance

- Modify/delete original after enqueue: recipient still gets the snapshot bytes and original
  filename/MIME.
- Symlink swap, hardlink, foreign namespace, route mismatch, tampered snapshot and protected-
  file export fail before upload.
- Final DB transaction failure publishes no partial set of files/text; prepared orphans remain
  bounded and reconcilable.
- Photo fallback preserves bytes; voice/file errors return scoped IPC reasons;
  cancellation/restart do not fabricate delivery.

## Migration, retention and rollback

Legacy path-only queued items are quarantined, not assigned fabricated hashes. Re-prepare only
on explicit authorized intent after reviewing current bytes. Restore DB and snapshot inventory
together; old senders must not reopen unverified paths during rollback.

## Risks and decisions before implementation

Disk pressure on Pi; personal-to-group export leakage; retention conflicts with uncertain
delivery and backups.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

Full office suite in every image; source content secret detection; automatically publishing a
private artifact to another chat.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`src/artifacts.js; docs/artifacts.md; test/artifacts.test.js`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
