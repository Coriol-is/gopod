# Pi deployment — resumed reliability streams, 2026-11-02

Owner authorized `deploy / merge / accept` after PRs #143–147 merged.
Initial source **81f5f08d3f33514033ac3c0a1d06a9952482c1a7** was deployed at 18:22 UTC.
Final deployed source is **a0de4dc62f1614f9947ef859c84964037e0b9e51** (cursor correction below).
This accepts the operational rollout and the bounded command probes below;
it does not complete the CP-002/CP-016 features or all live-client scenarios.

## Release and known issue

All five PRs passed test, Docker isolation-spike and Sonar before merge.
Integrated build/vet/full tests and race suites for store/runner/queue passed.
[Main CI 41498601957](https://github.com/Coriol-is/gopod/actions/runs/41498601957)
passed on the initial deployed source `81f5f08`.

The preceding main run on `5b0e362` failed the task IPC replay test. Investigation
found a pre-existing receipt/archive replacement race, deterministically reproduced;
100 repeated ordinary runs also passed, showing why retrying CI is not a fix.
The mutation journal still protects task changes, but a replacement may lose its
receipt. [Finding, diagnostic and fix constraints](../reviews/2026-11-02-task-ipc-replay-race.md).
It remains open; no fixture sleep or protocol change was included in this rollout.

## Backup and rollout evidence

- Previous controller source `8a4f55a`, image
  `sha256:98c0f5eb14cb9e16cd5507181100fe6ca91e0b24880280155b3079a95f2e482f`.
- Retained rollback tag: `gopod-gopod:rollback-pre-streams-20261102`.
- Online set `20261102T181545Z-db-cb79e88a`: verified, 7,286,784 bytes,
  38 tables, 8,077 rows, integrity OK. Old controller continued serving.
- Checkout fast-forwarded cleanly to `81f5f08`; ARM64 image built on the Pi with
  `VERSION=81f5f08`, additionally tagged `gopod-gopod:streams-81f5f08`.
- Container skills synced from checkout with `rsync -a`; `diff -rq` passed.
  There were no skill content changes in this source wave.
- Pre-stop idle gate: pending/running turns, in-flight outbox, unresolved deliveries,
  artifact reservations, executing task runs and held attempts all zero.
- Graceful stop began **18:20:43 UTC**. No live gopod agent container remained;
  unrelated containers were not stopped.
- The first matched capture refused because optional declared
  `/opt/gopod/data/mount-allowlist.json` did not exist; nothing was published.
  Retried without declaring that absent file. No config or secret was changed.
- Matched-stopped set `20261102T182141Z-ms-8e0ed662`: verified, 38 tables,
  8,082 rows; database and referenced artifact coverage, zero declared config
  files. There were no artifact rows/blobs to copy. This does not cover workspace,
  native-session/auth homes or pending IPC files; CP-039 remains open.
- New controller started **18:22:16.666 UTC**, about **94 seconds** after stop:
  image `sha256:c8a733e2bf233f250cdf211fd4055c32367eb2fd98961507c86d5994b7206140`.
- Startup log reports `version=81f5f08`, session mapping `legacy`, outbox running;
  startup notice at 18:22:26 says graceful previous run and zero replayed turns.

## Preservation and bounded acceptance

SQLite integrity is OK. Before/after row counts and SHA-256 hashes of sorted
complete rows match for **2 registrations, 38 native session mappings and 15
scheduled tasks**. No adoption is requested. `topic_deletions`, `turn_results`
and `turn_result_members` contain zero rows: the foundations have not activated
new foreground ownership or a deletion command. No feature flags, provider
credentials, schedule settings, Pi resource profile or host boot settings changed.
After startup and command probes: controller running, restart count 0, OOM false,
no WARN/ERROR lines, and no `artifact delivery DISABLED` warning.

The five previously approved owner-private commands were sent through the
standalone Telegram Toolkit CLI to `@Nanoclaw_go_bot`. Replies and button markup
were read through the same toolkit client; no buttons were clicked or model
request made. These are private account message IDs, not public links.

| Probe | Sent → reply | Evidence |
| --- | --- | --- |
| `/sessions status` | 3175944 → no observed reply; retry 3175953 → 3175954 | Retry: legacy, adoption no, zero durable sessions/native contexts. The initial missing response remains an observation, not a pass. |
| `/telegram doctor` | 3175945 → 3175946 | Read-only diagnostic response, startup Codex 0.164.0 reported; no stale marker. Native inference was not tested. |
| `/tasks` | 3175947 → 3175948 | Page 1/5, 15 total; Pause/Next markup present. |
| `/silent` | 3175949 → 3175950 | Off; on/off picker present. |
| `/model` | 3175951 → 3175952 | Default; default picker present. |

The initial session-status send is present in account history at 18:22:48,
but no reply was observed and no corresponding warning/error was logged. The
single status-only retry at 18:24:21 responded at 18:24:22. Investigation found
a concrete cursor defect: middleware stores next update ID, while SDK v1.27.0
interprets `WithInitialOffset` as last seen ID and increments again. Startup
therefore skipped one update. Source and fake-HTTP regression prove the defect;
the live startup cursor/first-observed-update gap strongly associates it with
the lost command, whose dropped update cannot be retrieved. A successful retry
is not acceptance of that first-message behavior. The corrective rollout is
recorded below. Visual rendering, callback actions,
provider turns/resume, full Stop behavior and attachment round trips stay open
in [ACCEPTANCE](../ACCEPTANCE.md).

## Rollback

At this checkpoint new topic/result tables are empty and mapping is still legacy.
If rollback is needed, first check idle, stop the controller, retain the current
image/data and retag `gopod-gopod:rollback-pre-streams-20261102` to the compose
image name, then start with `docker compose up -d --no-build gopod`.
The release adds `topic_deletions`; an old backup verifier does not know that
table. Use the retained new binary for post-upgrade backup inspection, and
reconcile before rollback if topic intents/frozen results or adoption ever appear.
No database restore or rollback was performed.


## Cursor correction — a0de4dc

[PR #148](https://github.com/Coriol-is/gopod/pull/148) was independently reviewed
and merged after test, isolation-spike and Sonar passed. It passes stored next
offset minus one to the SDK; persisted cursor format and schema stay unchanged.
The fake-HTTP regression uses the real `New` configuration and SDK polling,
checks requested offset 1/41, dispatches the first unseen `/ping`, and verifies
that persistence still contains next+1. Build/vet/full tests and the full Telegram
race suite passed. The separate async cursor-ordering concern is not changed.

The corrective rollout uses online set `20261102T182704Z-db-58639157`, verified
with 39 tables and 8,123 rows, and rollback tag
`gopod-gopod:rollback-pre-cursor-20261102` (the preceding `81f5f08` image).
Exact main [CI 41501629944](https://github.com/Coriol-is/gopod/actions/runs/41501629944) passed.
- Idle gate was all zero; the idle owner container contained only `sleep` and was stopped for matched capture.
- Stop began 18:37:09 UTC. Matched set `20261102T183811Z-ms-db3036d5` verified: 39 tables, 8,128 rows; the same coverage limitations apply.
- Controller started at **18:40:15.870 UTC** (about 187 seconds downtime), image `sha256:3c4e1c2c015a3c54e1cf1a4e962403f4990e61819aff6d35eded7f31009e1681`, retained as `gopod-gopod:cursor-a0de4dc`.
- `/sessions status` message **3175958** was sent at **18:37:35 UTC**, while stopped. Reply **3175960** arrived at **18:40:24 UTC** without retry: legacy, no adoption, zero durable sessions/native contexts. Startup notice 3175959 reports a0de4dc.
- Startup stored next offset **719691226** equals the first middleware update ID **719691226**. The first queued command was processed, directly accepting this restart boundary.
- Before/after complete-row hashes and counts match for registrations, native sessions and scheduled tasks; SQLite integrity remains OK. Controller running, restart count 0, OOM false, outbox running, no startup WARN/ERROR.

The first-message defect is corrected and its bounded live restart scenario passes.
The independent IPC receipt/archive race remains open. No elevated profile was activated.
