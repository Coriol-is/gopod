# CP-002 generation-bound draft and Stop controls

Base `3a889c2`; reviewed source `74cef1d`; PR #178 merged as `da8b4fe`.

## Selected gap

Preview network sends already carry the captured final-generation context, but
`bindDraft` and `turnKeyboard` looked up the latest registration by turn ID.
A retired handler could overwrite/clear its successor's draft Stop target or
construct the successor's token-bearing keyboard. Keyboard construction also
used two separate registry reads, permitting a replacement between them.

## Change

Both helpers now resolve the captured registration under the registry lock.
An explicitly empty or mismatched binding never reacquires a later registration.
An unbound caller retains legacy lookup, while production streaming and wait
callers pass their registered context. Phase checks prevent final/retired owners
from creating preview controls; stopped/pending/failed Stop suppresses keyboards.
Wait-notice and private-control stoppability use the same captured lookup.
No protocol/schema/activation changes or new architectural decision.

## Verification

- Focused existing Stop/wait/control/final-handoff tests: PASS.
- New regressions, ten repetitions with race detector: PASS. Old bind/clear cannot
  change the replacement; old draft Stop cannot cancel it; the replacement's
  draft Stop remains functional. Explicit nil/mismatched bindings cannot acquire
  controls. Concurrent replacement and final-owner refusal are covered.
- Full build/vet/all tests: PASS; full Telegram race suite: PASS (63.360 s).
- Independent bounded source review: PASS; independent focused races PASS
  (12.358 s). No blocking findings; wait-map/control-binding exclusions retained.
- Required exact-head [CI 42070485100](https://github.com/Coriol-is/gopod/actions/runs/42070485100):
  test and Docker isolation PASS on `3428996`, no rerun.
- Advisory Sonar on `3428996`, 2026-11-05 17:01:36 UTC: gate OK, zero bugs/
  vulnerabilities, two code smells. Existing empty no-op unregister (`go:S1186`)
  and wait-refresher complexity (`go:S3776`) were read; none suppressed.
- Squash `da8b4fe` has identical application source/tests to reviewed PR head.
  Merge followed independent review and green required CI.

## Remaining boundaries

This slice does not make the wait-placeholder/refresher maps or durable control
binding generation-aware. General preview ownership, reply-action parity and
qualified foreground publication/cutover remain open. No deployment or new
native/client acceptance; Pi remains `757faa2`.
