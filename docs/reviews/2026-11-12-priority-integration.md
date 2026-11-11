# Priorities 1–3: integration review and release gates

## Scope

Owner instruction: implement backlog priorities 1–3 in agents. Integration branch
`integration/backlog-1-3`, base `da529a8`, PR [197](https://github.com/Coriol-is/gopod/pull/197).
This is an implementation checkpoint, not completion of every parent acceptance gate.

| Area | Source checkpoints | Evidence |
| --- | --- | --- |
| Delivery ordering, frozen foreground producer | `7c15035`, `becd396` | [Ordering](2026-11-12-reply-surface-order.md), [foreground](2026-11-12-foreground-delivery.md) |
| Exact obsolete keyboard retirement | `9ec3914` | [Review](2026-11-12-reply-keyboard-retirement.md) |
| Registered-private nontext creation journal | `9f321d7` | [Review](2026-11-12-nontext-message-ownership.md) |
| Confirmed deletion, exact external observation, counters | `6246398`, `b709fbd`, `def3691` | [Review](2026-11-12-topic-deletion-orchestration.md) |
| Conversation provider/model selection | `0c0593b`, `3186ba8`, `3369c39` | [Review](2026-11-12-conversation-provider-selection.md) |
| Cross-feature preference retirement | `9f07f09` | Transaction rollback, sibling preservation and idempotency regressions |

Source commits were cherry-picked; integration equivalents are in branch history.
Independent source review covered each slice. Final independent review on integrated
`28cea8523e13e55a257d09f57c474b523ef57dc4` found no actionable blocker in scoped
provider/native callers, Stop/selection/freeze context composition, backup table
parity, atomic preference retirement or shared nontext ownership. Focused integration
regressions also passed. This does not waive the full-suite gate below.

## Verification

All source slices passed author build/vet/full tests and affected race tests.
Combined `28cea85` focused races passed for store, runner, control and Telegram
(8.650s / 7.693s / 6.671s / 9.446s).
Combined build/vet succeeded, but the full suite failed
`TestWaitDraftRefreshEndsWithContext` with a late fake-server observation after
cancellation. Investigation separated delayed server observation of an already-issued
request from a real ready-tick/cancellation race: before the guard, 15/30 adversarial
runs attempted an extra HTTP client dispatch with a canceled context.

Follow-up `5e487e5` (integrated as `8a320eb`) checks shutdown again after selecting a tick. Its deterministic test
holds the second HTTP request for three tick intervals, cancels, joins, verifies
exactly two client dispatches, then releases the server and proves a late observation
without a new dispatch. Root independently reviewed both fix and test. Final
build/vet/full suite passed (Telegram 60.029s); 100 regression repeats passed
(13.190s), and 15 repetitions of affected lifecycle race tests passed (26.540s).
No remote cancellation/retraction guarantee is inferred from a local join.
Earlier required CI on `56f7c28` and `8e2fb18` passed; these are not final-head CI.

## Release and parent gates

- GitHub requires one PR approval. Local independent agent review does not satisfy
  that server-side rule; no override or auto-merge is authorized by this report.
- Foreground and topic deletion flags default off. Provider selection is connected
  without a new flag and requires native image/session continuity acceptance.
- Registered-group destination/actor/publication policy and unregistered scope
  authority are not granted by owner-private implementation. See the nontext review
  for exact current guard locations; the group namespace itself already exists.
- Historical missing provenance and unknown remote/native outcomes retain holds;
  no inferred evidence, remote replay or manual force-clear was added.
- Gone-topic destination policy is resolved by owner decision D075: preserve the
  frozen answer without sending or rerunning the model. No main-chat reroute.
- No native/client or disposable-topic destructive acceptance, merge, deployment or
  runtime flag activation is claimed. Existing user topics were not deleted.
- CP-037 parallel/session activation and managed-task credentials remain deferred.
  Worktrees and rollback evidence are preserved; unmerged branches are not cleanup candidates.

After exact-head CI and required approval, follow the existing Pi-controller-only
rollout with idle checks, verified backup, health/version/state and scoped acceptance.
Do not enable foreground/deletion merely because this implementation is merged.
