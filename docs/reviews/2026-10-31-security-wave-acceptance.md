# Security fix wave acceptance — 2026-10-31

Scope: groups 1–8 of the [2026-10-30 review](2026-10-30-security-logic-review.md), from
`aa84116` to code baseline `e586c4a`. Separate worktree/PR per group; group 1 started and merged
first. Three worker agents plus the coordinator ran the groups in parallel as slots became free.
No deployment was part of this wave.

## Accepted changes

| Group | PR | Squash | Result |
|---|---|---|---|
| 1 runner | [#113](https://github.com/Coriol-is/gopod/pull/113) | `5296be6` | Registration lookup fails closed; personal runtime needs positive owner identity; output authorization cached for at most 3 seconds within one turn; group deadline 15 minutes; label-based idle cleanup; root UID refused; per-buffer output retention capped at 4 MiB while pipes continue draining. |
| 2 IPC | [#114](https://github.com/Coriol-is/gopod/pull/114) | `405a640` | Per-component fd-based NOFOLLOW request/archive/receipt/pruning paths; archive alias refusal and source disappearance check; bounded watcher state, directory reads and per-channel dispatch. |
| 3 tasks | [#115](https://github.com/Coriol-is/gopod/pull/115) | `eedf9a5` | Non-owner 10 unfinished tasks (including paused), recurring period at least 15 minutes, durable 96 daily run charges; owner notices; registration recheck; non-owner supplied creation IDs refused. |
| 4 outbox | [#116](https://github.com/Coriol-is/gopod/pull/116) | `e7f52ba` | Owner-only `/outbox` lists uncertainty; confirmed evidence settles it; expired delivery no longer blocks later conversation work. |
| 5 Telegram | [#120](https://github.com/Coriol-is/gopod/pull/120) | `e586c4a` | Current group trigger/actor checks on commands and callbacks; user-bound group command tokens; fallback provider owner check; refusal-only send fallback; login/media fixes; safe command echoes; tg:// links stripped; dispatcher claim/live/sweep barrier. |
| 6 logging | [#119](https://github.com/Coriol-is/gopod/pull/119) | `e256b6c` | Secret value/URL/Bearer redaction before both sinks; bounded SQLite log worker and flush lifecycle; positive configured owner ID; dispatcher stops before artifact close; CI actions pinned and contents read-only. |
| 7 memory | [#118](https://github.com/Coriol-is/gopod/pull/118) | `09a08c1` | Immutable original facts, atomic replacement row/vector and superseded_by; pinned/manual/unknown provenance protected; memory.go SQL moved into store; bounded API and shutdown; bridge-only Compose publication. |
| 8 groups | [#117](https://github.com/Coriol-is/gopod/pull/117) | `c05e5b2` | Upload overflow skipped; import paths pinned with NOFOLLOW; default one concurrent group and 96 turns per namespace/UTC day; validation does not charge or revoke the last admitted turn; 35-second drain; file_url denied; dead disabled map removed. |

Every PR passed GitHub `test`, `isolation-spike` and Sonar before its admin squash merge.
The coordinator independently reviewed worker changes; group 2 reviewed coordinator group 4,
and group 3 reviewed coordinator group 7, including fixes. A separate worker reviewed cross-group
integration (budgets, outbox, trigger/login, memory/logging and shutdown) without finding a blocker.

## Verification and review corrections

- Each group: `go build ./...`, `go vet ./...`, `go test ./...`; relevant race suites passed.
- Combined `e586c4a`: `go build ./...`, `go vet ./...`, `go test ./...` passed; race tests passed
  for runner, IPC, scheduler, store, control, Telegram, telegramhtml, memory, log, groupruntime,
  groupsandbox and isolationproxy. Compose configuration, local documentation links and `git diff --check` passed.
- Fault/regression tests cover failed registration lookup, admission throttling, output bounds,
  archive/results symlinks, scheduling caps and restart-stable budgets, uncertainty settlement/expiry,
  callback actor/trigger changes, transport ambiguity, log redaction/lifecycle, protected memory
  provenance and transactional rollback, API body limit, group quota/concurrency and upload overflow.
- `os.Root.OpenFile(O_NOFOLLOW)` followed an in-root symlink on the local Go/macOS runtime.
  IPC and group import fixes therefore use `unix.Openat` through pinned descriptors; symlink tests pass.
- The full group 4 suite exposed the existing `TestReconcileNeverSweepsAPartHeldByALiveWorker`
  race. Group 5 fixes the claim-before-live window with a shared mutex covering claim/publication
  and orphan-sweep snapshot/DB operation. A deterministic barrier regression exercises this boundary.
- Group 7 independent review caught summary source protection and a rollback test that failed
  too early. Both were fixed and re-reviewed. Sonar initially failed duplicate helper code;
  shared store helpers removed duplication, and the final gate passed without relaxing thresholds.

## Operations and limits

No Pi deployment, restart, account login, flag change or live schedule mutation occurred.
Read-only prerequisite before group 4: the Pi SQLite query
`SELECT count(*) FROM outbox_parts WHERE state='uncertain'` returned **0**.
The last recorded deployed release stays `630aa4e`; the merged fixes have no production acceptance yet.

`/outbox settle <part-id> sent:<positive-message-id>|unsent` requires owner authority and confirmation.
`sent` records the supplied acknowledgement; `unsent` cancels the uncertain part and pending remainder,
without replay. Attempts keep their uncertainty history; expiration only removes ordering blockage.
A future resend remains a separate action. Normal dispatcher polling may delay progress briefly after settlement.

Group runtime `MaxConcurrentGroups` and `MaxTurnsPerGroupDay` default to 1 and 96. Task daily
charges are per folder; group daily charges are per namespace. Both are conservative (failed starts
can consume budget); owner-private work remains exempt. Existing too-frequent non-owner schedules
are paused on attempted execution; budget exhaustion delays a task to the next UTC day.
Cron validation conservatively checks the complete daily clock: date-filtered schedules can be
refused when clock slots are less than 15 minutes apart even if actual occurrences are farther apart.

SQLite logging uses a 256-record queue: full queues drop the database copy while stderr continues.
Shutdown flushes for up to five seconds, then cancels the writer and may discard its remaining backlog.

Upload import examines only the first 128 directory entries, and preserves tool-created non-upload
files. Excess input no longer prevents the turn, but newer attachments may remain outside this window.
Host uploads are not deleted by this fix. Memory Compose uses `GOPOD_MEMORY_BIND_IP=172.17.0.1`
by default; verify/override it for a nonstandard bridge before a later deployment.

M9 credential-copy format and its relationship to the deferred task adapter remain an owner decision.
Low findings not assigned to the eight groups remain outside this wave. In particular, this is not
full CP-002/003/004 delivery or native-provider/client qualification. Existing live acceptance gates
in [ACCEPTANCE](../ACCEPTANCE.md) stay open. Worktrees/branches and unrelated PR #108 are retained.
