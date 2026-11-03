# Container health and readiness

Docker health is local operational evidence, not proof of provider availability,
authentication, quota, task success, isolation, or Telegram delivery. Checks are
read-only and bounded; they never run inference, refresh credentials, write probe
files or read credential content. Docker records status but does not restart an
unhealthy container automatically. Existing restart/lifecycle authority is unchanged.

## Health matrix

| Container/image | Check | Ready means | Not covered |
|---|---|---|---|
| Controller (`Dockerfile`, compose service `gopod`) | `gopod healthcheck`, loopback `/healthz` | Startup/recovery finished, signal not received, configured core loops have not exited, local SQLite state read completes within one second, started outbox dispatcher remains running | Remote APIs, auth, queue progress, undeliverable outbox items, provider quality |
| Personal Claude/Codex (`container/personal/*`; legacy `container/Dockerfile*`) | Offline shell prerequisites | Shell, sleep, Node and provider executable are available; chat workspace is traversable/readable | CLI execution, auth, writes, model availability, per-tool dependencies |
| Group Claude/Codex (`container/group/*`) | Same, inference workspace and mounted CA file readable | Local inference runtime prerequisites exist under nonroot/read-only profile | Credential content, relay/network/provider acceptance |
| Elevated Claude/Codex (`container/elevated/*`) | Same, session workspace and exec supervisor readable | Local executable/workspace prerequisites | Host safety/authority, Docker socket functionality, provider/auth; never inspects host PID 1 |
| Elevated supervisor test fixture | Shell/Node/supervisor readable | Credential-free test supervisor prerequisites | Provider CLI or production qualification |
| Group inference relay (controller image, overridden entrypoint) | `gopod-group-relay healthcheck` on loopback port 8787 | Real serving loop responds after config/policy setup | Upstream/provider/controller lease availability; probe bypasses neither auth nor normal request gates |
| Group tool sandbox (`Dockerfile.tools`) | **No periodic health process**; host validates image/policy, PID cgroup before exec, bounded exec completion and snapshot/cleanup | Per-invocation preflight and observed exit code | Long-running service readiness; an extra periodic process would consume the strict eight-PID budget and race pause/snapshot |
| `gopod-group-admin`, `gopod-group-catalog`, `gopod-group-canary`, backup CLI | One-shot command exit and command-specific output validation | Requested local operation completed with exit 0 | No persistent daemon; when invoking via `docker run`, use `--no-healthcheck` to suppress inherited controller health |

Controller image sets `GOPOD_HEALTH_ADDR=127.0.0.1:9878`. The endpoint is never
published by compose and rejects non-loopback bind addresses. Standalone binary
serving is opt-in via that variable; the CLI defaults to the same local address.
The CLI exits 0 only for the exact bounded readiness response and 1 otherwise;
it does not load `.env`, config, migrations or SQLite. The endpoint dispatches
through `internal/control`. Startup is unhealthy until recovery finishes; Docker
allows a 120-second start period, then requires three failures at 30-second
intervals to mark unhealthy. Probe timeout is three seconds (client deadline two).
Quarantine intentionally offers no readiness endpoint: it is not a serving bot.
Shutdown becomes unready on signal, before graceful subsystem draining.

Core-loop exit is latched even if it happened before startup completed (Telegram,
IPC, scheduler, memory API and generation maintenance when configured). A live
endpoint and store read do not prove every goroutine is making progress. Outbox
ownership/start failure is unhealthy when its dispatcher was configured. No health
failure itself stops containers or changes task/recovery state.

Runtime image checks use Docker's health exec with a 30-second interval,
three-second timeout, ten-second start period and three failures. They execute
only shell builtins; stat/permission checks are honest prerequisites, not CLI
self-tests. No `--version`, auth command, network request or writable sentinel is
used. Read-only roots and overridden UIDs are supported. Agent health may be
spoofed by an agent with authority to change its own binaries; this is not a
security attestation. A stopped container is stopped, not ready regardless of
its last health observation.

The relay uses its existing HTTP serving loop, accepts health requests only from
loopback, and returns no configuration, capability or credential data. Native
clients cannot use that route to probe or invoke an upstream. Runtime creation
explicitly overrides the controller image's inherited health command for relay
containers. No additional host port or mount is introduced.

## Rollout and diagnostics

1. Build/pin the controller and each enabled native image. Health definitions are
   image configuration: existing containers do not acquire them retroactively.
   Preserve rollback images and follow the normal drain/recreate lifecycle; do not
   force-stop owner work just to attach a check. Image-ID reconciliation will pick
   up rebuilt agent images on the next safe recreation.
2. Before rollout record image IDs, container IDs, running/restart/OOM state,
   current health (including missing checks), registrations/sessions and unfinished
   work. Preserve the verified backup and existing auth homes.
3. After normal startup inspect only bounded metadata:
   `docker inspect --format '{{.Name}} {{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}no-healthcheck{{end}}' <exact-container>`.
   Use the same exact-container health metadata for controller, owner/group,
   relay and any active elevated runtime. Do not create/login/infer merely to
   populate the matrix. Record absent optional runtimes as not exercised.
4. For controller failures run `docker exec <controller> gopod healthcheck`;
   exit 1 is intentionally generic. Inspect existing bounded controller logs to
   distinguish startup/quarantine, store failure or serving-loop exit. Never
   print environment/credential files. For native failures inspect executable
   and workspace availability under the configured UID; do not run auth refresh.
5. One-shot tools use explicit `--no-healthcheck` and their observed exit status.
   Do not wait for a periodic daemon health status on a completed command.

## Reboot qualification (separate owner authorization required)

No host reboot is part of implementation/local qualification. Before a future
approved reboot, capture the matrix and state above, verify backup, confirm idle
and coordinate owner work. After reboot, inspect exact image IDs, restart/OOM and
health states after start periods; controller must become healthy and registered
runtime state must remain intact. Native/relay containers created on demand are
qualified only once the owner-approved workflow creates them. Record successful
startup separately from auth/inference/Telegram acceptance. Any missing or stale
check is an open rollout gate, not a reason to delete session data.

## Local test procedure and boundaries

`python3 scripts/test-container-health.py` builds fresh controller/relay binaries
and disposable images from explicitly existing local fixtures; it never pulls an
image. Every container uses `network=none`, read-only root, dropped capabilities,
no-new-privileges and no host mounts/sockets/credentials. Controller uses disposable
tmpfs data. Native scenarios cover personal/group/elevated prerequisite modes,
missing workspace failure and supervisor mode. Elevated checks are exercised as
UID 0 **without host privilege or host namespaces**. Group CA fixture is empty,
not a real trust/auth acceptance. Positive relay serving-route behavior is covered
by in-process tests; missing relay config is tested as exit 1. The fixtures do not
prove fresh maintained-image builds or production/reboot behavior.

### Image roles for the current Pi rollout (inventory supplied 2026-11-03)

This is a rollout plan, not evidence these images were changed. Pin the inspected
full image IDs before building; the short IDs below are only identification notes.
Preserve every existing USER, WORKDIR, ENTRYPOINT, CMD, environment and label unless
explicitly listed. In particular COPY health scripts does not require changing
USER to root: image COPY already creates readable root-owned files.

| Existing alias / short ID | Required new content/config | Preserve runtime identity |
|---|---|---|
| Controller service image | New `gopod` binary, `ENV GOPOD_HEALTH_ADDR=127.0.0.1:9878`, controller HEALTHCHECK | Compose UID/GID, mounts, ports and entrypoint |
| `gopod-agent:latest` / `034f051a21e2` | Health shell, `claude /workspace/chat` check | Existing USER/WORKDIR and personal spawn UID |
| `gopod-agent-codex:latest` / `7e441e88fba1` | Health shell, `codex /workspace/chat` check | Same |
| `gopod-agent-elevated:latest` / `78399afd77a0` | Health shell, `claude /workspace/session` check | Existing root USER, supervisor, host-authority profile |
| `gopod-agent-elevated-codex:latest` / `bc5d59506ef0` | Health shell, `codex /workspace/session` check | Same |
| `gopod-agent-group:cp020-v2` | Health shell, `claude /workspace/inference` check | Nonroot USER, readonly group runtime and mounts |
| `gopod-agent-group-codex:cp020-v2` | Health shell, `codex /workspace/inference` check | Same |
| `gopod-group-relay:cp020-observer` / `2422f46ad26f` | **New `gopod-group-relay` binary**, relay HEALTHCHECK | Existing relay config mounts/permissions, entrypoint, USER; manager also supplies the relay health override |
| `gopod-group-tools:cp020` / `5ab09eeacbb0` | `HEALTHCHECK NONE` (explicit one-shot contract) | Tool USER, eight-PID budget, no network/credentials |

A bounded native overlay consists only of:

```dockerfile
FROM <pinned-existing-image>
COPY --chmod=0555 container/healthcheck.sh /usr/local/bin/gopod-runtime-healthcheck
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD ["/bin/sh", "/usr/local/bin/gopod-runtime-healthcheck", "<claude-or-codex>", "<workspace-from-matrix>"]
```

For an existing relay image, copy the freshly built matching-architecture
`gopod-group-relay` binary to `/usr/local/bin/gopod-group-relay` and use
`HEALTHCHECK ... CMD ["/usr/local/bin/gopod-group-relay", "healthcheck"]` with
those same timing settings. A new manager plus an old relay binary would fail
its check; overlaying only the controller image is insufficient. Preserve old
aliases under rollback tags before repointing any production alias. Perform
safe image/container reconciliation through the existing lifecycle; no health
probe is permission to recycle an active container.
