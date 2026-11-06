# Pi reliability rollout and development cycle — 2026-11-06

Owner authorized deploy, backlog update and housekeeping as the permanent development
cycle. CLAUDE.md records review/CI/merge → Pi controller deploy/acceptance → documentation
and backlog → bounded housekeeping. No repeated deploy request is required for future
completed implementation batches unless the owner narrows scope.

## Deployment

Deployed `80f4b11cc5077cde1df28afb0274b5f79b7bb7ba` at 10:25:02 UTC, replacing
`757faa2`; controller only, no worker. Includes #178/#179 generation controls,
#180 durable keyboard fallback, #182 noncontrol notice ownership and #183 durable
reply-action readthrough. All source PRs independently reviewed; exact-main
[CI 42198643997](https://github.com/Coriol-is/gopod/actions/runs/42198643997) PASS.
Sonar source-PR results remain in review reports: #180 advisory duplication ERROR;
#182/#183 OK with two/six code smells. No findings suppressed.

Cross-built linux/arm64 CGO-disabled Go 1.27.3 binary, SHA256
`3d6f738c24bcff6c274bb2f550e2968d2087eb3916d4f55b2b83bbcc0d9f06d9`, matches running
container. Controller-only overlay on exact prior runtime; isolated readiness passed
without network, credentials or host mounts, with read-only filesystem and dropped
capabilities. Agent image/provider binaries/environment unchanged.

All eight idle counters zero before stop, stopped and after rollout; native owner
runtime contained only sleep. Matched-stopped backup `20261106T102424Z-ms-44bebb93`
scratch verification PASS, 50 tables / 9705 rows. Backup limitations remain: provider
auth/session homes, workspace and IPC are not a complete matched recovery set.
Preserved rollback `gopod-rollback:pre-80f4b11` and all prior named rollback evidence.
Do not blindly downgrade across keyboard fallback/ownership ledgers.

Health PASS, restarts 0, OOM false, StartedAt `2026-11-06T10:25:02.697881071Z`.
Registered chats (2), sessions (39), tasks (15) full-row hashes unchanged; integrity
OK, mapping unchanged, pending adoption zero. Owner runtime and six unrelated
services healthy. Remote evidence: `/tmp/gopod-release-80f4b11`.

Telegram `Nanoclaw_go_bot`: test-topic command 3177320 (reply to 3176309), correlated
response 3177321 at 10:25:43 UTC: `gopod 80f4b11 (go1.27.3)`. Restart notices were
observed at 3177313/3177317. This verifies release/routing, not complete visual/native
or durable foreground acceptance. Foreground producer disconnected, managed tasks
off, deletion command unavailable.

## Housekeeping

Proved exact PR-file equivalence between heads and squash commits, and ancestry in
main, for #178–#183. Removed two clean agent worktrees and six integrated local and
remote branches: preview-generation-controls, wait-control-generation,
outbox-keyboard-fallback, parallel-workstreams, topic-retirement-next, reply-actions-ack.
Canonical and session worktrees retained. The current cycle docs branch is removed
after merge. Removed only the disposable isolated check container and candidate
image alias; live image and rollback tags retained.

Backup directory 283 MiB after new set; prior backup cleanup remains in force.
No additional backup deletion, broad Docker prune, NAS operation or unrelated-service
cleanup. Existing untracked credential backup remained untouched.

## Remaining work

CP-002 control-repair arbitration, More/Back checkpoints, eager row cleanup,
mixed-path ordering and integrated foreground qualification. CP-016 historical/
nontext ownership, unknown outcomes, managed/native settlement and retirement.
Backlog priority and owner deferrals unchanged. Completed slices archived; deployment
does not mark either parent proposal complete.
