# CP-002 generation-bound draft and Stop controls

Base `3a889c2`; branch `fix/preview-generation-controls`.

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
- Full build/vet/tests, full Telegram races, independent review and required CI:
  pending at this checkpoint.

## Remaining boundaries

This slice does not make the wait-placeholder/refresher maps or durable control
binding generation-aware. General preview ownership, reply-action parity and
qualified foreground publication/cutover remain open. No deployment or new
native/client acceptance; Pi remains `757faa2`.
