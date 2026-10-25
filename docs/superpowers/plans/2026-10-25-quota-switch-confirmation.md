# Confirmed quota fallback implementation plan

1. Preserve failed-run provider identity in the quota error; add expected-provider
   compare-and-switch inside the existing chat agent/lifecycle locks.
2. Add a hidden confirmed Router command, durable quota offer validation and
   lookup of an existing retry by source identity.
3. Render single-use confirmation in the original topic; enqueue a deduplicated
   original-input retry only on Yes, and edit the prompt to its result.
4. Verify no pre-confirm mutation, cancellation, expiry, foreign scope, stale
   choices, concurrent double tap, retry metadata, failure notices, hidden menu
   entry, and second quota handling without a switch chain.
5. Update CP/ADR/CONTROL/HANDOFF, then build, vet, full tests and race tests;
   independent review and green CI before merge. No deployment in this task.
