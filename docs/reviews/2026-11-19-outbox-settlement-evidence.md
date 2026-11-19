# CP-002 exact edit settlement evidence

Base: `59fcd55`; branch `fix/outbox-settlement-evidence`.

An uncertain immutable edit targeting message 42 accepted operator evidence
`sent:99`. This could resolve uncertainty and, for a completed interactive plan,
grant reply memo/surface authority to a different physical message.

The regression failed on the original implementation with
`mismatched edit evidence accepted: <nil>`. Settlement now reads kind and target
in the same transaction and rejects contradictory positive evidence before writes.
The owner-only listing exposes kind and target without message payloads. Existing
confirmation, exact-target settlement and proven-unsent cancellation remain.
No new Telegram request, schema, replay, activation or reconciliation authority.

Full `go build ./...`, `go vet ./...`, `go test ./...` passed. Targeted
Store/Control race checks passed. Tests establish retained uncertainty/tail blocking,
exact-target progress, unsent cancellation and prevention of false reply authority.
Independent review found no blockers in source/tests; no pending review findings.
Required PR/main CI, merge and controller deployment subsequently passed; see
[release evidence](../deployments/2026-11-19-outbox-settlement-rollout.md). Broad native/client
acceptance is deferred to the owner-approved final campaign.

Historical ownership and legacy-journal unknown reconciliation remain open. This
check validates consistency of owner evidence; it does not prove a remote outcome.
