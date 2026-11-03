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
