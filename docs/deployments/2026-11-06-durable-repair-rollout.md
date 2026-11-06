# Durable repair outcome rollout — 2026-11-06

## Release

Pi controller deployed `a85b328d1d98aa28c4757e30d43cf8abe3a50891` at12:26:54 UTC,
replacing `0cd8078`; no worker/agent-image/provider/environment changes. #189/D069
adds immutable repair attempts before HTTP, durable uncertainty barriers across
restart/rebind/retention, exact receipt-backed DB-only settlement and preclaim
outbox holds without attempt burn. Source review, full tests/races and required PR
CI PASS; exact-main [CI42217121239](https://github.com/Coriol-is/gopod/actions/runs/42217121239)
PASS. [Review](../reviews/2026-11-06-durable-repair-outcomes.md).
Sonar OK on reviewed e329bf2,30 smells retained, zero bugs/vulnerabilities.
No foreground, managed-task, parallel-session or topic-delete activation.

## Build, backup and state

CGO-disabled linux/arm64 Go1.27.3 binary SHA256
`882b3375688b0c5fd71195d42f598468d7f4f94b094afb767219313493c527ce` matches running
container. Controller-only overlay on exact prior image passed isolated readiness
without network, credentials or host mounts, with read-only root and dropped caps.
Before stop all eight old idle counters zero and native owner only sleep. After
startup all nine counters zero, including `control_repairs_pending`.

Pre-rollout matched-stopped backup `20261106T122616Z-ms-a8ac2967` verified in scratch:
51 tables,9816 rows. Post-migration online database-only backup
`20261106T122805Z-db-5ea83652` also scratch verified:52 tables,9847 rows. The second
set verifies new-table allowlist/schema compatibility; it does not replace the
matched-stopped artifact coverage. Full workspace/auth/session/IPC recovery remains
unqualified. Neither backup was restored over live data.

Registrations2, sessions39 and tasks15 full-row hashes unchanged before/after;
integrity OK, mapping unchanged, pending adoption0. Controller healthy, restarts0,
OOMfalse; StartedAt `2026-11-06T12:26:54.754318224Z`. Owner and six unrelated services
healthy. Read-only inspection confirms new ledger exists with0 attempts; no unknown
remote effect was deliberately injected into production.

Rollback `gopod-rollback:pre-a85b328` and all named prior backups/images retained.
Do not run an older binary across pending D069 attempts: it cannot enforce the
barrier. Preserve full DB/manifest and held dispatch; no TTL or force-clear exists.
Remote evidence `/tmp/gopod-release-a85b328`.

## Telegram and housekeeping

Telegram `Nanoclaw_go_bot` test topic3176309: `/version`3177406, reply3177407 at
12:27:49 UTC `gopod a85b328 (go1.27.3)`. Restart notices3177404/3177405 observed.
This proves release/routing, not forced crash, visual/native or foreground acceptance.

Proved PR189 exact-file equivalence to squash commit and ancestry in main before
removing its clean agent worktree and local/remote `feat/durable-repair-outcomes`.
Canonical/session worktrees retained; current docs PR188 branch cleanup is planned
after merge. Disposable readiness container and candidate alias removed; live and
rollback images retained. Backup directory306MiB after both new sets; no backup
pruning, NAS, broad Docker cleanup or unrelated-service changes. Existing untracked
credential backup untouched.

## Remaining gates

D069 holds unsupported unknowns; it does not resolve them or reconstruct historical
pre-journal HTTP. Wider non-repair edits, explicit reconciliation, eager old-row
cleanup, mixed legacy/durable ordering and integrated preview/Stop/publication
qualification remain. CP-016 nontext/history/managed/native and retirement remain.
Backlog/handoff/roadmap and proposal checkpoints reconcile this boundary.
