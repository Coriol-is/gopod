# Pi controller rollout and bounded backup cleanup — 2026-11-05

## Release

Owner authorized deployment on Pi only, no worker, then requested old-backup
inspection and a modest cleanup. Deployed `757faa22ec28c6e1f025d9943924637d2e2dc88b`
at 15:11:59 UTC, replacing `d58cdd2`. Exact-main
[CI 42050073211](https://github.com/Coriol-is/gopod/actions/runs/42050073211) passed.
PRs #173–#177 had independent review and required CI before integration.

Includes D065 code-inline alternatives, successful-final ownership, stopped-result
publication fence, D066 durable Stop/late completion evidence, and D067/D068 durable
task IPC claims with exact-byte receipt matching. Foreground producer remains
disconnected; managed tasks remain off; no topic deletion activation. No worker,
agent image, provider binary, credential, account or environment configuration change.

## Build, backup and state preservation

- Controller binary cross-built linux/arm64, CGO disabled, trimpath, version ldflag.
  Compiler/runtime reports Go 1.27.3 (previous deployed binary used Go 1.26.10).
- Binary SHA256 `d996e8692a586c8028790984f81a1e05721670e8e9cfe5b938b7143014b9b317`;
  verified against the running container.
- Image `sha256:3045f40a2f53dc846b9d71cb8394c358b063e247d324ca32b26758fa6fb45360`.
  Controller-only overlay on the prior exact runtime image; candidate passed
  isolated readiness with no network/credentials/host mounts, read-only filesystem
  and all capabilities dropped before production switch.
- All eight idle counters zero before stop, after stop and after rollout. No task
  or message inbox requests were pending. Owner runtime contained only sleep;
  graceful stop did not interrupt native/model work.
- Matched-stopped set `20261105T151122Z-ms-6ff01519` created and scratch-verified:
  database integrity OK, 46 tables, 9515 rows. No artifact/config files were in
  this set. Workspaces, provider auth/session homes, IPC and secrets remain outside
  this backup's guarantee and were preserved in place.
- Registered chats (2), sessions (39) and scheduled tasks (15) full-row hashes
  identical before/after rollout and after live IPC acceptance; integrity OK,
  session mapping unchanged, pending adoption zero.
- Controller healthy, restart count 0, OOM false; StartedAt
  `2026-11-05T15:11:59.617147645Z`. Owner runtime healthy and six unrelated services
  healthy. No group/relay/elevated runtime observed.
- Remote evidence: `/tmp/gopod-release-757faa2`. Existing untracked credential
  backup `.env.pre-elevated-20261103T105344Z` was left untouched.

## D068 instruction migration and bounded live acceptance

Inventory found three existing SCHEDULING.md files: owner, legacy chat workspace
and current protected group workspace. No separate task receipt scripts were found
in the bounded scan of workspace `.py/.js/.mjs/.sh` files; native/auth/session,
memory, dependency and Git directories were excluded. This is not an exhaustive
claim about arbitrary external consumers.

Before candidate startup, each guide received a leading D068 section that
supersedes status-only advice and contains the qualified producer/consumer example.
Original guide contents remain below it, byte-for-byte; original copies and
before/after hashes are retained in
`~/gopod-backups/consumer-migration-757faa2`. All three migrated hashes verified.
Seeding behavior remains non-overwriting; no claim is made that every external
consumer or already running model context adopted the new instructions.

The actual shipped Node consumer functions were run on Pi against the owner IPC
namespace using a unique read-only `list` request:

- initial request: hash-matched `ok`, 15 tasks;
- identical same-filename replay: hash-matched `ok`;
- changed same-filename `list` request: hash-matched `error` conflict;
- no task mutation, native/provider call or artificial crash; pending claim files
  zero after completion.

Receipt evidence is `receipt-acceptance.json` in the remote release directory.
This qualifies the deployed host and matching consumer path, not all legacy
status-only clients or a power-loss experiment.

Telegram CLI `/version`: command `3177168`, reply to retained test topic `3176309`;
correlated bot response `3177169` at 15:12:37 UTC:
`gopod 757faa2 (go1.27.3)`. Version/routing passed; no visual/native qualification
is inferred.

## Owner-authorized cleanup

`~/gopod-backups` used about 394 MiB after the new backup/migration. Deleted exactly
18 sealed automatic `database_only` sets after verifying current rollout health,
receipt acceptance and the retained new matched-stopped set. No symlink/hardlink
entries were admitted to deletion. Removed logical bytes: 124685085; allocated
file blocks: 124817408 bytes (about 119 MiB). Remaining usage: 275 MiB.

Retained all 20 matched-stopped sets, all named/manual/auth baselines, migration
originals and three database-only anchors:

- `20261029T191500Z-db-402ed5ad` (early foundation baseline);
- `20261031T001910Z-db-a42b1b89` (security-rollout baseline);
- `20261103T135647Z-db-3af03ade` (latest database-only set).

The exact planned/deleted set inventory is stored in
`~/gopod-backups/cleanup-20261105-757faa2.json`. No retention job was installed.
Separate `~/backups` is only 556 KiB (NanoClaw/OneCLI/systemd historical data) and
was retained. No Docker-wide prune, other-service cleanup or NAS operation.
Root disk remains about 85 GiB used / 27 GiB free; this was intentionally modest.

## Recovery and remaining boundaries

Prior image retained as `gopod-rollback:pre-757faa2`, image
`sha256:bdf20457739aaf06132b19e472513222e30a36632da3f439d60151159a5c4b22`.
Both newest matched-stopped backups and prior named rollback evidence remain.
No blind old-binary restart: new Stop, conditional delivery and claim ledgers need
compatible reconciliation. Drain claims or hold task IPC before downgrade;
complete IPC/spool restore remains unqualified. No live database restore occurred.

Resume CP-002 preview/control generation ownership and reply-action parity before
foreground cutover. CP-016 retirement and other native/client acceptance remain
separate. The D068 bounded acceptance above does not close those gates.
