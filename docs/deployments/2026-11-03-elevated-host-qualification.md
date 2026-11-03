# Elevated host qualification — 2026-11-03

The owner authorized memory-cgroup configuration, a whole-Pi reboot and
before/after checks of all running containers. Controller remained `75b3be3`.
Online backup `20261103T103308Z-db-584284ba` verified (43 tables, 8,286 rows).
Boot command line backup: `/boot/firmware/cmdline.txt.pre-memory-20261103T103437Z`.
The script appended `cgroup_enable=memory`; reboot was requested at 10:34:44 UTC.

## Reboot acceptance

Inventory at 10:32:40 UTC compared with 10:36:15 UTC: all nine container names
returned, all image IDs unchanged. The controller recreated its managed owner
container as expected. Seven configured Docker healthchecks were healthy:

| Container | After reboot |
| --- | --- |
| gopod-gopod-1 | running, no healthcheck |
| gopod-owner | running, no healthcheck |
| codex-agent-pi-agent-assistant-1 | healthy |
| onecli | healthy |
| onecli-postgres-1 | healthy |
| honcho-api-1 | healthy |
| honcho-deriver-1 | healthy |
| honcho-database-1 | healthy |
| honcho-redis-1 | healthy |

Honcho health, OneCLI web and gopod memory health returned HTTP 200 before and
after. Gateway root HTTP 400 and group-tool `/health` HTTP 404 matched their
baseline (not successful health endpoints). Both PostgreSQL readiness probes
passed; Redis returned PONG. Registration/session/task hash snapshots were
identical (2 registrations, 38 mappings, 15 tasks); SQLite integrity passed.

Docker now reports MemoryLimit, CPUCfsQuota and PidsLimit true. An isolated probe
observed memory.max=134217728, pids.max=32, cpu.max="50000 100000". The initial
host-authority probe through the controller service confirmed root, shared host
PID/network/IPC namespaces, Docker socket access and `nsenter ... -- true`.

## Updated images and manager acceptance

Built on Pi from the PR sources without replacing production tags:

- `gopod-agent-elevated:e6-candidate`: `sha256:7ed5e2e85a14b73f688ced63591595feea51e9288166be86d8123cebf994948d`
- `gopod-agent-elevated-codex:e6-candidate`: `sha256:0680d49d001519da2669b1f325157b61ca0401b3f2a18b76dd5fc5a84b8f5c42`

Both passed offline smoke: Claude 2.1.310, Codex 0.164.0, Docker CLI 29.9.2,
Compose 5.7.0. The cross-compiled opt-in `TestElevatedHostLifecycleProof` passed
on Pi in 15.30 seconds with separate images and a disposable DB/native homes.
It proved fixed read-only host exec through each image's real supervisor,
root/host namespaces/nsenter, actual memory cap, journal settlement, explicit
close and fresh-manager survivor cleanup. It does not invoke native inference.
All nine production containers remained running; no elevated test container
remained. Keep production elevated off while running this host-global test.

Build, vet and the full local suite passed. Existing independent source review
and a focused review of the opt-in test passed. PR CI is required on the final head.

## Remaining acceptance and activation

The owner subsequently requested `GOPOD_ELEVATED=1` on rpi4 after merge/deploy;
the controller default remains off. Enabling the command does not start a session:
owner-private provider selection and explicit warning confirmation are required.
Native provider login/new/resume/tool execution and real Telegram callback
acceptance remain open. No ordinary credentials were copied. Container healthchecks
are queued separately in ROADMAP.

## Controller rollout and rpi4 activation

PR #154 merged as `294e8a77dc793313ff6b069685ee9b3d87b4d292` after independent
review and green test/isolation/Sonar checks on `124bc1a`. Exact main CI
[41622458555](https://github.com/Coriol-is/gopod/actions/runs/41622458555) passed.
Sonar CLI was used; the quality gate passed, with complexity findings and existing
touched-file maintainability findings still reported (none suppressed).

Controller built on Pi with version `124bc1a`; `git diff --quiet` verified that
its source tree equals the squash merge. Checkout is detached at `294e8a7`.
Image: `sha256:a1bf425003559a85109e43ee5bbb91efe88234a5c00f776c24c6c343107aea46`.

- Online backup `20261103T104727Z-db-29e9eff4` verified: 43 tables, 8,317 rows.
- Idle gate: all counters zero. Stopped-writer backup initially refused the still
  live idle owner container; it was explicitly stopped before retrying.
- Matched-stopped backup `20261103T105305Z-ms-d150a86a` verified: 43 tables,
  8,322 rows; no config files were declared. Native homes remain outside coverage.
- Rollback controller tag: `gopod-gopod:rollback-pre-elevated-e3-20261103`.
  Both provider images retain `rollback-pre-e3-20261103` tags.
- Controller `.env` saved as `.env.pre-elevated-20261103T105344Z`, mode 0600.
  Explicitly set `GOPOD_ELEVATED=1` and both dedicated production image names.
  Repository defaults remain off. Candidate images promoted to `latest`.

Controller started 10:53:46 UTC, restart count zero, OOM false; ordinary owner
Codex self-check passed, scheduler/outbox/IPC ready, Telegram startup notice sent.
All nine containers run, all seven configured healthchecks healthy. Memory API,
Honcho and OneCLI return HTTP 200. Registration/session/task snapshots are
identical to pre-deploy; SQLite integrity OK. Telegram `getMyCommands` confirms
`root` in the private menu. Active elevated sessions: zero, as intended.

Enabling the controller flag exposes the owner-private confirmation flow without
starting root execution. To disable it, set `GOPOD_ELEVATED=0` in the controller
`.env` and recreate the controller; startup recovery still removes surviving
root containers. Native login/inference and visual/callback acceptance remain
open; activation is not evidence that either provider is authenticated.
