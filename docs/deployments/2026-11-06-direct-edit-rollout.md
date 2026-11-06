# Direct edit provenance rollout — 2026-11-06

## Release and scope

Pi controller deployed `682042284175b7a9559acb3b5ed32a126ace64d4` at 14:27:21 UTC,
replacing `a85b328`. No worker, agent-image, provider, credentials or environment
change. #191/D070 covers registered-private direct text/HTML/keyboard attempts with
durable provenance, exact outcomes and mutual raw-target barriers. Outer streaming,
silent/voice, wait and terminal-error paths preserve NoReplay; unknown edits cannot
lead to replacement sends or TTS. [Review](../reviews/2026-11-06-durable-edit-outcomes.md).
Full tests/races and independent exact-head review PASS; required PR CI and exact-main
[CI 42233254945](https://github.com/Coriol-is/gopod/actions/runs/42233254945) PASS.
Sonar OK on a47d53b: 26 smells retained, zero bugs/vulnerabilities.

Historical registered-private messages without durable provenance now truthfully
hold instead of mutating old messages. Groups/unregistered edits, DeleteMessage,
other non-edit effects, explicit unknown reconciliation and integrated foreground
qualification remain open. No foreground, managed tasks, parallel sessions or topic
deletion activation. This release does not reconstruct historical remote effects.

## Build, backups and preservation

CGO-disabled linux/arm64 Go 1.27.3 binary SHA256:
`1ac0700e6d27fb4c72512c9451e53b7d51c9f60c93b658d0ec79e1610f846801`.
Matches running binary. Exact-prior-image controller overlay passed isolated
readiness without network, credentials or host mounts, with read-only filesystem
and dropped capabilities. Before stop nine idle counters zero and owner only sleep;
after startup ten counters zero, including `direct_edits_pending`.

Matched-stopped pre-rollout backup `20261106T142641Z-ms-fdaef581` scratch verified:
52 tables, 9891 rows. Post-migration online database-only backup
`20261106T142752Z-db-949e93c5` scratch verified: 53 tables, 9922 rows. Second set
verifies new schema/allowlist and does not replace artifact/config coverage. Full
workspace/provider auth/session/IPC recovery remains unqualified; preserved in place.
No live restore occurred. Registry2/session39/task15 full-row hashes unchanged
before/after, integrity OK, mapping unchanged, pending adoption zero.

Controller healthy, restarts0/OOMfalse; StartedAt `2026-11-06T14:27:21.302367074Z`.
Owner and six unrelated services healthy. Read-only inspection confirms direct-edit
ledger exists with zero attempts; no production edit fault injection claimed.
Rollback `gopod-rollback:pre-6820422` retained alongside prior evidence. Older binaries
cannot enforce D069/D070 holds; preserve complete DB and do not downgrade across
pending attempts. Remote evidence `/tmp/gopod-release-6820422`.

## Acceptance and housekeeping

Telegram `Nanoclaw_go_bot`, retained topic3176309: command3177452, response3177453
at14:27:48 UTC `gopod 6820422 (go1.27.3)`. Restart notices3177450/3177451 observed.
This confirms release/routing, not native/visual keyboard or full foreground acceptance.

Verified exact PR-file equivalence to squash and ancestry in main before deleting
clean agent worktree and local/remote `feat/durable-edit-outcomes`. Canonical/session
worktrees retained; current docs PR190 branch cleanup is planned after merge.
Disposable readiness container and candidate alias removed. Backup root321MiB;
no backup deletion, NAS, broad Docker prune or unrelated-service changes. Protected
backups/rollback/auth and existing untracked credential backup preserved.

Backlog/handoff/roadmap/proposal checkpoints and completed ledger reconciled. Next
work is explicitly remaining compatibility/non-edit uncertainty and reconciliation,
eager old-row cleanup, mixed-path ordering and integrated foreground qualification.
