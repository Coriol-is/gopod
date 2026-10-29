# CP-007 — Scheduled store backups on the production host

| | |
|---|---|
| Status | in-progress |
| Date | 2026-10-05 |
| Origin | feature survey of `rzaytsev/codex-agent` (2026-10-05) and the 2026-10-03 public-release audit |
| Size | M |
| Touches | `deploy/` (new: systemd unit + timer + script) or `docker-compose.yml` sidecar, `README.md` production section |
| Depends on | — |
| Plan | [Implementation slices](../superpowers/plans/2026-10-26-reliability-operations.md) |
| Spec | [Design](../superpowers/specs/2026-10-26-matched-store-backups.md) |

## Problem
`data/store.sqlite` (chats, messages, turns, memory, vectors) and `data/sessions/` (Claude and Codex session state, auth) exist in one copy on the Pi. Several manual online database snapshots now exist (see dated deployment reports), but no scheduled backup/retention or restore drill is established by repository evidence.

## Proposal
Separate database-only SQLite API snapshots from full matched backups. The first full mode
uses a host-only admission fence and verified stopped writers, then private manifest-based
DB/workspace/session/auth capture. Offline validation and startup quarantine precede any
activation. Timer stays off until a restore drill and accepted maintenance window are qualified.

## Out of scope
Automatic cloud uploads, image/repository byte copies, live credential use and automatic
production restore. Compose healthchecks are separate from disaster recovery acceptance.

## Risks and acceptance
Live tar plus SQLite backup is not a matched cut. Tests must prove writer exclusion, protected
permissions, complete manifest bytes, bounded retention and no effects/poller/auth during scratch
restore. CP-003 adds a joint artifact-manifest rollout gate without blocking baseline backups.

## 2026-10-25 reconciliation
Manual SQLite backups before deployments do not close this proposal. Qualify
session/workspace coverage, protected backup permissions and a scratch restore
before treating it as automated disaster recovery. Do not print or commit auth
files. Off-host transport and a healthcheck are distinct acceptance claims.

## 2026-10-26 adoption design

The linked spec now defines this candidate in the [adoption map](2026-10-26-codex-agent-adoption.md),
wave **1 support**. Status remains proposed; a saved design does not approve
implementation or change the existing delivery queue. New acceptance/migration
requirements in the dated spec supersede conflicting early shorthand here.

## 2026-10-29 implementation, PR 1 (slices 4-6)

Delivered on branch `feat/cp007-backups` ([D043](../DECISIONS.md), [runbook](../deployments/BACKUP-RESTORE.md)):
store-owned online snapshot (`internal/store/backup*.go`), sealed backup sets with atomic publication and offline
verify (`internal/backup`), controller-lock fence, settlement check and restore latch (`internal/maintenance`),
`gopod backup create|verify|list|idle|quarantine`, and the quarantine start mode. Still open: slice 7 (opt-in
timer, bounded retention), capture of ChatsDir/provider session and auth homes/IPC files (so `matched_stopped` is not
yet a full matched recovery), restore epoch invalidation of transient approvals, an activation/restore command,
online artifact leases (needed only for an online matched mode), a Pi restore drill and qualification.
