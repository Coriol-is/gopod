# CP-002/003 delivery resume: immutable presentation parity

Base: `71ff629`, branch `feat/delivery-resume`. This is a prerequisite to D063
foreground cutover; the production producer remains disconnected.

## Checkpoint 1: code-backed scope

The explicit frozen planner diverges from production presentation in three
observable ways: sync voice is ordered after text instead of before it; every
text chunk replies to the source instead of only the first; code documents carry
a reply target that the existing document path does not set. This slice corrects
these differences and compares immutable output to the actual direct path using
fake Telegram and fake speech. Artifact bytes, captions, markup placement,
Unicode chunking, preparation failure and streaming selection are part of that
comparison. No provider or Telegram network calls are needed.

Owned files: `internal/telegram/final_reply.go`, final-reply tests and this report.
No schema, handler, streaming, queue, runner or topic lifecycle edits are planned.
Root owns global handoff/backlog updates; CP016 has separate file ownership.

Conditional upload-time code fallback remains open: it needs a persisted branch
policy, explicit refused-versus-ambiguous outcome handling, transactional dispatch
selection and restart tests. Re-rendering on recovery or falling back after an
ambiguous upload is not an acceptable substitute. Preview ownership, late
cancellation, reply-action handling and native producer wiring remain open too.

## Checkpoint 2: implementation and differential verification

The planner now orders sync voice before code documents/text and streaming voice
after them. Only the first text chunk carries the source reply target; documents
carry none, matching the production helper. Voice retains its own reply target.
Preparation still precedes publication, so ordering changes do not introduce
network effects during planning. Input/schema versions are unchanged: preparing
intents select the same presentation, while already-ready plans return before
rendering and retain their exact previously frozen parts (including old routing).

The 12-case differential test compares the stored plan with the actual direct
send helpers: operation order, rendered chunks, document bytes/names/captions,
reply targets, markup and spoken text. It also verifies repeated publication does
not regenerate audio or change output. Cases cover sync and streaming voice,
multiple/medium/code-only/unclosed blocks, long Unicode and failed snapshots.
Focused frozen-final tests passed. A dedicated regression preserves older ready
plans byte-for-byte. Full build/vet/test and relevant race verification follow.

## Checkpoint 3: falsification and remaining gates

The new differential suite was run against the unmodified base implementation.
All four selected cases failed: `sync_voice_and_text`,
`sync_voice_before_document`, `stream_voice_after_document`, and
`unicode_multichunk_stream`. The working implementation passes those cases.
This demonstrates order/routing defects rather than tests that merely mirror the
new implementation.

This slice qualifies successful output presentation and snapshot-failure inline
behavior only. It does not qualify delivery-outcome parity: an uncertain voice
upload can still block subsequent outbox parts, unlike the direct helper, and a
failed document upload still needs durable conditional inline content. Neither
case authorizes duplicate/ambiguous retransmission. A ready plan from the older
planner remains authoritative; it is never rewritten to adopt these corrections.

Next implementation decisions remain finite: persist code fallback alternatives
and outcomes; qualify refused/ambiguous upload ordering; settle preview ownership
and late cancellation; bind reply-action acknowledgement bookkeeping; then wire
foreground production and run staged acceptance. No runtime flag, producer call,
schema change, deployment or live acceptance is part of this patch.

## Verification complete

- `go build ./...`, `go vet ./...`, `go test ./...`: PASS.
- `go test -race ./internal/telegram`: PASS (full affected package).
- Final `go vet ./internal/telegram` and uncached differential/old-ready tests:
  PASS after baseline falsification and restoration.
- `git diff --check`: PASS. `PrepareFrozenFinalReply` still has no non-test caller.
- Required CI/Docker isolation and independent source review remain merge gates
  owned by the orchestrator; no merge or deployment was performed here.

Independent review: PASS, including reviewer-focused race checks. Reviewed source
hashes match the committed implementation:
`final_reply.go` SHA256 `db1e48f6645d8a7ff6b51ab27da8945e318d1d30965b1f0f21d557029200a3fb`;
`final_reply_parity_test.go` SHA256 `d427bc9b7a47a64b3bd92992ea245c8e5ea268c078baa7d5c2ae6ed1b0c5fa1d`.
Implementation commit: `904a632`; only this review evidence was appended afterward.
