# Pi deployment — CP-040 agent attachments, 2026-11-02

Owner authorized ("deploy", 11:12 CET) after [PR #140](https://github.com/Coriol-is/gopod/pull/140)
merged as `8a4f55a` ([D058](../DECISIONS.md)). Rollout over Tailscale (`rpi4`).

## Release checks

- PR #140 head `6a60339`: `test`, `isolation-spike`, SonarCloud all pass; independent
  review (one Important fixed: `retrying` receipt status documented to the agent; minors
  fixed) before the merge. Main CI on `8a4f55a`
  ([run 41426581794](https://github.com/Coriol-is/gopod/actions/runs/41426581794)) green.

## Backup and rollout

Previous checkout `d8866c9`, image `sha256:5e75c582…`. Rollback tag
`gopod-gopod:rollback-pre-cp040-20261102`.

1. Online backup `20261102T101224Z-db-517211d2` created and verified (38 tables, 7,868 rows).
2. Checkout fast-forwarded to `8a4f55a`; image built with `VERSION=8a4f55a` while the
   old controller served (tag `gopod-gopod:cp040-8a4f55a`).
3. Idle check: all counters zero. `docker compose stop -t 60 gopod` at 10:16:56 UTC;
   the idle owner agent container was stopped before the capture (runbook step 3).
4. Matched-stopped backup `20261102T101714Z-ms-d176631f` created and verified
   (database + declared config + artifact blobs, 38 tables, 7,873 rows).
5. `docker compose up -d --no-build gopod`; new controller started **2026-11-02 10:17:49 UTC**,
   image `sha256:98c0f5eb14cb9e16cd5507181100fe6ca91e0b24880280155b3079a95f2e482f`.
   Downtime about 53 seconds.

## Container skills sync (new runbook step)

The controller mounts container skills from `GOPOD_REPO_ROOT/container/skills`
(`/opt/gopod/repo/container/skills` → `/home/node/.claude/skills` in agent containers).
`/opt/gopod/repo` is not the source checkout (`~/gopod`); it held a copy from 2026-05-07
with only `scheduler`, so `attachments`, `freefeed`, `integrations` and `memory` were
not visible to agents on the Pi. Fixed at 10:19 UTC: the old directory was copied to
`/opt/gopod/repo/container/skills.bak-20261102`, then
`rsync -a --delete ~/gopod/container/skills/ /opt/gopod/repo/container/skills/`;
`diff -rq` reports the two in sync and the running owner container lists all five
skills. **Every deployment must repeat this sync after the checkout moves** until the
mount source is pointed at the checkout (tracked in HANDOFF).

## Operational verification

- `gopod starting version=8a4f55a`; `session mapping mode=legacy`; `outbox dispatcher running`;
  startup notice sent (`previous_run=graceful replayed=0`); 23 commands published.
- After 80 s: running, restart count 0, OOM false, zero `ERROR`/`WARN`; memory ~2.1 GiB of 3.8 GiB.
- Owner agent container recreated; `/workspace/chat/ATTACHMENTS.md` seeded;
  `/home/node/.claude/skills` lists `attachments`.

Not verified: a real attachment round trip (ACCEPTANCE A22–A25, owner's real client).

## Rollback

Check idle, stop the controller, retag `gopod-gopod:rollback-pre-cp040-20261102` as
`gopod-gopod:latest`, `docker compose up -d --no-build gopod`. Restore the skills
directory from `skills.bak-20261102` only if an old build needs it. Schema unchanged.
