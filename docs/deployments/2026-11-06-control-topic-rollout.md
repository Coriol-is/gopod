# Control arbitration and topic ownership rollout — 2026-11-06

## Release and qualification

Deployed `0cd807858f1c355631fb38ccc239f02d739445d8` on Pi controller only at
11:34:37 UTC, replacing `80f4b11`. No worker, agent image, credential, environment,
provider or feature-activation changes. #186/#187 had independent exact-head
review, full build/vet/tests, affected races and required PR CI PASS. Exact-main
[CI 42210762626](https://github.com/Coriol-is/gopod/actions/runs/42210762626) PASS.
Sonar OK: five complexity smells #186, nine smells #187; read, none suppressed.
See [topic review](../reviews/2026-11-06-topic-ownership-next.md) and
[control review](../reviews/2026-11-06-control-final-arbitration.md).

#186 journals registered-private topic-target text with exact source/target
validation and receipt-only repair/no-replay. #187 serializes local message edits,
protects published final targets from later legacy writes, checkpoints old controls
for More/Back, prevents held-row starvation and obsolete Stop requeue. Neither
local ordering nor completion of these slices proves unknown remote repair effects
settled. Foreground producer stays disconnected; `/thread delete` unavailable;
managed task activation and parallel sessions remain deferred.

## Build and preservation

CGO-disabled linux/arm64 Go 1.27.3 controller binary SHA256:
`495f067d916faa315e9cb7b2faf3a517b32b20cebf40adda12aa49d6cde64f69`.
Running binary hash verified. Controller-only overlay on prior exact image;
isolated readiness PASS with no network/credentials/host mounts, read-only root
and dropped capabilities. All eight idle counters zero before stop, stopped and
after rollout; owner native runtime contained only sleep before drain.

Matched-stopped backup `20261106T113359Z-ms-d860fe6a` scratch verified: 51 tables,
9756 rows, integrity OK. Workspace/provider auth/session homes and IPC remain outside
complete matched recovery guarantees; preserved in place. Registered chats (2),
sessions (39) and scheduled tasks (15) full-row hashes identical before/after;
mapping unchanged, pending adoption zero. Controller healthy, restarts0, OOMfalse;
StartedAt `2026-11-06T11:34:37.852168587Z`. Owner and six unrelated services healthy.
Rollback `gopod-rollback:pre-0cd8078` retained, plus all earlier named evidence.
No blind downgrade across existing ownership/outbox ledgers.
Remote evidence `/tmp/gopod-release-0cd8078`.

## Live bounded acceptance

Telegram `Nanoclaw_go_bot`: `/version` command 3177357 in retained topic3176309,
reply3177358 at11:34:58 UTC: `gopod 0cd8078 (go1.27.3)`. Restart notices3177355/3177356.
Read-only journal check found an acknowledged same-source/target topic3176309 row
with Bot API message1492. An initial equality check using CLI message3177358 found
no row; CLI/MTProto and Bot API IDs are different namespaces, so no direct equality
or stronger cross-namespace correlation is asserted. Final evidence records both.
This is release/routing and bounded topic-journal observation, not visual/native
qualification, crash injection, deletion or foreground acceptance.

## Housekeeping and next action

Exact PR-file equivalence to squash commits and ancestry in main verified before
removing two clean agent worktrees and local/remote `fix/control-final-arbitration`
and `feat/topic-ownership-next` branches. Canonical/session worktrees retained;
cycle docs branch removed after merge. Removed only disposable check container
and candidate alias; live/rollback images retained. Backup root290MiB after new
set, no backup deletion, broad Docker prune, NAS or unrelated-service mutation.
Untracked credential backup untouched.

Backlog/handoff/roadmap/proposal checkpoints and completed ledger reconciled.
Next CP-002: durable unknown repair settlement, eager old-row cleanup, mixed-path
ordering and integrated preview/Stop/publication qualification. CP-016: nontext/edit
attempts, historical ownership/retention, unknown reconciliation, managed/native
settlement and confirmed retirement orchestration. Neither parent is complete.
