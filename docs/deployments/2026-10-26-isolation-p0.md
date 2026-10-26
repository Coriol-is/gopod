# CP-020 P0 deployment evidence

Source: c82399bb22eb3d0bc4ff74fdf48215fbf6f9f3ae (PR #61).
Date: 2026-10-26. Target: existing RPi4 controller, controlled group sandbox.

## Reviewed payload

P0 permits agent execution only for the configured positive Telegram owner ID,
with a unique consistent live registration. Foreground, fresh/system, recovery,
auth, startup, provider switch and management boundaries fail closed for other
profiles, regardless of caller tier or a folder named owner. Pending non-owner
IPC is preserved without dispatch or retention mutation. Restricted builders
exclude personal vault, shared skills, legacy extras and global credential env.
IPC channel enumeration uses rooted no-follow directory handles.

New restricted CLAUDE.md/AGENTS.md templates have matching public-space privacy
rules. Existing files are preserved. These templates are dormant; groups remain
unavailable until later identity/filesystem/credential/network qualification.
No group profile, account-service isolation, migration or re-enablement is claimed.

## Validation before activation

- Independent review approved exact feature SHA 25b15b59eca4e39dd150e9f3131a1797e34d700f.
- `go test ./...`, `go vet ./...`, runner/queue/Telegram/IPC race tests passed.
- PR CI test, native CLI isolation spike and SonarCloud checks all succeeded.
- Host inventory: no gopod agent containers, 97 completed turns, zero unfinished
  turns. No legacy process needed draining or stopping.
- Online SQLite backup: `/home/spaceinvaderz/gopod-backups/pre-p0-20261026T210143Z.sqlite`,
  permissions 0600, integrity check `ok`. This DB-only snapshot is not a matched
  filesystem/auth recovery guarantee (CP-007 remains planned).
- Three scheduled tasks: metadata SHA256
  `1d5efe1e46d7a634651b2e0a3a703cfef96b86b63b0a6a1bde4b9f254f708a34`.
- Previous controller image retained as `gopod-gopod:rollback-pre-p0`, image
  `sha256:e8693d45a874db0c7a066875e333e67100e60fef7c59ccbc038099377b8833d9`.

## Rollout and live observations

- Built on RPi with `docker compose build --build-arg VERSION=c82399b gopod`;
  activated using `docker compose up -d --no-build gopod`.
- Controller image: `sha256:b8b578e8dde2b822f81bb05059f56a9805a65405e6e32cf0bc313010e9e01b45`.
- Container started at `2026-10-26T21:03:49.73481955Z`, running with restart count 0.
  Startup logs report `version=c82399b`; scheduler and IPC started.
- Startup created only `gopod-owner`; no group agent container is running.
  Owner self-check: Claude 2.1.310, container/CLI ready, auth authenticated.
  Claude management quota is unknown/unsupported and inference remains unverified.
  Controller `OOMKilled=false`; no model or login request was made by this rollout.
- Post-activation SQLite integrity is `ok`; 97 turns remain completed, no
  unfinished turn was replayed. All three scheduled-task metadata rows retain
  the exact predeploy hash above. No schedule was created, paused or edited.

## Remaining acceptance and rollback boundary

Unit/fake-Docker tests prove denial across entry paths and continued personal
behavior, not an authenticated group-model round trip. No model request, account
login, personal-service access, test Telegram publication or schedule edit is
part of this deployment. Standard controller startup may make its existing
management observation and lifecycle notification.

Later CP-020 PRs implement namespaces, two Dockerfile/profile sets, enforced
credential/network boundaries and clean-epoch migration. Targeted checks run
with each slice in owner-controlled sandboxes; full cross-boundary acceptance
precedes real groups. A rollback must keep non-owner agent admission closed;
restoring the old permissive image alone is not a safe rollback. Preserve DB,
files and retired evidence; never replace the live DB with an old snapshot to
roll back code casually.
