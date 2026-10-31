# Pi deployment — explicit private-chat Stop, 2026-10-31

The owner authorized checks and merge of PR #124, then deployment.
[PR #124](https://github.com/Coriol-is/gopod/pull/124) was admin squash-merged at
19:54:53 UTC as `8a3ce75113c0709c63ede99ed437fad4d8e191a0`. The source branch is retained.

## Release checks

- Final PR head `9dad3a535a2008f3442f90aabe32c3ce25f4a218`: build/vet/full tests and
  affected-package race tests passed. Source re-review fixed a buffered voice-only
  regression and added a regression test. This was implementer review; no separate
  human GitHub approval is claimed.
- PR CI [41236207502](https://github.com/Coriol-is/gopod/actions/runs/41236207502):
  `test` and `isolation-spike` passed; SonarCloud also passed on that head.
- Main CI [41236417748](https://github.com/Coriol-is/gopod/actions/runs/41236417748)
  passed on the exact merge SHA before the service switch.
- Existing real local Docker cancellation test passed (114 ms to result). Native
  Claude/Codex inference and Telegram-client Stop acceptance are separate gates.

## Backup and rollout

Previous checkout: clean `e3b5401`; image
`sha256:289e86dcd86ce470c4ba8d38c97b42da944f40b6b2e14752333d11fb9d89cf69`.
Retained rollback tag: `gopod-gopod:rollback-pre-stop-20261031`.

1. Created and verified online backup `20261031T195134Z-db-44ca3933`:
   database-only, 6,713,344 bytes, 32 tables, 7,076 rows at capture time.
2. Fast-forwarded the Pi checkout to the exact merge SHA and built with
   `docker compose build --build-arg VERSION=8a3ce75 gopod`. The old controller
   kept serving during the build. No agent image, CLI, credential or feature flag
   was changed. Candidate tag: `gopod-gopod:private-stop-8a3ce75`.
3. A Telegram turn became active during preparation. Deployment waited for it to
   finish; no deliberate user-turn interruption was performed. Rechecked idle:
   zero pending/running turns, in-flight/unresolved outbox parts, artifact
   reservations, executing task runs and held attempts.
4. Stopped the controller gracefully and the remaining labeled agent container.
   Captured and verified matched-stopped set `20261031T200015Z-ms-b00fa5a4`:
   1 file, 32 tables, 7,101 rows. No config files or artifact blobs were present
   to include. One-off backup containers used detached stdin.
5. Started the new controller with `docker compose up -d --no-build gopod` at
   **2026-10-31 20:00:48.751 UTC**. Runtime image:
   `sha256:d273819f674a1f6a73444170e6a2ddcc112af83076009111dc8c7efaa4ceec5f`.

Both verified sets remain under `~/gopod-backups`. They do not back up native
provider session/auth homes, workspaces or pending IPC files; those existing
mounts were preserved in place. No restore was performed.

## Operational verification

- Controller running; version `8a3ce75`, restart count 0, OOM false.
- Owner Claude container and CLI ready; CLI version unchanged at 2.1.310.
- Startup classified the prior shutdown as graceful and replayed zero turns.
- Memory `/health` returned 200/`ok` on the existing Docker bridge binding.
- Telegram read-only `getMe` succeeded. `getMyCommands` reports 23 private
  commands including `stop`; the default menu does not contain `stop`.
- Before/after row hashes exactly matched for all 34 session mappings, 10
  schedules and 2 registrations. Existing flags matched: outbox dispatcher/IPC
  enabled, artifacts/managed tasks unset/off. No schedule was deliberately changed.

This verifies deployment and menu publication, not real-client acceptance.
A08/A10 still require actual inline-button and `/stop` tests during thinking,
execution and waiting, plus next-turn native session continuation for Claude and
Codex. No paid inference/TTS, account login/refresh, Gmail/Calendar canary, group
activation or host reboot was performed. Known best-effort process cleanup and
stopped-reply delivery limits remain in the [audit](../reviews/2026-10-31-private-chat-interruptions.md).

## Rollback

Check idle first, stop the controller, retag
`gopod-gopod:rollback-pre-stop-20261031` as `gopod-gopod:latest`, and run
`docker compose up -d --no-build gopod`. Keep the source checkout; do not rebuild
while rolling back the image. The schema was unchanged. Database restore is a
separate offline operation; never overwrite the live database or replay uncertain
work merely to roll back this UI feature.
