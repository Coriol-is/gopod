# CP-037 P0 implementation acceptance

Owner authorized worktree-based parallel implementation, review, checks and merge
on 2026-11-01. Deployment is not part of this authorization. This report is updated
against the integrated source, not just worker claims.

## Scope

Runner lifecycle/termination safety; graceful and forced controller restart;
Telegram interruption-status repair. Session parallelism, adaptive capacity and
provider-routing contracts remain later slices.

## Baseline

`cee5cac` is the verified remote main baseline. Docker 29.8.2 is reachable locally.
Baseline real-Docker synthetic stream cancellation and normal-exit tests passed
using redis:7-alpine, without provider credentials or inference. This establishes
the fixture, not acceptance of the new implementation.

## Gates

- Integrated build, vet, full unit tests and affected-package race suites: pending.
- Disposable Docker busy-idle/drain/force tests: pending.
- Independent agent source review: pending.
- PR final-head test, Docker isolation and analysis checks: pending.
- Main post-merge checks: pending.
- Real Telegram/native-provider acceptance: not performed; separately scoped.

## Known implementation boundaries

To be reconciled from the final implementation and independent review. No source
slice is accepted merely because its existing tests pass. In particular, verify
unknown process death, force non-replay, queue admission during drain and stale
Telegram controls independently.

## Owner checkpoint: handoff to Claude, no combined acceptance yet

Owner later explicitly deferred combined acceptance to a Claude session and
prohibited deployment until the entire feature is complete. No PR/push/merge or
production mutation was performed. Read integration branch log for final local SHA.

| Worktree / source branch | Source commit | Local evidence |
|---|---|---|
| gopod-p0-lifecycle / feat/p0-lifecycle | 37f3efd | build/vet/full tests before final accounting refinement; final runner race and focused drain tests passed; 3 Docker lifecycle + stream tests before final accounting refinement |
| gopod-p0-telegram / feat/p0-telegram | 05f39fa | build/vet; full Telegram/store and targeted race tests passed |
| gopod-p0-restart / feat/p0-restart | c17f99d | build/vet/full tests and affected race tests passed on worker branch; cmd/queue race rerun after ownership fix |
| gopod-p0-integration / feat/p0-integration | inspect current HEAD | cherry-picked workers; resource tests passed before final combined source; full integration checks deliberately not run |

Root changes: pinned redis Docker lifecycle CI step, real resource environment
parsing, opt-in deploy/compose.rpi4.yml, strict-memory gate and conservative growth
reserve tests, main startup wiring. Native-provider parallel resource acceptance
has not run. See [Pi evidence](2026-11-01-rpi4-resources.md).

## Review observations and explicit limits

Independent Telegram-worker review found unknown execution releasing global gate
and stream-setup cancellation gaps; lifecycle worker fixed them in its final
commit. They need final-head independent regression verification. Reviewer also
found fatal startup on ErrExecutionUnproven: a live quarantined container must
leave Telegram control plane available rather than restart-looping. Follow-up `cc29c97` is cherry-picked: quarantine keeps control plane alive and
skips owner warmup; repair startup timeout is 10 seconds. Its targeted controller
tests passed on the worker branch; verify combined behavior before acceptance.
Root reviewed Telegram checkpoint/callback code; worker fixed UTF16 truncation and
terminal/unknown distinctions. These passes are not complete independent approval.

- Normal Stop now stops the slot-owned runtime to prove detached child death;
  next run recreates it with persistent mounts. No live native-resume acceptance.
- Startup live leftovers are quarantined even at the same version; normal drain
  blocks on uncertain execution, explicit scoped force may settle it.
- Old pre-upgrade/unacknowledged Stop messages lack provable bindings; no unsafe
  backfill. Uneditable/deleted messages log failure; no new fallback notice send.
- New Continue/Start UI from C9 is not implemented. Do not advertise full C9 UI.
- No active-stream reattachment or automatic started-work replay is promised.
- No P1+ per-thread runtime parallelism, autoscaler or provider failover implemented.
- Memory caps on the current Pi are requested but not enforced by its kernel.

## First actions in Claude

1. Read CLAUDE.md, this HANDOFF/report and the integration log/diff from cee5cac.
   Verify clean worktrees; preserve all branches and unrelated open PR #108.
2. Review integrated lifecycle/restart/UI behavior independently, especially
   uncertain execution capacity, force cutoff/replay, scheduler suppression,
   cancel-vs-ready CAS, scoped callbacks and startup control availability.
3. Run final-head `go build ./...`, `go vet ./...`, `go test ./...` and
   `go test -race ./internal/runner ./internal/queue ./internal/telegram ./internal/store ./internal/control ./internal/scheduler ./cmd/gopod`.
4. With local Docker, run
   `GOPOD_TEST_SH_IMAGE=redis:7-alpine go test -tags docker_integration -count=1 -v ./internal/runner -run '^TestIntegration(Stream|Lifecycle|FreshPreemption)'`.
   Require non-skipped fixtures. Repeat after any relevant fixes.
5. Validate Compose override without displaying expanded secrets: use
   `docker compose -f docker-compose.yml -f deploy/compose.rpi4.yml config --quiet`
   with a local ignored env source as needed. Never print resolved secret values.
6. Reconcile docs/CONTROL.md, docs/ISOLATION.md and append a superseding ADR to D046
   for the final accepted behavior; do not mark whole CP-037 done for P0.
7. If acceptance passes, create PR, independent review, final-head green CI
   (including Docker job and analysis), then authorized squash merge retaining
   branches. Verify main checks. **No deploy until complete feature acceptance.**

Git HTTPS fallback when SSH agent refuses signing:
`git -c credential.helper='!gh auth git-credential' -c fetch.prune=false fetch --no-prune https://github.com/Coriol-is/gopod.git refs/heads/main`.
Do not prune preserved refs/worktrees. PR bodies must not contain attribution.
