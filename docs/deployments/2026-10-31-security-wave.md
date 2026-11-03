# Pi deployment — security wave, 2026-10-31

Owner explicitly authorized deployment after the eight-group fix wave had been accepted.
The deployed source is `e3b540116687bbf1679e3b26c154830c0000a6f4` (code baseline `e586c4a`).
All PR checks and the main CI for that source were green; the wave's combined build/vet/full tests
and relevant race suites passed. See [wave acceptance](../reviews/2026-10-31-security-wave-acceptance.md).

## Before deployment

- Pi checkout `~/gopod`: clean `630aa4e`; controller image
  `sha256:646f9f5d30b32372ff759559b6833db1f5cb05f9c1ae18fc11eaf7b239298529`, running, restarts 0, OOM false.
- Docker bridge gateway `172.17.0.1`; new memory API bind default fits the actual host.
- `backup idle` passed before work and immediately before stopping: no pending/running turns,
  outbox in-flight/unresolved work, artifact reservations, executing tasks or held attempts.
- Existing flags: outbox dispatcher and personal IPC enabled; artifacts and managed tasks unset/off.
- DB: 176 done turns, 14 sent outbox parts, 9 tasks (7 active/2 done), 31 sessions,
  207 memories and 2 registrations. No uncertain deliveries.

## Backup, build and switch

1. Tagged the old image `gopod-gopod:rollback-pre-security-e3b5401`.
2. Online database backup `20261031T001910Z-db-a42b1b89`: created and verified OK,
   6,533,120 bytes, 32 tables, 6,803 rows at snapshot time.
3. Fast-forwarded the Pi checkout to the exact approved SHA. Built on the Pi with
   `docker compose build --build-arg VERSION=e3b5401 gopod`; no agent image/CLI changes.
4. Rechecked idle, stopped the controller gracefully, checked/stopped any remaining gopod-chat
   containers, and created `20261031T002335Z-ms-376e0956` in `matched_stopped` mode.
   Verification passed: 1 file, 32 tables, 6,805 rows. There were no artifact blobs or declared
   config files to include. The service was not restarted until verification completed.
5. `docker compose up -d --no-build gopod` started the new controller at **00:24:07 UTC**.
   Image: `sha256:289e86dcd86ce470c4ba8d38c97b42da944f40b6b2e14752333d11fb9d89cf69`.
   Candidate tag retained as `gopod-gopod:security-e3b5401`.

Backup commands used Compose one-off containers with the host backup root mounted at `/backups`.
For SSH scripts supplied on stdin, use `docker compose run ... < /dev/null` so the one-off
container cannot consume the remaining script. An initial idle-only attempt consumed stdin and
exited without stopping or changing the service; it was repeated with stdin detached.
Both sets live under `~/gopod-backups`. They do not capture provider auth/session homes, chat
workspaces, pending IPC files or secrets; those paths were preserved in place. No restore was run.

## Observed acceptance

- New controller reports version `e3b5401`, running, restart count 0, OOM false.
- Startup: Docker/runner/memory ready; owner Claude container ready, CLI 2.1.310 ready;
  queue and outbox dispatcher running; lifecycle startup notice sent; Telegram long polling started.
- Read-only Telegram `getMe` and `getMyCommands` succeeded; command menu contains `outbox`.
- Memory publication is **172.17.0.1:9876**, not wildcard. Host bridge and owner container
  (`host.docker.internal`) both get `/health` 200 and unauthenticated `/memory/list` 401.
  A TCP connection to the Pi LAN address on 9876 is refused. Port 9877 remains as before.
- SQLite integrity check returns `ok`. Schedules, sessions and registered-chat rows exactly match
  the matched stopped snapshot; counts remain 9/31/2. Memories remain 207. Outbox remains 14 sent,
  no uncertainty; all 176 turns done, no pending/running work.
- Existing feature flags are unchanged. No account login/refresh, paid fallback, model inference
  canary, Gmail/Calendar acceptance run, deliberate schedule change, group activation or host reboot.

This is operational deployment acceptance, not proof of Telegram client UX or native-provider inference.
Still open: real trigger/callback denial, `/outbox` confirmation/settlement, ordinary messages during
device login, and existing provider/group qualification gates. M9 and the upload first-128 window
remain as documented in wave acceptance.

## Rollback

The old image and both verified backups are retained. For an image-only rollback, first check idle,
then stop the service, tag `gopod-gopod:rollback-pre-security-e3b5401` back to `gopod-gopod:latest`,
and start with `docker compose up -d --no-build gopod`. The source checkout can remain on the new
revision; do not rebuild during rollback. The bridge binding is compatible with the old binary.
A DB restore is a separate offline procedure: do not overwrite the live database or replay uncertain work.
