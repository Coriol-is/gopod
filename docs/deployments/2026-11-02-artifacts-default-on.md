# Pi deployment — artifact delivery on by default (D057), 2026-11-02

Owner authorized ("deploy", 00:58 CET) after [PR #137](https://github.com/Coriol-is/gopod/pull/137)
merged as `d8866c9`. Rollout over Tailscale (`rpi4`, `100.123.150.53`).

## Release checks

- Main CI on `d8866c9` ([run 41361675937](https://github.com/Coriol-is/gopod/actions/runs/41361675937)):
  `test` and `isolation-spike` passed. The PR was merged while its SonarCloud
  quality gate was red (Maintainability B on new code: 2 code smells on 12 new
  lines, debt ratio 6.7%) and before `test`/`isolation-spike` had finished — a
  process error by the deploying session, recorded here; main CI passed afterwards.
- Local: config/cmd/telegram/ipc suites green on the PR head.

## Backup and rollout

Previous checkout `be7575f`, image
`sha256:45ffd4e9322b32e3658fddeb4d2f9c31f7270f285b3a424190be274ffa86bd6d`.
Retained rollback tag `gopod-gopod:rollback-pre-artifacts-20261102`.

1. Online backup `20261101T235844Z-db-7d82b4d7` created and verified
   (database-only, 7,188,480 bytes, 38 tables, 7,732 rows).
2. Checkout fast-forwarded to `d8866c9`; image built with `VERSION=d8866c9`
   while the old controller served (tag `gopod-gopod:artifacts-d8866c9`).
3. Idle check: `turns pending=0 running=0; outbox parts in flight=0; unresolved deliveries=0; artifact reservations=0; task runs executing=0; task attempts held=0`.
4. `docker compose stop -t 60 gopod` at 00:04:16 UTC; no agent container was running.
5. Matched-stopped backup `20261102T000419Z-ms-67366fa1` created and verified
   (database + declared config + artifact blobs, 38 tables, 7,737 rows).
6. `docker compose up -d --no-build gopod`; new controller started
   **2026-11-02 00:04:53 UTC**, image
   `sha256:5e75c582b7f1308eb36a21b4b8389eb4cd9d8f04fc1f7c5b4382c49f2cecc106`.
   Downtime about 37 seconds.

## Operational verification

- `gopod starting version=d8866c9`; `leftover cleanup complete cleaned=0`;
  `session mapping mode=legacy`; `outbox dispatcher running`; startup notice
  sent (`previous_run=graceful replayed=0`); 23 commands published; long poll.
- No `artifact delivery DISABLED` line; `/opt/gopod/data/artifacts` was created
  at 00:05 (mode 0700), so the artifact store opened and uploads are accepted.
- After 70 s: running, restart count 0, OOM false, zero `ERROR`/`WARN` lines;
  host memory about 2.2 GiB used of 3.8 GiB.

Not verified: an actual file attachment from an agent (the agent-facing
contract is CP-040, in progress). Real-client checks from the previous
rollout remain open.

## Rollback

Check idle, stop the controller, retag
`gopod-gopod:rollback-pre-artifacts-20261102` as `gopod-gopod:latest`, and
`docker compose up -d --no-build gopod`; or keep the image and set
`GOPOD_OUTBOX_ARTIFACTS=0` in `.env` and restart. Schema unchanged by this
release.
