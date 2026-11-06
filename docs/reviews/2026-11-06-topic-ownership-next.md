# CP-016 topic-target text ownership — 2026-11-06

## Scope checkpoint

Base `8b53386`, branch `feat/topic-ownership-next`. Under existing D064, extend
registered-private text ownership from main/fallback targets to direct topic
targets. No schema, ADR, historical backfill or deletion activation.

Store admission permits only the exact nonnegative source topic as target, or
main fallback. Ownership is committed while the existing transport lease is
held, before HTTP. The same attempt records source and actual target, and ACK
settlement cannot move either. Definite refusals remain retryable; unknown
outcomes and ACK failures retain pending evidence and NoReplay semantics.
Positive receipt repair remains DB-only and requires the exact current scope.

Own Store/Telegram ownership helpers and focused tests. Parallel CP-002 owns
control repair/final-edit arbitration; canonical state documents belong to root.

## Verification checkpoint

Implementation complete. Existing Store and Telegram full package tests PASS.
New focused Store/Telegram target-ownership tests PASS. Affected races PASS:
`go test -race ./internal/store ./internal/telegram -run 'TestTelegramTopicOwnership|TestTopicTargetOwnership|TestTopicOwnership|TestMessageReceipt|TestNoticeOwnership|TestIPCSink|TestTopicQuiescence' -count=1`.
Independent source review and focused race PASS (topic Store/Telegram ownership
and notice tests). `go build ./...`, `go vet ./...`, `go test ./...` and
`git diff --check` PASS. Required exact-head CI remains an integration gate.

Regressions cover invalid cross-topic routes before HTTP, exact ACK/repair target,
message-ID uniqueness across sibling topics, durable restart ambiguity, missing
receipts, same-source/sibling/main output availability, receipt-only repair during
deleting, definitive topic-refusal to main, and retryable 429 settlement. No
existing fake message IDs or shared control tests needed changes.

## Remaining gates

Nontext operations, edits without a durable attempt, historical unowned outputs and ledger retention,
unknown remote outcomes without positive evidence, managed settlement, native
login exit qualification and confirmed retirement orchestration remain open.
No absence is inferred from restart, elapsed time or canceled HTTP. Group topic
semantics and unregistered diagnostics retain their current paths. `/thread delete`
remains unavailable.

## Integration — 2026-11-06

PR #186 merged as `6faa197`; reviewed head `613edd3a816fe2bade013f2a37608a1763323e97`.
Required [CI 42209821093](https://github.com/Coriol-is/gopod/actions/runs/42209821093)
PASS. Independent exact-head review PASS; focused races Store 11.720s, Telegram
14.392s. Sonar analysis 11:22:56 UTC matches head: gate OK, five complexity smells
in ownership helpers/tests, zero bugs/vulnerabilities. Findings read, none suppressed.
Deployment awaits the combined batch; Pi remains `80f4b11` at this checkpoint.

Deployment closure 2026-11-06: #186/#187 shipped in `0cd8078`, with exact-main CI,
verified backup, health/state and Telegram version PASS.
[Rollout and cleanup](../deployments/2026-11-06-control-topic-rollout.md). Parent activation gates remain.
