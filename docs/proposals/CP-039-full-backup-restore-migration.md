# CP-039 — Automatic full backup, restore and device migration

| | |
|---|---|
| Status | proposed (draft, owner request 2026-11-01; to be refined before acceptance) |
| Date | 2026-11-01 |
| Origin | Owner asked for an automatic backup/restore of the whole bot, all files and sessions included, and for moving the installation to another device |
| Size | L |
| Touches | `internal/backup`, `internal/maintenance`, `internal/store` (manifest, epochs), `cmd/gopod` (`backup`/`restore`/`migrate` subcommands), `deploy/` (timer, retention), `docs/deployments/BACKUP-RESTORE.md` |
| Depends on | CP-007 (foundation: sealed sets, `matched_stopped`, quarantine start), D043, CP-003 artifact leases for any online matched mode, CP-037 P1 session identity (durable workspace/session records), D012 (secrets only in env) |
| Spec | `—` until written |

## Problem

Today a backup set protects the database only (`docs/deployments/BACKUP-RESTORE.md`):
`not_covered` always lists chat workspaces (`ChatsDir`), provider session homes
(Claude/Codex native transcripts), provider auth homes, pending IPC files, container
images/source and secrets. Nothing is scheduled, nothing is pruned, nothing leaves
the host, and restore is a manual, offline, quarantine-first procedure with no
`restore` command. A disk failure or a lost Pi therefore loses every native session
transcript and workspace even when the latest database set is intact, and moving
gopod to another device is an undocumented hand copy of several directories plus a
re-login of every provider.

## Proposal

Extend CP-007 into a complete, automatic and portable recovery story in three parts.

1. **Full matched set.** A `matched_stopped` capture (and later an online matched
   mode behind artifact leases) also contains, under one manifest with per-file
   hashes: chat workspaces, per-session workspaces and native context homes
   (CP-037 records which session owns which directory), provider auth homes and
   pending IPC files, declared config, and a record of the exact controller/agent
   image digests and CLI versions. Secrets stay in env; auth homes are captured
   encrypted at rest with an operator-held key and are never printed.
2. **Automation.** An opt-in timer (`deploy/`, or a controller-internal scheduler
   under quiet hours per CP-006) takes online database sets frequently and a full
   matched set in a maintenance window, with bounded retention (count and bytes),
   never deleting the newest verified set, and an optional off-host copy of sealed
   sets to one operator-configured target (rsync/SSH or S3-compatible), verified by
   hash after transfer. Failures are reported to the owner chat, not only logged.
3. **Restore and migration.** `gopod restore <set>` rebuilds a complete data root
   from a verified set on the same or a new host, starts in quarantine, reconciles
   `work_evidence`, verifies provider sessions resume by id, and only then releases
   the latch. `gopod migrate export|import` wraps the same set format for a device
   move: export on the old host, import on the new one, with an explicit cut-over
   checklist (stop old poller first; one bot token, one poller), a restore epoch
   that invalidates transient approvals and capability tokens, and image/CLI
   version pinning so the new host runs the same build before anything connects.

## Out of scope

Live replication or hot standby, multi-host serving, restoring into a different
owner or bot token, cloud-provider-specific tooling, re-login automation for
providers whose auth is device-bound (the import must say what needs a fresh login),
and any automatic production restore without an explicit operator command.

## Risks

- Full sets contain chat content, workspaces and encrypted credentials: a weak
  key, a readable backup root or an unverified off-host target leaks everything.
- A set that is matched for the database but not for native homes restores a
  session index that points at missing or newer transcripts; the manifest must
  prove the cut was consistent and restore must refuse partial sets.
- Restoring on a host with a different `GOPOD_HOST_DATA_DIR`, uid/gid or Docker
  version silently breaks bind mounts and ownership; migration must rewrite or
  verify these, not assume them.
- Two controllers polling one token after a half-finished migration duplicate
  deliveries; the cut-over must be a documented, verified sequence.
- Off-host transport and timers add a long-running privileged job to the Pi;
  memory and disk headroom (36 GiB free, 3.8 GiB RAM) bound set size and frequency.

## Acceptance

- A full matched set lists no `not_covered` entry except container images/source
  and explicitly undeclared config; `verify` proves every file hash and the schema.
- A scheduled run produces, verifies and retains sets without operator action and
  reports a failed run to the owner chat; retention never removes the newest
  verified set and respects the configured byte cap.
- A restore drill on a scratch host (or scratch data root) from the newest full set
  brings up quarantine, reconciles evidence, resumes at least one Claude and one
  Codex session by id, and releases with no replayed effect; evidence is dated and
  linked from `docs/deployments/`.
- A device migration drill moves the installation to a second machine with one
  documented command sequence; the old poller is stopped before the new one starts,
  transient approvals and capability tokens are invalidated, and the owner chat
  receives exactly one startup notice from the new host.
- No secret value appears in any set manifest, log, report or repository file.
