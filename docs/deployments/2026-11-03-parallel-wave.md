# Parallel reliability rollout — 2026-11-03

## Reviewed scope

- CP-016 PR #163, `b62eb22`: topic admission/output fences; full quiescence and
  deletion command remain off. Independent review and exact-head CI passed.
- CP-002 PR #164, `e8b58b8`: result-aware recovery and holds, NoReplay propagation;
  foreground freeze activation remains off. Independent review and CI passed.
- Bounded acceptance fix `33ba30a`, no PR as owner authorized: truthful attachment
  refusal notices routed to the host-granted topic, no fallback/path disclosure
  for unknown grants. Independent review and full/race tests passed.

Combined source build/vet/full tests and queue/store/cmd/Telegram/IPC race suites
passed. No schema, provider/helper image or configuration change. Final main CI
and live rollout passed. Production is `e6f0fad`; `ec51ee6` is retained for rollback.

Sonar: PR #163 advisory ERROR, 4.6% duplicated new lines (test fixture blocks);
PR #164 OK. Main analysis remains stale at `4e3031a` (2026-10-26), security E.
No findings were suppressed.

## Predeploy acceptance

[Ordinary acceptance](../reviews/2026-11-03-acceptance-next.md): Codex nonce
continuity, bounded Stop/process exit and post-Stop resume passed. Test topic
`3176309` was created once by the newly implemented Telegram Toolkit command;
PDF delivered there and its contents verified. The preliminary missing-file
notice leaked into main; this release fixes that defect. Topic/PDF retained.

The rollout below completed exact main CI, idle and rollback preparation, a
verified matched-stopped backup, startup/state read-back and scoped Telegram checks.

## Candidate checkpoint

Candidate `e6f0fad` built CGO-free for linux/arm64 with Go 1.26.10. Source files
match the fully tested integration tree. Exact main CI:
[41654649071](https://github.com/Coriol-is/gopod/actions/runs/41654649071).
Pi isolated synthetic-account boot passed with no network/production mounts.
Candidate image `sha256:70d9f3ef7925d9946d90424ee048358c618abf039d5fa78f205ca56790426639`.
Rollback tag `gopod-gopod:rollback-pre-backlog-20261103-e6f0fad` retains `ec51ee6`.
This candidate was switched only after CI passed and the idle gate was rechecked.

## Deployment connection checkpoint

Exact main CI passed: test and isolation-spike. An intervening native/outbox
operation was allowed to finish; subsequent read-only counts were zero.
SSH then failed before executing the rollout script: the 1Password agent refused
the main SSH key signature and the server disconnected for authentication attempts.
No controller stop or production switch occurred. Production remains `ec51ee6`;
owner was asked to unlock the existing SSH agent. Credentials/config unchanged.

## Successful retry rollout

Owner requested retry; SSH access recovered. Idle gate reported all seven
counters zero. Matched-stopped backup `20261103T162832Z-ms-0c657f89` verified:
43 tables, 9,028 rows, four files including artifact blobs. Existing backups,
auth homes, workspace files and rollback tags retained. No credentials changed.

Controller started `e6f0fad` / Go 1.26.10 at 16:29:08 UTC, ready 16:29:14.
Image matches the candidate above. Exact registered-chat/session/scheduled-task
fingerprints matched; SQLite integrity OK. Initial running state: zero restarts,
OOM false, no warning/error startup logs; six external service healthchecks healthy.
Final scoped Telegram regression passed: one missing-file request `3176367`
in test topic `3176309`; host warning `3176369` stayed there and used truthful
source_missing wording. No notice appeared in main within the bounded probe
history. Native turn 263 finished once, with no unfinished turns. `/version`
3176371 → 3176372 returned `gopod e6f0fad (go1.26.10)`.
At 16:30:49 UTC (>100 seconds after start): running, zero restarts, OOM false,
all seven idle counters zero. No warning/error startup entries. Existing test
PDF and topic retained; no auth/provider/config changes.

Rollback image/tag and verified backup above remain available. No schema or
new freeze activation occurred. Use the same idle gate before switching back
to retained `ec51ee6`; do not erase results or restore over live work blindly.
The rollout and bounded live regression are complete.
