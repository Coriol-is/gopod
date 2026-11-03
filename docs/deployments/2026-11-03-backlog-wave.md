# Backlog wave rollout — 2026-11-03

Owner authorized review, merge and deploy, then small fixes without PRs.
Target `4350e1b` includes picker fix #159, atomic ordinary batch attempts #160,
and durable topic-outbox fences #161. All three PRs and exact target main CI
passed; independent combined deployment review and targeted race checks passed.
No schema, feature flag, provider image or group helper change is required.

## Preflight checkpoint

Pi initially ran image `49b0888029a6…`, source `1a3b9e1`, restart count zero and
OOM false. The owner's ROOT Codex session was idle (only service `sleep`). The
ordinary idle gate reported zero pending/running turns, in-flight/unresolved
deliveries, active artifact reservations and held/executing task attempts.

The CGO-free linux/arm64 controller was built locally with Go 1.26.10 and
link-time version `4350e1b`, retaining unchanged helper binaries and runtime base.
The candidate reached readiness in an isolated synthetic-account boot probe on
Pi with no network or production mounts. Online backup
`20261103T135647Z-db-3af03ade` verified: 43 tables / 8,793 rows.

Rollback image: `gopod-gopod:rollback-pre-backlog-20261103-4350e1b`.
Matched-stopped backup `20261103T135805Z-ms-f228921f` verified: 43 tables /
8,798 rows. Controller started at 13:58:41 UTC and reached readiness at
13:58:48. Image `sha256:b89bf9fefdab296f9308f36f664c140c588c841665a0a3e63f75cd15bc4a928a`.
Read-back after one minute: running, zero restarts, no OOM or warning/error logs;
six external service healthchecks healthy. SQLite integrity and exact registered
chat/session/scheduled-task fingerprints preserved. Auth homes/workspaces and
existing backups retained (auth homes/workspaces are outside backup coverage).
The idle ROOT session closed through normal controller shutdown.

## Telegram acceptance

- `/silent` 3176274 → 3176275: on/off round trip retained the same message and
  buttons with the checked value updated; restored off.
- `/model` 3176276 → 3176277: selecting current default retained the picker.
- `/root status` 3176278 → 3176279: off; ordinary conversation active.
- `/sessions status` 3176280 → 3176281: legacy, no adoption, no durable sessions.

These are actual Telegram callback/edit proofs, not a visual rendering or
provider-inference qualification. CP-002 freeze/publication and CP-016 complete
quiescence/deletion remain open.

## Small version follow-up — preparing

The owner requested subsequent small fixes without PRs. `/version` now receives
the application's link-time identity, retaining VCS/dev fallback for standalone
router callers and removing duplicated `go` in the response. Independent review
passed; full local build/vet/tests passed. Exact main CI must pass before rollout.
