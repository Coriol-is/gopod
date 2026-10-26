# Matched backups and isolated restore qualification

Date: 2026-10-26. CP-007. Status: **proposed design**, not implemented or enabled.
Planning wave: 1 support; size estimate: M.

[Proposal](../../proposals/CP-007-store-backups.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

Manual SQLite copies before deployment do not establish automated recovery, especially once DB
rows reference artifact bytes, sessions and profiles.

## Dependencies and boundaries

CP-003 when artifact storage is introduced.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Create host-controlled protected backups using SQLite's backup API rather than copying a live
  WAL file. Include a manifest with schema/source/image revisions, namespace inventory and
  hashes of referenced immutable artifacts plus required workspaces/profiles/session/auth
  state.
- Define a consistent capture cut: either drain relevant writers/auth mutations, or use a proven
  immutable-snapshot protocol and manifest verification. A live tar plus a database file is
  not automatically a matched snapshot.
- Backup directories are mode 0700 and ordinary backup files 0600, outside all agent mounts.
  Never print credentials or resolved configuration. Local scheduled retention is bounded and
  avoids deleting the last verified restore point.
- Off-host transport and encrypted storage require explicit destination/key/retention choices
  before enabling; this initial proposal does not silently upload auth backups. Auth backups
  are private credentials even when the source instance is group-scoped.
- Restore into a disposable offline instance first, with polling, sends, schedules and actual
  auth use disabled. Verify DB integrity, referenced file hashes, scope isolation and
  source/schema compatibility before enabling a real instance.

## Acceptance

- Scheduled synthetic backup survives WAL activity and restores known records plus artifact
  hashes.
- Missing snapshot bytes/schema mismatch fail restore validation before any delivery.
- Scratch restore starts no second Telegram poller, performs no account login/model request and
  sends nothing.
- Retained backup manifests identify the actual source/image/config revision; permissions and
  retention are checked.

## Migration, retention and rollback

Reuse existing manual backups as historical evidence, not automatic readiness. Introduce
protected schedules only after an isolated restore drill. Rollback preserves newer audit/effect
records and never pretends that restoring DB undoes external actions.

## Risks and decisions before implementation

An unmatched restore can duplicate external sends or expose private auth. Filesystem snapshots
and DB transactions require explicit consistency coordination.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

Automatic cloud uploads, new backup credentials, resetting production or full off-host
encryption implementation in the first slice.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`docs/backups.md; scripts/backup.js; scripts/backup-agents.py; docs/artifacts.md`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
