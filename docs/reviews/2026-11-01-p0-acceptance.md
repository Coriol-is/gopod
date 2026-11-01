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
