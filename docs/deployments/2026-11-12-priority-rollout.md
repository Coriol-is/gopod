# Priorities 1–3 controller rollout — 2026-11-12

## Release and verification

PR [197](https://github.com/Coriol-is/gopod/pull/197) squash-merged as
`56d1a7decd3061ef308898805fc82102318ea549`. The owner explicitly authorized a
one-time admin merge after GitHub rejected normal merge for missing approval.
The merge tree exactly matches reviewed PR head `46a75f5`; independent source
review, full build/vet/tests and affected races passed. Required PR
[CI43121399752](https://github.com/Coriol-is/gopod/actions/runs/43121399752) and exact-main
[CI43289167370](https://github.com/Coriol-is/gopod/actions/runs/43289167370) passed,
including Docker isolation. PR Sonar passed on the exact head at 2026-11-11 23:39:07 UTC;
this is not a claim of a fresh published main Sonar baseline.

Go1.27.4 CGO-disabled linux/arm64 binary was built from the exact merged source.
SHA256: `6749df89871be73972ae3e3250e686d54de935ac875317f42b2cd375a1c5188f`.
The controller-only overlay on the exact prior image passed isolated readiness
without network, credentials or host mounts, with read-only root and dropped
capabilities. Runtime agent/worker images and credentials were not changed.

Pi controller replaced `0200892` at `2026-11-12T19:25:37.403797908Z`.
Image and running binary hash match the candidate; health is healthy, restart count0,
OOMfalse. Registration/session/scheduled-task full-row hashes, legacy mapping and
pending-adoption state matched before and after rollout, before acceptance traffic.

## Preservation

Eleven idle counters were zero before graceful stop. Native runtime held only its
idle sleep process. Stopped-writer idle was checked again before capture.
Matched-stopped set `20261112T192455Z-ms-c62b8cd3` passed scratch verification:
54 tables,11026 rows,one file. Post-migration database-only set
`20261112T192601Z-db-6dfbb091` also passed:58 tables,11054 rows,one file.
The latter checks new schema/table coverage, not artifact/session/auth/full restore.
No live restore occurred. All previous backups and protected credentials remain.

Rollback image `gopod-rollback:pre-56d1a7d` retained. Do not blindly restart the old
binary after new ownership, preference or attempt records: reconcile ledger/schema
compatibility first. Evidence is preserved under `/tmp/gopod-release-56d1a7d` on Pi.
Six unrelated service containers stayed healthy; no broad cleanup occurred.

## Acceptance and boundaries

Telegram main-chat command3178836 received correlated reply3178837:
`gopod 56d1a7d (go1.27.4)`. `/provider` command3178838 received3178839 showing
inherited Codex, chat default Codex, conversation0. Restart notices3178834/3178835
were observed. Post-migration idle has12 zero counters, including topic deletions.

Bounded native Codex request3178840 received rendered answer3178841,
`GOPOD56D1A7DOK`. The observed reply omitted the requested underscore separators;
this establishes a reply round trip, not exact literal-text parity. Durable turn401
completed once (`done`, attempts1), with immutable selection Codex, provider-default
model and conversation0. All12 idle counters returned to zero. No provider override
was changed. Provider alternation, cross-topic session continuity, visual/client
matrix and disposable-topic deletion remain unqualified.

`GOPOD_OUTBOX_FOREGROUND`, `GOPOD_TOPIC_DELETION_ENABLED` and managed tasks remain
off. CP-037 parallel/worker activation remains paused. D075 saves a gone-topic
frozen answer without sending, rerunning inference or main-chat redirect; no
foreground activation was implied by accepting that policy. Existing topics were
not deleted. The broader CP-002/003/016/018 parent acceptance gaps remain in BACKLOG.


## Housekeeping

The disposable isolated-readiness container and candidate image alias are removed
after acceptance; the live image, rollback image, binary, bundle, reports and every
backup remain. Source worktrees and unrelated work are retained. No remote branch
was deleted. Documentation follow-up does not imply another runtime rollout.
