# Backup and restore runbook (CP-007 foundation)

Date: 2026-10-29. Applies to a gopod build that contains `gopod backup` (not any earlier image).
This replaces the manual "stop, cold-copy `store.sqlite` + `-wal` + `-shm`" step of earlier deployment
reports. The Raspberry Pi host has **no `sqlite3` CLI**; none is needed: everything below runs the `gopod`
binary already inside the image, which embeds SQLite.

Nothing here is scheduled. There is no timer, no pruning, no off-host copy and no automated restore.

## What you get, and what you do not

| Mode | How | Covers | Writers |
|---|---|---|---|
| `database_only` | online, controller keeps running | consistent snapshot of `store.sqlite` including committed WAL content | none stopped; a concurrent turn may commit just after the cut |
| `matched_stopped` | controller stopped first | database + declared config files + artifact blobs referenced by the database | refused unless the controller is down and no agent container is live |

Every set lists in its manifest what it does **not** contain (`not_covered`), and `verify` prints it:
`chats_dir_workspaces`, `provider_session_homes`, `provider_auth_homes`, `ipc_pending_files`,
container images/source, and secrets unless you declare them with `--config`.

**A set is not a matched filesystem/auth recovery guarantee.** It protects the database (chats, messages,
turns, memory, vectors, outbox/task ledgers) and, in `matched_stopped`, the artifact bytes the outbox may still
send. Provider logins, group workspaces and Claude/Codex session homes are not captured; restoring only a
database can leave sessions and workspaces ahead of or behind it. Hashes are unkeyed: they detect corruption and
accidental change, not tampering or authenticity (`verify` prints this every time); anyone who can rewrite a whole
set can rewrite its hashes. Restoring a database cannot undo anything already sent to Telegram.

Backup sets contain chat content (and config files you declare, which can be credentials). Keep them on the
host, mode 0700/0600, and never print or paste them.

## Setup (once)

```sh
# on the Pi, as the user that runs docker compose (uid must equal GOPOD_UID, default 1000)
mkdir -p ~/gopod-backups && chmod 700 ~/gopod-backups
cd /opt/gopod/repo        # directory with docker-compose.yml and .env
```

The backup root must be outside the data dir, chats dir and repo; the tool refuses otherwise, and refuses a
symlinked root or one with group/other access. Mount it into the one-off container as `/backups`.
All commands use `docker compose run --rm --no-deps`, which starts a throwaway container from the same image,
env file, user and data mount; it does not start Telegram, the scheduler or any agent (`gopod backup` never
opens the store through the application and runs no migrations). Define a shortcut:

```sh
GP='docker compose run --rm --no-deps -v '"$HOME"'/gopod-backups:/backups -e GOPOD_BACKUP_DIR=/backups gopod'
```

Exit codes: 0 ok, 1 failed or not restorable, 2 usage, 3 refused because owned writers are not settled
(nothing was stopped or changed). Add `--json` for machine output.

## A. Online database backup (no downtime)

```sh
$GP backup create                 # database_only
$GP backup list
$GP backup verify <set-id>        # offline restore check; use the id printed by create
```

`create` snapshots through a private SQLite connection (the application's connection is not used), checks
`integrity_check` and `foreign_key_check`, records row counts and a schema fingerprint, writes the set under
`/backups/.staging/` with 0700/0600 permissions and fsyncs it, then publishes it with one atomic rename.
If the database is under heavy write load the copy can keep restarting; the capture then ends with
"snapshot cancelled or timed out after N copy restarts ..." (default `--timeout 5m`). Nothing is published; retry in a
quiet period or raise `--timeout`. Leftovers of a crashed capture under `.staging/` are removed automatically by the
next capture once they are older than 30 minutes and no live capture holds their lock; they never count toward
`--max-bytes`.
A database that already contains orphan rows (foreign key violations) is still backed up in `database_only` mode: the
manifest records a warning naming tables and counts (never rows) and `verify` repeats it. `matched_stopped` refuses
such a database; repair it first.
Use `--max-bytes N` to cap the total size of the backup root; free space is also checked first and a shortfall
fails the capture without writing a set. A set that is listed as `sealed` is complete; an interrupted capture
never appears.

`verify` copies the database to a private scratch directory (default `/backups/.staging`, so a small `/tmp` is not the
limit; override with `--scratch DIR` or `GOPOD_BACKUP_SCRATCH_DIR`; it needs about 2-3 times the database size, and a
capture needs about 2 times plus the artifacts) and checks: completion marker, manifest, every
file size and SHA-256 (opened component by component without following symlinks, so a symlinked `config/` or
`artifacts/` directory is rejected), no unlisted files, `integrity_check`, foreign keys, fingerprint and row counts, that the
schema is **not newer than this binary**, and that every ready artifact the database references has its blob
(`matched_stopped` sets) or is flagged as not covered (`database_only`). It does not touch the set or the live
store and activates nothing.

## B. Stopped-writer backup (`matched_stopped`)

Use before risky changes such as a deploy, or when artifact delivery is on. It needs downtime.

**Never take a `matched_stopped` capture before the old controller container has been replaced by a build that
contains this feature.** An older controller does not hold the instance lock, so the lock cannot prove it is gone.
As a best-effort extra check the capture also refuses while any non-one-off container of the compose service
`gopod` is running (and fails closed when the container list cannot be read), but a controller started some other way
is invisible to it. Stop it by hand and confirm with `docker ps` before capturing.

1. **Check the system is idle** (read-only, safe while running):
   `$GP backup idle` prints pending/running turns, outbox parts in flight, unresolved deliveries, artifact
   reservations, executing task runs and held attempts. Exit 1 means busy: **defer**. Stopping a busy system
   interrupts user work; this tool never does that for you.
2. Stop the controller through the normal procedure: `docker compose stop -t 60 gopod`.
3. Stop any agent containers that are still live. List them with
   `docker ps --filter label=gopod.chat` and stop exactly those, by id. (Controller shutdown does not remove
   same-version agent containers.) The capture checks this itself and refuses while any is
   running, restarting or paused.
4. Capture (declare config you want in the set; repeat `--config`):
   ```sh
   $GP backup create --mode matched_stopped --config /opt/gopod/data/mount-allowlist.json
   ```
   Add `--config /opt/gopod/repo/.env` only if you accept credentials inside the set; the file is copied
   without following symlinks and its contents are never printed. If the inventory cannot be read and you know
   no agent containers can exist (for example `GOPOD_NO_CONTAINER=1`), `--assert-no-containers` records that as
   operator-asserted, not verified.
   The capture holds the controller lock, so a controller started meanwhile waits up to 60 s and then exits;
   `restart: unless-stopped` retries it afterwards.
5. `$GP backup verify <set-id>`
6. Start the controller again: `docker compose up -d --no-build gopod`. Rows that were mid-flight when the
   controller stopped are recorded in the manifest as `work_evidence` and are reconciled by the normal start.

If step 4 prints `backup refused ... nothing was stopped or changed`, a controller or agent container is still
live; fix that (steps 2–3) and retry. A failed or refused capture publishes nothing.

## C. Restore (manual, offline-first)

There is no automated restore. Never copy over the live data directory.

1. `verify` the set (above) on the host that will restore. A set with a newer schema than the binary, a bad
   hash, a missing marker, a missing artifact blob or any unlisted file is rejected.
2. Prepare a **new, empty** data directory (for example `/opt/gopod/restore-data`), copy `store.sqlite` from the
   set into it (mode 0600), copy `artifacts/` from the set to `<newdir>/artifacts/` for `matched_stopped` sets,
   and place the restore latch before anything can start:
   `GOPOD_DATA_DIR=<newdir> gopod backup quarantine set`.
3. Start the instance against `<newdir>`. With the latch present (or `GOPOD_RECOVERY_QUARANTINE=1`) it runs in
   **quarantine**: control plane only. It opens no Telegram connection, polling, scheduler, IPC watcher, queue
   recovery, model or auth warmup, container cleanup or delivery, no metrics listener, and does not run migrations.
   It logs one read-only validation result of the store. Even if valid credentials are in `.env`, no poller
   starts. Make sure the production controller is not polling the same bot token before you leave quarantine:
   two pollers on one token conflict.
4. Reconcile by hand: unfinished turns, `pending/sending/uncertain` outbox parts and IPC files in the restored
   data are **evidence**, not work to replay. Compare effects since the capture time with what Telegram actually
   shows before deciding anything. The manifest `work_evidence` counts say how much was in flight.
5. Only when satisfied: `GOPOD_DATA_DIR=<newdir> gopod backup quarantine release --yes`, then a normal start.
   Replacing the live roots is a separate, explicitly authorized operation; keep the previous roots until the
   restored instance is proven. Do not start an older binary that predates quarantine against restored data.

## Retention and rollback

Sets are never pruned by the tool. Delete old ones by hand, and never the newest set that passed `verify`.
Rolling back this feature means not running `gopod backup`; existing sets stay valid files. The controller lock
file `<data>/.controller.lock` is harmless and is released by the kernel when the process exits.

## First use

The image running on the Pi before this change has no `gopod backup`. Take the pre-deploy copy for the deploy that
introduces it the old way (graceful stop, cold copy of `store.sqlite`, `-wal`, `-shm`, `.env`, mode 0600), then use
this runbook for every later backup. Run a first `create` plus `verify` on the Pi and record the set id, duration
and size before relying on it; a restore drill into a scratch directory is still to be qualified.
