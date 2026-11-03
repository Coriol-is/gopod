# CP-016 durable outbox fences — 2026-11-03

This slice connects the existing topic deletion journal to durable delivery. It
adds no deletion command, Telegram deletion call or production activation.

- New publication and conversation validation refuse deleting/uncertain topics,
  and refuse confirmed deleted/retired topics. Artifact publication uses the same
  transaction helper, so uploads cannot bypass the publication fence.
- Claim selection skips pending delivery for deleting/uncertain topics without
  consuming attempts or blocking other conversations. A definite deletion refusal
  restores dispatch of those same pending parts. Delivery expiry still applies.
- Confirmed deletion cancels remaining pending parts on the next claim pass,
  including future retries and tails behind sending/uncertain heads, with
  `topic_retired`, never
  rerouting them to main. Existing sending/uncertain parts keep their evidence;
  acknowledgements and uncertainty settlement remain writable.
- A new plain-text fallback attempt is refused after a deletion intent begins.
  Already issued remote work is not relabelled as unsent.

## Boundary before exposing `/thread delete`

These are transactional publication/claim fences, not a network quiescence
barrier. A claim already committed before the intent may still issue its remote
call. The future orchestrator must fence admission, stop/wait for native and
transport activity before deleting the Telegram topic, and reconcile ambiguous
outcomes. Queue/recovery, streaming/edit fallback-to-main, callbacks, counters,
registration changes and operator reconciliation remain open. Do not enable the
deletion command from this slice alone or claim that all late replies are fenced.
IPC artifact authorization currently classifies retired-topic errors as transient
`durable_unavailable`; before enabling topic-directed IPC, map confirmed retirement
to a terminal recipient-unavailable response while unresolved deletion stays held.
Current durable IPC destinations are main-only.

## Verification checkpoint

Tests cover each deletion state, refusal reopening the original delivery,
uncertainty never reopening on refusal, main/other-topic progress, zero attempts
while held, durable hold after store reopen, pending-only cancellation, truthful
late acknowledgement and blocked plain fallback. Full build/vet/test and affected
race suites passed. Independent re-review passed after the cancellation fix.
PR CI/merge remain pending. Initial review caught a
pending tail hidden behind an uncertain head; the cancellation sweep now runs
independently of dispatch eligibility and has a sending/uncertain/delayed
regression. No live topic was deleted.
