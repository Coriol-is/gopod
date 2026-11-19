# Outbox edit settlement controller rollout — 2026-11-19

PR [200](https://github.com/Coriol-is/gopod/pull/200) merged as
`371dc040d480288aa3de5189a384aeca1cb618c1` using the owner's explicit one-time
admin source-merge authorization. Merge tree equals PR head `b505911`; source
is unchanged from independently reviewed `d0044df`.

## Checks

Local build/vet/full tests, targeted Store/Control races and staged Linux/ARM64
deadcode passed. Independent review found no blockers. Required PR
[CI44556376294](https://github.com/Coriol-is/gopod/actions/runs/44556376294) and exact-main
[CI44564682905](https://github.com/Coriol-is/gopod/actions/runs/44564682905) passed both
Go checks and Docker isolation. Sonar PR ERROR (maintainability C), six complexity/
parameter-count findings reviewed and retained; advisory. Main published analysis
was stale at 2026-10-26 (`4e3031a`); no fresh main Sonar pass is claimed.

Exact merged source built with Go1.27.4, CGO disabled, Linux/ARM64.
Binary SHA256: `1a9a8325ccccb5c0c042e400df3c3cc2153f007355a72d1261e44736fb231c3a`.
Controller-only overlay on the prior image passed isolated readiness without
network, credentials or host data, with read-only root and dropped capabilities.
Agent/worker images and credentials were not changed.

## Deployment and preservation

Before shutdown all12 idle counters were zero and the owner container ran only
`sleep infinity`. The first preflight failed on Docker top formatting before any
runtime stop; corrected PID-inclusive inventory then passed. Controller and idle
owner runtime were stopped for matched capture. Verified sets:

- `20261119T162326Z-ms-eb7e19d4`: matched-stopped, 58 tables, 12275 rows, one file.
- `20261119T162424Z-db-37b991eb`: post-rollout database-only, 58 tables, 12302 rows,
  one file.

Both scratch verifications passed. These are not full workspace/auth/session/IPC
restore acceptance. Registration, session, scheduled-task, conversation-preference
and execution-session full-row hashes matched before shutdown, after shutdown and
after startup. The live store was not restored or overwritten.

Controller started at `2026-11-19T16:24:13.414Z`, version `371dc04`; running binary
hash matched. Healthy, restart0, OOMfalse; all12 final idle counters zero. Native
owner runtime returned healthy; unrelated service containers remained running.
Pi checkout fast-forwarded to merged source; unrelated untracked backup preserved.

Rollback image: `gopod-rollback:pre-371dc04`; all previous backups/credentials retained.
Private release evidence and scripts: `/tmp/gopod-release-371dc04` on Pi. The earlier
November12 temporary evidence directory was absent; its committed report remains.

## Boundaries and housekeeping

No foreground, topic-deletion, managed-task or paused parallel-session activation.
No broad Telegram/provider/client acceptance or deliberate remote fault injection;
that campaign remains after CP-007. Health/version/backup checks establish this
controller rollout, not complete CP-002 reconciliation or feature acceptance.

Disposable readiness container removed. Candidate, live and rollback images plus
source worktree/branches are retained; no remote branch deletion or broad prune.
[Source review](../reviews/2026-11-19-outbox-settlement-evidence.md).
