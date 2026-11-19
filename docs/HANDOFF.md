# gopod — current handoff

**Last updated:** 2026-11-19 (owner-approved implementation-first reprioritization)

Read this first; use [ROADMAP](../ROADMAP.md) for the
approved queue, [proposal registry](proposals/README.md) for CP status and
[ACCEPTANCE](ACCEPTANCE.md) for pending real-client verification.

**Acceptance authorization (2026-11-03):** The owner explicitly permits agents
to access Telegram and run gopod acceptance checks themselves, including test
messages, commands, attachments and supported button interactions, without
per-check permission. See [CLAUDE.md](../CLAUDE.md#workflow-rules). Older references
to "owner in Telegram" do not require handing automatable checks back to the
owner. No acceptance result or deployment status changes with this instruction.

## Resume here — implementation first, then comprehensive acceptance, 2026-11-19

Owner explicitly selected the **whole queue through CP-007, then acceptance**:
**CP-002 → CP-016 → CP-019 → CP-033 → CP-014 → CP-007 → comprehensive acceptance**.
This supersedes the earlier acceptance-first order. CP-018 and CP-040 acceptance
move to the final campaign, which covers all implemented features, not only new code.
See [BACKLOG](BACKLOG.md#current-priority--owner-approved-2026-11-19),
[ROADMAP](../ROADMAP.md#execution-priority--2026-11-19) and the
[campaign scope](ACCEPTANCE.md#comprehensive-campaign--after-cp-007).

### CP-002 checkpoint — edit settlement fix deployed, 2026-11-19

[PR #200](https://github.com/Coriol-is/gopod/pull/200) merged as `371dc04` with the
owner's explicit one-time admin source-merge authorization; it is not a general
source bypass. Contradictory `sent:<id>` evidence for an immutable edit target is
rejected before mutation; owner listing exposes operation kind and edit target.
Full build/vet/tests, targeted races, deadcode, independent review, required PR
CI44556376294 and exact-main CI44564682905 passed. Sonar PR ERROR remains advisory
(six complexity/parameter findings), with no findings suppressed.

Pi controller deployed at 2026-11-19 16:24 UTC: exact binary hash, healthy, restart0,
OOMfalse, all12 idle counters zero. Registered chats, sessions, scheduled tasks,
conversation preferences and execution sessions retained identical full-row hashes.
Matched-stopped and post-rollout backups passed scratch verification. No agent/worker
image or credentials changed. [Release evidence](deployments/2026-11-19-outbox-settlement-rollout.md).

**Next:** continue source-backed CP-002 unknown/history reconciliation gaps under
existing evidence/no-replay contracts; do not reimplement this settlement guard.
Then CP-016/019/033/014/007, followed by comprehensive acceptance. This fix does not
provide generic legacy-journal settlement or prove unknown remote outcomes.
Foreground, topic deletion, managed tasks and CP-037 remain off/paused. No broad
client/provider acceptance performed. Source worktree and remote branch retained;
no pending merge approval for this slice. No parent CP lifecycle is promoted.

## Historical deployment — priorities 1–3, 2026-11-12

PR197 is squash-merged as `56d1a7d` after explicit owner authorization for a one-time
admin merge. Required PR and exact-main CI, independent reviews, build/vet/tests
and affected races passed. Pi controller `56d1a7d` is healthy; image/binary hashes,
registration/session/task preservation and verified pre/post-migration backups pass.
No worker/agent-image/credential change. [Release and acceptance evidence](deployments/2026-11-12-priority-rollout.md).

Implemented: mixed legacy/outbox ordering, gated immutable foreground producer,
one-shot obsolete keyboard retirement, registered-private document/voice/As-file
ownership, gated topic confirmation/deletion/exact observation/local retirement,
and conversation-scoped provider/model selection. Preference cleanup is atomic
with retirement; per-topic counters are cleared only after quiescence. The final
wait-refresher cancellation regression is fixed and validated100 times plus races.
[Integration review](reviews/2026-11-12-priority-integration.md), D072–D075.

Foreground and topic deletion flags remain off. CP-018 routing is live; initial
Telegram version/provider inspection and one native Codex turn passed; immutable
attempt selection was checked in SQLite and all12 idle counters returned to zero. Full provider alternation/session continuity and the broader client
matrix remain open. CP-037 parallel/session activation and managed tasks remain
paused/off. Owner decision D075: save a gone-topic frozen answer without sending,
rerunning the model or redirecting to main. There is no pending destination choice.

The November 19 queue above supersedes the rollout's old next-action order.
Do not reopen implemented slices or call these partial parents fully complete.

Rollout documentation PR198 is merged. State-machine audit PR195 and widget
specification PR196 preserve their paused/design-only boundaries; neither authorizes
implementation. Runtime remains `56d1a7d`; these documentation merges need no deploy.
Source and unrelated worktrees, rollback images, backups and credentials are retained.
Owner policy (2026-11-19): prose-only documentation may be committed/pushed
without a PR; existing docs PRs may be merged autonomously without reviewer approval
or waiting for prose-only CI reruns. Admin docs merge is authorized when needed.
Source changes retain normal worktree/PR/review/CI gates. See CLAUDE.md.
PR197's source admin exception was one-time only.

## Current design — Telegram widgets, 2026-11-11

Owner requested specifications only after the sendWidget feasibility assessment.
[CP-041](proposals/CP-041-telegram-widgets.md) is accepted for design only;
[product](superpowers/specs/2026-11-11-telegram-widget-panel.md) and
[protocol](superpowers/specs/2026-11-11-telegram-widget-protocol.md) drafts define
an owner-private read-only status panel, Refresh, text compatibility, durable
creation, ownership/expiry and native-client acceptance. Baseline da529a8.
No application/dependency/configuration changes, Telegram probes or deployment.
Existing priority and paused-feature gates are unchanged. Next for this feature:
review the design; implementation requires a subsequent owner request and SDK,
scoped snapshot/control-outbox and generation-identity qualification.
Documentation: [PR #196](https://github.com/Coriol-is/gopod/pull/196), branch
`docs/telegram-widget-spec`, worktree `gopod-widget-spec`. Local links and whitespace
checks passed. Independent review findings addressed: per-panel expiry is excluded
from shared snapshot caches, late receipts cannot revive expired/retired panels,
and registry table formatting is corrected. This remains a design-only change under the owner-approved immediate docs-merge policy.

## Paused — explicit state machines refactor, items 1-6, 2026-11-08

Read-only audit of implicit state machines is recorded in
[reviews/2026-11-08-implicit-state-machines.md](reviews/2026-11-08-implicit-state-machines.md):
19 machines, transitions with file:line, Mermaid diagrams, test gaps, risk ranking.
Twelve high-impact claims were re-verified by reading the cited code; the rest come
from five area audits that cite lines. One verified production-path bug: forced
restart with a held managed attempt fails in `cmd/gopod/restart.go:580-593`
(stale reservation flag after `SettleTaskAttempt` already released; gated by
`GOPOD_MANAGED_TASKS`, untested).

Owner approved refactoring ranking items 1-6 as parallel agent tasks, then paused
before dispatch until the five-hour quota window resets. The implementer briefs are
fixed in [specs/2026-11-08-explicit-state-machines.md](superpowers/specs/2026-11-08-explicit-state-machines.md).
The six dedicated refactor branches remain unimplemented at base `da529a8`.
The audit describes the November8 snapshot: PR197 subsequently connected foreground
and topic retirement callers and fixed a wait-refresher cancellation race. Reconcile
those changes before resuming; do not reimplement completed slices.
Six clean worktrees exist with no commits:

| Item | Branch | Worktree |
|---|---|---|
| 1 turn transitions | `store/turn-transitions` | `../gopod-turn-transitions` |
| 2 managed reconcile | `tasks/managed-reconcile` | `../gopod-managed-reconcile` |
| 3 restart phases | `restart/typed-phases` | `../gopod-restart-phases` |
| 4 runner evidence | `runner/execution-evidence` | `../gopod-runner-evidence` |
| 5 legacy task status | `tasks/legacy-status-check` | `../gopod-legacy-task-status` |
| 6 login/stream state | `telegram/login-stream-state` | `../gopod-telegram-view-state` |

**Next (only on explicit resume):** refresh each branch against current main, then dispatch one implementer
per brief (spec parts 1-6) into its worktree, following `superpowers:subagent-driven-development`
with a task review per branch. Merge order to limit conflicts in
`internal/store/restart.go` and `cmd/gopod/restart.go`: 2 (smallest), 1, 3, then 4, 5, 6
with a rebase before each. Controller rulings already taken are listed at the end of
the spec's introduction and in the audit's recommendation table; items 2 and 4 keep
their out-of-scope lists. No deployment is part of this batch until all six merge
and the owner confirms Pi scope.

## Historical checkpoint — deadcode hook and Sonar scope, 2026-11-06

Owner requested deadcode in pre-commit and exclusion of scripts/all Python from
Sonar. Work is on `ci/deadcode-precommit`, worktree `gopod-deadcode-hook`, base3ea9328.
Staged-snapshot Linux/ARM64 analysis uses pinned v0.49.0 with tests;16-symbol
baseline prevents new unreachable functions without deleting planned disconnected
features. Five disposable-Git integration tests, actual staged hook and baseline analysis
PASS. Independent source/config review PASS on298d1e3; full build/vet/tests PASS
(Telegram73.339s). PR194 Sonar15:29:58UTC OK; zero changed file/issue components,
Python/scripts excluded, project settings read back. Final prose/required CI follow. Sonar configs include `scripts/**,**/*.py,**/*.sh,.githooks/**`. No application Go code was renamed.
Owner explicitly requests merge/push only, no deployment. Finish final review/CI,
squash merge, enable the local hook and clean the integrated task worktree.
Pi runtime remains0200892; no restart or deployment is part of this follow-up.
[Development checks and audit](DEVELOPMENT-CHECKS.md).
Separate unresolved CI observation: unchanged managed Stop/backoff test saw
`turn = running` in run42243855328;30 local repeats passed. Do not treat subsequent
CI success as root-cause resolution; no runtime change/deploy in this batch.

## Recorded deployed baseline — D071, 2026-11-06

#193 (`0200892`, D071) closes the registered-private sync voice-placeholder
DeleteMessage gap: commit-before-HTTP attempts, exact outcomes, durable pending/
ACK tombstones, mutual edit/repair/outbox barriers and no Finished/TTS after unknown.
ACK removes only the captured control; no text delivery is inferred. One failed
receipt settlement cannot starve unrelated control recovery.
[Review](reviews/2026-11-06-durable-delete-outcomes.md).

Full build/vet/tests, affected races and independent exact-head review PASS.
Required PR CI42238584841 on5ff03d3 and exact-main CI42239307009 PASS.
Pi controller0200892 passed isolated readiness, pre/post-migration backup checks,
health/version and unchanged state hashes. Eleven idle counters zero; deletion
ledger exists with zero rows. No live deletion fault injection/voice acceptance.
[Release and housekeeping](deployments/2026-11-06-durable-delete-rollout.md).
No implementation in flight; feature worktree/branch and candidate objects removed.
Docs PR192 cleanup follows merge. Protected backups/auth/rollback remain.

Owner's production-only Sonar rule is in CLAUDE: automatic and CLI configs now
exclude tests entirely. Exact-head PR analysis OK,zero test components,22 open
smells/no bugs/vulnerabilities. Main scanner confirms exclusions, but57,111 lines
exceed50k plan limit; published main baseline remains2026-10-26. No hidden production
code, subscription changes or manual finding suppression. Go CI/race unchanged.

**Next:** group/unregistered compatibility, other non-edit effects, historical
missing provenance and explicit unknown reconciliation. Eager old-row cleanup,
mixed-path ordering and integrated foreground qualification remain open. No full
CP-002/CP-016 completion or foreground/worker activation.

## Previous completed D070 — deployed, 2026-11-06

#191 (`6820422`, D070) journals registered-private direct text/HTML/keyboard edits
with durable creation/control/ACKed-outbox provenance. Pending direct/repair/outbox
effects mutually fence the physical target across restart; exact receipts settle
SQLite only. Unknown edits prevent outer fallback/replacement/TTS. Historical
private targets without provenance hold truthfully; no blanket overwrite authority.
[Review](reviews/2026-11-06-durable-edit-outcomes.md).

Full build/vet/tests and affected Store/Telegram/backup/control races PASS;
independent exact-head review and CI `42232618774` PASS on `a47d53b`.
Sonar OK,26 smells read/retained. Pi controller `6820422` passed exact-main CI
`42233254945`, isolated readiness, pre/post-migration backup verification, unchanged
state hashes, health and Telegram version. Ten idle counters zero, including
`direct_edits_pending`; ledger exists. No worker or foreground activation.
[Release and housekeeping](deployments/2026-11-06-direct-edit-rollout.md).
D070 implementation is complete; its integrated feature worktree and branch removed.
Protected backups/auth/rollback retained; D070 docs branch was removed after #190 merge.

**Remaining after D071:** groups/unregistered edits and other non-edit effects,
historical missing provenance and explicit unknown reconciliation. Eager old-row
cleanup, mixed-path ordering and integrated foreground qualification remain open.
D070 covers direct edits in registered private scope, not full CP-002/CP-016.

## Previous completed repair journal — 2026-11-06

PR #189 (`a85b328`, D069) journals each control repair before HTTP, preserves
unknown outcomes across restart/rebinding/retention, and holds conflicting message
edits, outbox claims, relevant topic quiescence and backup idle. Exact positive or
definite-unsent receipts settle SQLite only; no automatic replay/force-clear.
[Source review and validation](reviews/2026-11-06-durable-repair-outcomes.md).

Full build/vet/tests, Store/Telegram/backup/control races, independent exact-head
review and required PR CI `42216574391` PASS on `e329bf2`. Sonar OK,30 smells read,
none suppressed. Pi controller `a85b328` passed exact-main CI `42217121239`,
isolated readiness, pre-rollout matched-stopped backup, post-migration database
backup, health/state preservation and Telegram version acceptance. No worker.
Nine idle counters zero including `control_repairs_pending`; ledger exists.
[Release and housekeeping](deployments/2026-11-06-durable-repair-rollout.md).
D070 above is now deployed; one integrated clean agent worktree and feature branch
removed. D069 evidence must be preserved on downgrade; no automatic unknown clearing.

**Remaining:** historical pre-journal repair evidence and explicit resolution of
unknowns without positive evidence, remaining groups/unregistered and non-edit effects after D070, eager old-row
cleanup, mixed-path ordering and integrated foreground qualification. CP-016
retains nontext/history/managed/native/retirement gaps. No foreground activation.

## Previous completed control/topic batch — 2026-11-06

- #187 (`0cd8078`): per-message edit arbitration protects published final targets
  from subsequent legacy repair/writes; More/Back checkpoint old control retirement.
  CAS scheduling prevents LIMIT100 starvation; obsolete Stop cannot requeue an
  ACKed terminal control. [Report](reviews/2026-11-06-control-final-arbitration.md).
- #186 (`6faa197`): existing durable ownership journal covers private topic-target
  text with exact source/target validation and receipt-only repair/no-replay.
  [Report](reviews/2026-11-06-topic-ownership-next.md). No schema change.

Full build/vet/tests, affected races, independent exact-head review and required
PR CI PASS. Sonar OK for both (nine/five smells retained). Pi controller `0cd8078`
passed exact-main CI `42210762626`, isolated readiness, matched-stopped backup
verification, health/state preservation and Telegram version acceptance. No worker.
[Release, acceptance and housekeeping](deployments/2026-11-06-control-topic-rollout.md).
The D069 follow-up above is also deployed. Two clean integrated agent worktrees and their
local/remote branches removed; protected backups/auth/rollback retained.

**Next implementation:** preserve unresolved historical/non-repair gaps before
foreground activation; the D069 barrier above is implemented. Eager row
cleanup, mixed-path latest ordering and integrated preview/Stop/publication remain.
CP-016 still needs nontext/edit attempts, historical ownership/retention, unknown
reconciliation, managed/native settlement and confirmed retirement orchestration.

## Previous completed cycle — 2026-11-06

Permanent development cycle is recorded in [CLAUDE.md](../CLAUDE.md#workflow-rules):
review/CI/merge → Pi controller deploy/acceptance → backlog/docs/handoff → housekeeping.
Deployment `80f4b11` passed exact-main CI `42198643997`, isolated readiness, verified
matched-stopped backup, health/state preservation and Telegram version acceptance.
Controller only; no worker. [Release and cleanup evidence](deployments/2026-11-06-reliability-cycle-rollout.md).
Two integrated clean worktrees and six local/remote branches removed. Protected
backups/auth/rollback and unrelated services preserved. New work is listed above.

## Completed parallel slices — 2026-11-06

- CP-002/003: #183 (`261805e`) adds restart-safe More/Back/As file from fully
  acknowledged immutable results. Exact scope/topic/message/markup checks reject
  partial, uncertain, compacted, bare-keyboard and superseded rows. Original text
  survives process-local memo loss. A review-found pre-HTTP failure classification
  bug was fixed and regression-tested. [Report](reviews/2026-11-06-reply-actions-ack.md).
- CP-016: #182 (`0ac9b3f`) journals restart/lifecycle/IPC noncontrol main-text
  notices, preserving fallback origin and no-replay on unknown/ACK-failed sends.
  [Report](reviews/2026-11-06-topic-retirement.md).

Both passed full build/vet/tests, affected races, independent exact-head review
and required CI: #183 `42197591379` on `ef3dbd0`; #182 `42196527739` on `752120d`.
Sonar gate OK for both, six and two smells respectively, zero bugs/vulnerabilities;
findings read, none suppressed. Reports record exact heads and analysis times.
All five source slices #178–#180/#182/#183 first deployed in `80f4b11` and remain included.

## Deployed delivery prerequisites

- #178 (`da8b4fe`): draft metadata and Stop keyboard use captured registration.
- #179 (`bbc30b4`): generation-scoped wait maps/refresher joins and durable control
  binding reject stale writes while preserving unregistered-sender compatibility.
- #180 (`6722e6a`): committed bare-keyboard attempt after explicit refusal, composing
  HTML/plain alternatives without retrying ambiguous outcomes.

Full checks, affected races, independent review and required CI passed.
[#178 review](reviews/2026-11-05-preview-generation-controls.md) ·
[#179 review](reviews/2026-11-05-wait-control-generation.md) ·
[#180 review](reviews/2026-11-05-outbox-keyboard-fallback.md).
Sonar #180 was ERROR on reviewed `6dde8c9`: duplication 22.4% versus 3%,
18 smells and zero bugs/vulnerabilities. None suppressed; soft-gate policy applied.
Before downgrading a future release containing #180, drain bare-keyboard selections
or hold dispatch and preserve the ledger; see the
[runbook](deployments/BACKUP-RESTORE.md#durable-keyboard-fallback-compatibility-2026-11-05).

## Previous deployment — 2026-11-05 (historical evidence)

Pi controller deployed as `757faa2` at 15:11:59 UTC; no worker deployment.
PRs #173–#177 are now deployed. Exact-main CI passed, candidate isolated readiness
passed, matched-stopped backup `20261105T151122Z-ms-6ff01519` verified. Controller
and owner runtime healthy, zero controller restarts/OOM; chat/session/task hashes
unchanged. Telegram `/version` response `3177169` confirms 757faa2 (Go 1.27.3).
[Rollout, acceptance and cleanup evidence](deployments/2026-11-05-stop-ipc-rollout.md).

Three existing scheduling guides received an explicit D068 matching section;
originals/content preserved. Actual shipped Node consumer on Pi passed read-only
list, identical replay and changed-byte conflict, all hash-matched; 15 tasks
unchanged and claim files empty. This is bounded host/consumer acceptance, not
qualification of arbitrary old consumers or native/visual/power-loss behavior.

Owner-authorized cleanup removed 18 old automatic database-only sets (~119 MiB).
All 20 matched-stopped, three database-only anchors, manual/auth baselines,
rollback images and guide originals retained; backup directory now 275 MiB.
Separate 556 KiB NanoClaw/OneCLI/systemd archives retained. No broad Docker prune.

## Next implementation and acceptance

Follow the implementation-first order in [ROADMAP](../ROADMAP.md#execution-priority--2026-11-19)
and the completion gates in [BACKLOG](BACKLOG.md#current-priority--owner-approved-2026-11-19).
Complete CP-002/016/019/033/014/007 implementation before the comprehensive
acceptance campaign; CP-018 and CP-040 are included in that campaign.
Ordering, eager keyboard retirement and private nontext ownership shipped in PR197;
foreground and topic deletion remain off. D075 preserves frozen gone-topic answers.
Historical next-action lists above describe earlier deployments, not current missing
source or execution order. No acceptance checks were run in this documentation task.

Managed-task credentials remain owner-deferred and activation off. CP-037 P2–P7
and T5/T5b remain paused; P0/P1 are deployed in legacy mapping. T9 cgroup/reboot
work is complete. CP-007 stays below the product queue; manual backup cleanup
is not automatic retention or qualified full restore.

CP-040 A22–A25, ordinary Stop/restart, CP-020 provider/client and CP-038 Claude
browser-completion gates remain in [ACCEPTANCE](ACCEPTANCE.md). Existing Telegram
authorization applies; coordinate a single actor. The latest rollout adds one bounded native Codex round trip; it adds no forced-crash,
full visual/native/client matrix or full restore qualification.

## Release and recovery pointers

- Deployed source: `371dc040d480288aa3de5189a384aeca1cb618c1`; exact-main
  [CI44564682905](https://github.com/Coriol-is/gopod/actions/runs/44564682905) passed.
- Pi: `ssh spaceinvaderz@192.168.1.128`; checkout `~/gopod`, data `/opt/gopod/data`,
  mounted workspace repository `/opt/gopod/repo`.
- Rollback: `gopod-rollback:pre-371dc04`; preserve prior rollback images and backups.
- Verified matched-stopped set `20261119T162326Z-ms-eb7e19d4`; post-migration
  database-only set `20261119T162424Z-db-37b991eb`.
- Evidence `/tmp/gopod-release-371dc04` on Pi; durable
  [release report](deployments/2026-11-19-outbox-settlement-rollout.md).
- Preserve task claim spool and SQLite ledgers before downgrade; full workspace,
  auth/session/IPC restore remains unqualified. No automatic old-binary rollback.
- Guide originals `~/gopod-backups/consumer-migration-757faa2` and prior cleanup
  inventory `~/gopod-backups/cleanup-20261105-757faa2.json` remain protected.
- If SSH Git authentication fails, use per-command HTTPS with `gh auth git-credential`;
  explicit URL/refspec fetch requires `--no-prune`. Do not change global auth settings.

## History and documentation maintenance

[Completed/rejected/superseded ledger](history/BACKLOG-2026-11-05-completed.md) ·
[full previous handoff](history/HANDOFF-2026-11-05-before-housekeeping.md).
Historical next-action and undeployed statements in snapshots are not current instructions.
Update this file and the active backlog at implementation/review/deployment checkpoints;
archive completed slices without closing unverified parent acceptance gates.
