# D070 direct edit outcomes

Base `8dcc4f9`, branch `feat/durable-edit-outcomes`.

## Checkpoint 1: inventory and scope

Seven production HTTP edit writers exist: streaming plain/HTML (two), generic
keyboard replacement (one), durable/legacy More/Back (two), D069 repair (one),
and outbox PartEdit (one). No caption/media/live-location edit exists. This slice
journals the five direct wrappers for registered private messages with durable
creation/control/ACKed-outbox provenance. D069 and outbox retain their own attempt
ledgers. Group and unregistered diagnostic compatibility stays explicit and is
not claimed covered. Historical registered-private messages lacking durable
provenance fail closed; process-local caches cannot invent authorization.

Pending direct edits fence the raw target across restart/scope drift and prevent
conflicting repair/outbox/direct work. Admission and pending insertion are one
transaction; raw pending/sending/uncertain outbox edits symmetrically block direct
work. A creation receipt proves provenance, not permission to overwrite a final.
Only exact eligible durable callbacks may change ACKed final markup. Unknown
outcomes forbid fallbacks/replacement sends; receipts repair SQLite only. D070 is
root-owned and included verbatim with source. No foreground/delete/managed activation.

## Checkpoint 2: implementation and review corrections

All five direct HTTP writers use the new adapter inside existing message gates
and transport leases. An atomic admission revalidates durable provenance, supplied
source, registration and raw pending effects before insertion; final outbox markup
additionally reuses the transaction-local acknowledged callback read model.
Registration removal between resolution/admission fails closed. Repair and outbox
exclude pending direct targets before batch/claim limits; idle/backup and exact
topic quiescence include direct work. A text ACK retires only its matching captured
terminal control checkpoint; markup never asserts text ACK. Checkpoint persistence
failure now stops before HTTP.

NoReplay is propagated through plain/HTML/bare fallback, normal streaming progress,
overflow placeholder creation, terminal-error cleanup, silent summary, voice-only
placeholder fallback and wait-placeholder handoff. Unknown outcomes cannot reach
replacement sends/TTS. Missing historical authority is a known-unsent hold even
through the unknown-owner transport lane; it does not latch chat-wide uncertainty.
Groups/unregistered compatibility remains explicit, as do DeleteMessage and other
non-edit/nontext operations outside this inventory.

Tests now use exact target IDs in successful fake edit receipts. Fresh registered
callback fixtures carry real host creation evidence; dedicated tests retain the
historical no-evidence rejection and malformed receipt behavior. Focused store and
Telegram regressions pass; the complete suite is being reconciled against those
intentional compatibility boundaries before final validation.

## Checkpoint 3 — full-suite integration correction

Independent focused race review passed for D070/D069, durable More/Back and fresh
command/Stop paths. The first complete build/vet passed; complete tests and
store/Telegram/backup/control race each found the same single fixture mismatch:
quota confirmation targeted invented message 600 although its actual host-created
quota offer had receipt 500. The test now presses that real offer, retaining the
production ownership journal rather than manufacturing authority. Historical
unknown-owner rejection remains independently covered.

An overlay restoring the prior ignored-error behavior in silent overflow and
voice-only Finished fallback fails both new regressions: each proceeds to paid
fake generation after an unknown edit. The actual silent test exceeds the 4000
byte edit limit (5000 bytes), and both tests enable the fake TTS credential.
A checkpoint-persistence failure regression also proves zero HTTP dispatch.
Final complete validation follows this fixture correction.

## Operational boundaries

`telegram_edit_attempts` is an additive schema/backup-manifest change. Pending
rows have no cascading parent foreign key or TTL and cannot be deleted by normal
cleanup; settled rows also remain retained. Older binaries cannot honor this
barrier or restore the new manifest table list: unresolved rows prohibit a blind
downgrade. A retained in-process exact positive/refusal receipt repairs SQLite
only; losing that bounded receipt map on restart leaves the attempt held, without
HTTP replay or guessed settlement. No operator force-clear is introduced.

This closes the inventoried registered-private direct-edit boundary only. Group
and unregistered edits, historical targets without durable provenance,
DeleteMessage, other non-edit effects and remaining foreground presentation gates
are not qualified by these tests. Historical registered-private callbacks may
hold instead of mutating old messages; a fresh host-created control has durable
provenance. No foreground producer, topic deletion, managed mode or provider call
is enabled by this change. Docker isolation remains the CI gate.

## Final local validation

- `go build ./...` and `go vet ./...`: PASS.
- `go test ./...`: PASS (Telegram 49.996s).
- `go test -race ./internal/store ./internal/telegram ./internal/backup ./internal/control`:
  PASS (Telegram 65.931s; other three packages cached from the preceding run).
- Independent focused race review: PASS; final quota/checkpoint delta separately
  reviewed and exercised under race (7.397s).
- `git diff --check`: PASS. Fetched main remains base `8dcc4f9`.
- Negative overlay: both old ignored-error paths fail their regression as expected.

No live Telegram/API/provider execution, deployment or production mutation was
performed. CI and exact committed-head reviewer binding follow in the PR.

## Integration — 2026-11-06

PR #191 merged as `6820422`; exact reviewed head
`a47d53beaad68aab9d93839cc550cdbeabf391e6` PASS. Independent focused race
Store10.932s/Telegram12.354s and final quota/checkpoint delta7.397s PASS.
Required [CI42232618774](https://github.com/Coriol-is/gopod/actions/runs/42232618774)
PASS. Sonar14:17:40 UTC matches head: OK,26 code smells (complexity/parameter count,
existing lifecycle/literal comments), zero bugs/vulnerabilities; read, none suppressed.
No production source changed after reviewed corrections. Rollout tracked in handoff.
