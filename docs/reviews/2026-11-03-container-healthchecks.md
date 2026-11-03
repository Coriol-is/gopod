# Container healthchecks — implementation checkpoints

## Scope and ownership

Owner authorized implementation in the parallel CP-002/CP-016/health wave. This
stream owns image health definitions, local controller/relay probes, and the
health matrix/runbook. No production changes, credentials, inference, provider
refresh, deployment or host reboot. Root owns global HANDOFF/ROADMAP integration.

## Checkpoint 1 — contracts and implementation

Controller readiness is a responsive loopback endpoint after startup, a bounded
store read, a signal fence and a latched failure when a configured core serving
loop exits. Docker invokes a separate CLI probe that opens no database/config.
Quarantine does not expose readiness. Native containers expose offline executable
and workspace prerequisites only; health does not assert auth or inference.
Relay checks its serving loop locally without controller/upstream requests.
Short-lived group tools use existing pre-exec isolation checks and observed exit;
periodic health processes would consume the strict 8-PID budget and race pause.
All current maintained and legacy image recipes are inventoried in the runbook.

Docker is available locally (29.8.2, arm64). Planned disposable runtime checks
use local images, no credentials/host sockets/workspaces and network=none.
Pending: regression tests, full build/vet/tests, affected race suites, Docker
runtime evidence, independent review and exact CI. No deployment/reboot claim.

## Checkpoint 2 — local qualification

Initial full `go build ./...`, `go vet ./...`, `go test ./...` and affected race
suites (control/controller/relay/group runtime) passed. Offline Docker matrix
passed using local arm64 fixtures: controller healthy; quarantine unhealthy;
personal/group/elevated modes for both providers healthy; missing-workspace
negative controls unhealthy for both; supervisor healthy; relay with absent
config exited 1. Every fixture was read-only, network-none, capability-dropped,
with no host mounts or credentials. Disposable resources were removed by their
unique names. These were overlays on installed fixtures, not rebuilt upstream
npm images and not a privileged/elevated host test.

Added explicit local outbox lifecycle check after that initial suite to avoid
reporting a configured dispatcher as ready after stop/start refusal. Final suites
will include this final delta. The runbook records the Pi image-alias matrix and
relay-binary overlay requirement; existing USER/config/rollback state must remain.

## Checkpoint 3 — final source validation

Final source build/vet/full tests passed after the outbox lifecycle delta.
A real dispatcher test now probes before Start, during running and after Stop
while a second goroutine reads health; targeted race check passed. Affected full
race suites include Telegram in addition to control/controller/relay/group runtime.
The [health matrix and rollout/reboot runbook](../CONTAINER-HEALTH.md) explicitly
separate stat-only native prerequisites, controller service readiness, one-shot
completion, and unperformed production/reboot qualification. Root's independent
source review reported PASS pending final checks. Parent owns PR/CI integration.

Final affected race suites passed, including Telegram; the real dispatcher
concurrent-read lifecycle regression also passed independently under `-race`.
Shell syntax, Python compilation and diff whitespace checks passed. No production
changes or host reboot occurred. Runtime fixtures intentionally covered local
prerequisites, not real provider logins or fully rebuilt native release images.
