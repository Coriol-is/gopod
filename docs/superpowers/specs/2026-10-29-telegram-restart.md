# Owner Telegram restart

Date: 2026-10-29. User-requested implementation. CP-031; ADR D046.

## Contract

`/restart` goes through `internal/control` with `PermOwnerOnly`. It is accepted
only as a message in the configured owner's private Telegram chat; the trusted
message ID is a replay fence, and its thread is the notification destination.
The command has no arbitrary process/container target or shell command argument.

The supported supervisor is the repository's Docker Compose `gopod` service.
Before accepting a request, Docker inspection must match the process hostname to
its container ID and Config.Hostname, the Compose service label, a running,
unpaused controller, an `always` or `unless-stopped` restart policy, and at least
10 seconds of uptime. This last condition is Docker's successful-start window for
[restart policies](https://docs.docker.com/engine/containers/start-containers-automatically/). Host binaries and unverified supervisors refuse the command.

The request and exact current personal container identities/scopes are committed
to SQLite before sending an acknowledgement. Only after acknowledgement and a
second durable `restarting` transition does the controller cancel its normal
shutdown context. Polling/queue/IPC drain normally; exact old personal container
IDs are then stopped. No volumes, host workspace, auth homes or native session
files are deleted. Process exit lets the verified Docker policy restart it.

On the next boot, while holding the controller instance lock and before ordinary
leftover cleanup, the same exact old IDs are stopped idempotently. A missing old
ID is already retired. Scope, labels and workspace bind source are revalidated;
failed validation prevents recycling and suppresses ordinary leftover cleanup.
The existing startup path restores provider/model choices, recreates and warms
the configured owner's personal runtime, and performs local CLI/auth checks.
After queue recovery and subsystem initialization, immediately before Telegram
polling starts, the explicit restart recovery notice runs. A success notice requires the matching startup observation
(container/CLI/local auth ready) and a current Docker running check. Otherwise it
reports degraded recovery. Neither wording claims model inference or quota
qualification.

## Boundaries and failure behavior

- This does not activate groups. A running registered group native runtime or
  another matching non-personal workspace makes the command refuse rather than
  silently restarting a profile that cannot be warmed by personal startup.
- Managed task reservations past spawn must be reconciled first. The request
  and recycling paths refuse while exit evidence is unresolved; no uncertain
  work is replayed or declared complete by restart.
- Requests are serialized; an active request and a one-minute cooldown reject
  another request. A persistent message high-water mark rejects old/redelivered
  command messages even after later successful restarts.
- Failed pre-shutdown phase writes receive bounded state-only abort repair, including
  an ambiguously committed transition. A later command repairs stale unstarted
  receipts after one minute; the persistent message replay fence remains intact.
- A notice timeout is unknown, not an acknowledgement. A failed/unknown initial
  send does not initiate shutdown. Recovery claims its notice durably before
  its single send, so an accepted send followed by a crash never loops. Unknown
  recovery notices are not automatically retried. The record remains available
  in `router_state` for diagnostics.
- Explicit restart notices do not depend on `GOPOD_NOTIFY_LIFECYCLE`. Generic
  lifecycle notices are suppressed for this restart to avoid duplicate or
  premature "back" claims. Both explicit notices target the original thread;
  a closed topic is not silently redirected to main.
- Shutdown uses existing queue drain behavior. It is not an emergency kill for
  a wedged controller, a host reboot, a Docker daemon restart or an image deploy.
  If draining cannot finish, host intervention is still needed.
- Default flags, schedules, provider choices and accounts do not change.

## Validation

Unit/integration tests use a real temporary SQLite store and fake Docker/API
operations. Cover permissions, source identity, supervisor policy/grace,
durable-before-notice ordering, unknown-send refusal, duplicate messages,
reopen recovery, one-shot recovery notices, exact-ID idempotence and workspace
mismatch refusal. Full build/vet/test and relevant race suites are required.
Production Compose restart and real Telegram/thread behavior remain a separate
live acceptance gate; this implementation task does not run a production restart.
