# CP-002/003 durable upload alternative

Base `29eea1b`, branch `feat/delivery-outcomes`.

## Checkpoint 1: selected production gap

D052/D058 already authorize a document of the exact same immutable artifact after
an allowlisted explicit photo refusal. Today both HTTP calls share one database
attempt. This slice records the refusal and selects the document branch in a
transaction before the second HTTP call. The existing photo presentation marker
is the immutable branch policy; part/plan hashes and ready plans do not change.

The branch ledger binds the original part hash and refused attempt to its first
document attempt. Scope, exact claim, controller fence, expiry and retry budget
are revalidated. Known-unsent retries after selection stay on document; sending
left by a crash becomes uncertain and is never replayed. Ambiguous photo results
never select the alternative. Artifact, route, caption and reply target remain
unchanged; recovery performs no rendering, generation or billing.

Owned: store outbox branch/schema/backup registry, Telegram photo transport and
dispatcher, related tests and this report. Topic worker confirmed no overlap.
Root approved this bounded scope; global handoff/backlog belongs to root.

This is a production-wired upload-outcome prerequisite, not full conditional
code-inline fallback. Foreground freeze remains disconnected; preview,
cancellation, reply bookkeeping and conditional code content remain open.

## Checkpoint 2: implemented transaction and outcome tests

`outbox_document_fallbacks` binds immutable source hash/policy to the refused
photo attempt and first document attempt. The store verifies the canonical
persisted route/payload, fence, current open attempt, topic admission, expiry and
budget before committing. The transport now performs exactly one HTTP request;
the dispatcher owns the branch transaction. Known-unsent retries reload the
selected document presentation; unchanged source parts still carry the photo
marker and retain their original hashes. Diagnostics report acknowledged photo
alternatives as documents.

Focused tests cover the transaction visible before HTTP2, exact bytes/caption/
thread/reply target, explicit refusal allowlist, wrong status/code, ambiguity,
known-unsent/rate-limit retries, scope/topic fence, forged payload/route, stale
claim/fence, budget/expiry, concurrent selection, rollback on inserted failure,
commit-ack ambiguity, cancellation, accepted-but-unrecorded acknowledgement,
restart before/after selection, corrupt branch hash, old populated schema
migration and attempt-retention foreign-key cleanup. No source regeneration or
paid backend exists in this flow.

## Rollback compatibility

This is an additive schema migration, not a downgrade-compatible dispatch state.
An older binary ignores `outbox_document_fallbacks` and can retry a pending
selected document as a photo from the unchanged original part. Before downgrade,
inspect and settle selected pending work or hold dispatch until a compatible
binary is restored. Never delete the branch ledger to make a downgrade proceed.
An older backup verifier/restore allowlist may reject the new table/counts.
Old-to-new migration and hash preservation do not qualify new-to-old rollback.

## Validation checkpoint

Full `go build ./...`, `go vet ./...` and `go test ./...` passed, including
backup/schema registry coverage. Full store and Telegram race suites passed. Independent source review provisionally passed with
the downgrade boundary documented. No source changes followed that review.

The exact photo-refusal token allowlist is inherited from the existing D052
implementation; this patch neither broadens it nor claims new live evidence that
Telegram stored nothing. Existing real-library fake-HTTP tests preserve strict
matching and reject authentication/thread errors, generic 400s, 429s and unknown
outcomes as branch-selection signals. Live token qualification remains separate.

## Combined integration checkpoint

Merged receipt reconciliation (`930586a`, PR #171) from origin/main without
rebasing; merge commit `219cef3`. All ten delivery source/schema/test files are
byte-identical to reviewed `7a02087`. No conflict or integration source edit was
needed. Full build/vet/test and full affected Store/Telegram race suites passed
on this combined source; it was pushed as the final PR head.

## Integration result

Independent final review approved `7a02087` and the combined `219cef3`, confirming
all delivery source files unchanged and receipt source equal to its approved
version. PR [#172](https://github.com/Coriol-is/gopod/pull/172) passed both required
jobs in [CI 41950532576](https://github.com/Coriol-is/gopod/actions/runs/41950532576)
and Sonar gate OK, then squash-merged as `a016b82`. No production deployment,
foreground activation, billed API call or live Telegram acceptance occurred.
Downgrade and remaining foreground gates above remain mandatory boundaries.
