# CP-007 — Scheduled store backups on the production host

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-05 |
| Origin | feature survey of `rzaytsev/codex-agent` (2026-10-05) and the 2026-10-03 public-release audit |
| Size | S |
| Touches | `deploy/` (new: systemd unit + timer + script) or `docker-compose.yml` sidecar, `README.md` production section |
| Depends on | — |
| Spec | `—` |

## Problem
`data/store.sqlite` (chats, messages, turns, memory, vectors) and `data/sessions/` (Claude and Codex session state, auth) exist in one copy on the Pi. The only backup is the one-off copy made by hand before the 2026-10-03 history rewrite.

## Proposal
A `deploy/backup.sh` that takes an online SQLite snapshot (`sqlite3 .backup` or the Python fallback already used in ops) plus a tar of `sessions/` and `chats/`, rotated daily/weekly, run by a systemd timer; README documents restore. `restic` optional for off-host copies. `/healthz` from observability O2 is wired into `docker-compose.yml` as a healthcheck in the same change.

## Out of scope
Backing up the agent images or the repo. Encryption at rest.

## Risks
A snapshot taken mid-WAL-checkpoint must use the SQLite backup API, not `cp`; auth files are secrets and the backup location must be `0700`.

## Acceptance
- Timer fires nightly; `backup.sh` produces a restorable snapshot; a restore drill on a scratch directory opens with `gopod` and lists the same message count.
- `docker compose ps` shows `healthy` for the gopod service.
