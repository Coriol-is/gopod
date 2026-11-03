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
and live rollout are pending. Production currently remains `ec51ee6`.

Sonar: PR #163 advisory ERROR, 4.6% duplicated new lines (test fixture blocks);
PR #164 OK. Main analysis remains stale at `4e3031a` (2026-10-26), security E.
No findings were suppressed.

## Predeploy acceptance

[Ordinary acceptance](../reviews/2026-11-03-acceptance-next.md): Codex nonce
continuity, bounded Stop/process exit and post-Stop resume passed. Test topic
`3176309` was created once by the newly implemented Telegram Toolkit command;
PDF delivered there and its contents verified. The preliminary missing-file
notice leaked into main; this release fixes that defect. Topic/PDF retained.

Next: exact main CI, idle and retained rollback image, verified matched-stopped
backup, startup/state read-back, scoped missing-file warning and `/version`.

## Candidate checkpoint

Candidate `e6f0fad` built CGO-free for linux/arm64 with Go 1.26.10. Source files
match the fully tested integration tree. Exact main CI:
[41654649071](https://github.com/Coriol-is/gopod/actions/runs/41654649071).
Pi isolated synthetic-account boot passed with no network/production mounts.
Candidate image `sha256:70d9f3ef7925d9946d90424ee048358c618abf039d5fa78f205ca56790426639`.
Rollback tag `gopod-gopod:rollback-pre-backlog-20261103-e6f0fad` retains `ec51ee6`.
No production switch until CI is green and the idle gate is rechecked.

## Deployment connection checkpoint

Exact main CI passed: test and isolation-spike. An intervening native/outbox
operation was allowed to finish; subsequent read-only counts were zero.
SSH then failed before executing the rollout script: the 1Password agent refused
the main SSH key signature and the server disconnected for authentication attempts.
No controller stop or production switch occurred. Production remains `ec51ee6`;
owner was asked to unlock the existing SSH agent. Credentials/config unchanged.
