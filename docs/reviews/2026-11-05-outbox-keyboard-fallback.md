# CP-002 durable keyboard-refusal fallback

Base `167381a`; reviewed source `5f92f9a`; PR #180 merged as `6722e6a`.

## Selected parity gap

The ordinary final sender preserves an answer after Telegram explicitly refuses
its keyboard by sending without buttons. Durable outbox instead failed that text
part. This slice carries the existing presentation fallback into the outbox's
one-HTTP-call-per-persisted-attempt contract, without activating foreground output.

## Implementation

Only an explicit Bad Request classified as keyboard refusal, for text/edit with
stored markup, can select the bare-keyboard alternative. Selection closes the
refused attempt and commits a distinct attempt plus immutable source-hash ledger
before HTTP. The original part bytes/hash, destination and reply/edit targets do
not change. The effective request uses an explicit empty keyboard, including edits
that must clear old Stop controls. The real transport accepts that exact host form.

Known-unsent retries reload the bare selection; restart while sending remains
uncertain. Selection validates owner fence, current open claim, stored part/scope,
topic state, expiry and retry budget. An uncertain selection commit never triggers
another API call. Corrupt branch hashes fail closed. Diagnostics report acknowledged
parts sent without buttons; backup counts include the additive ledger.

HTML/plain fallback composes in either order, with separate attempts and correctly
updated transport attempt IDs. Both alternatives are bounded. The existing one-shot
plain fallback policy remains: rate-limit/not-sent after plain conversion is terminal.
No fallback is authorized by timeout, generic refusal, 5xx or a missing HTTP 400.

## Verification checkpoint

- Store/transport focused race suite PASS, including existing photo/plain fallback.
- New coverage: scope/topic/fence/budget/expiry/canonical-part rejection; concurrent
  selection, rollback, sending/known-unsent restart, populated-schema migration,
  hash preservation/corruption, and ledger retention cascade.
- Text/edit composition matrix verifies committed attempt identity before each
  call, immutable plan and unchanged targets. Actual bot transport tests verify
  explicit empty markup and committed refusal/branch before the second HTTP call.
- Unknown transition commit, accepted-but-unrecorded outcome and cancellation after
  selection retain conservative behavior; no blind second send/replay.
- Independent source review PASS; focused races PASS (Store 7.957 s, Telegram
  8.121 s), including real transport.
- Full build/vet/all tests PASS; full races PASS (Store 71.324 s, Telegram
  97.270 s).
- Required exact-head [CI 42090851015](https://github.com/Coriol-is/gopod/actions/runs/42090851015)
  PASS on `6dde8c9`: test and Docker isolation, no rerun.
- Advisory Sonar exact-head `6dde8c9`, 2026-11-05 19:07:34 UTC: **ERROR** due
  to new-code duplication 22.4% (threshold 3%); 18 code smells, zero bugs and
  vulnerabilities. Duplication is with existing document fallback and tests;
  complexity/context findings were also read. Persistence deliberately uses a
  bounded context independent of canceled HTTP. Nothing suppressed or marked
  accepted. Merge follows the documented Sonar soft-gate policy.
- Squash `6722e6a` source/tests match the reviewed PR head. Independent review
  and required CI passed before merge.

## Scope and rollout boundaries

No paid inference, native/client acceptance, production mutation or foreground
activation. Pi remains `757faa2`; #178–#179 and this slice await deployment.
Full reply-action memo/callback parity, newest-row handling and integrated preview/
Stop/publication qualification remain open. This is only refusal-path parity.
The additive ledger must be retained on restore; do not downgrade across pending
bare-keyboard selections to a binary that cannot honor them. Existing complete
IPC/session restore qualification remains separate.
