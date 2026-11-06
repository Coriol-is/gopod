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
