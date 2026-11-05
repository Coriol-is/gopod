# CP-002 wait-state and durable control generation ownership

Base `9de3cac`; reviewed source `699c0aa`; PR #179.

## Gap and implementation

Wait placeholders and refreshers were keyed only by turn/chat/message/topic. A
late handler could consume a replacement's placeholder, stop its refresher, or
replace its cleanup handle. A late HTTP response could also bind its placeholder
to the latest Stop registration and overwrite the turn's durable placeholder memo.

Wait keys now include the captured registration, preserved through retirement.
All production lookup/pop/stop paths pass the registered context. Duplicate
refresher start cannot overwrite an unjoined handle; concurrent stops both join
its done channel, and a sealed generation's refresher exits rather than retrying.

A metadata mutex serializes registration/replacement/unregister with control
binding and placeholder memoization. No network request holds that mutex; the
registry lock is released before Store calls. Stale captured registrations are
rejected before mutating control bindings, Stop allowlists or the durable memo.
An explicitly unregistered context can persist a placeholder only while the
registry is still absent, preserving existing non-stoppable sender behavior.
An already persisted historical control keeps its original token. A late HTTP
response from a retired generation remains isolated in its old wait key and
cannot establish a new active control binding; existing send ownership/receipt
handling is unchanged. No schema, protocol or activation change.

## Verification checkpoints

- Repeated new generation/race tests: PASS, including old cleanup versus successor,
  duplicate/concurrent refresher joining, sealed refresher exit and stale durable
  binding rejection. A blocked real fake-HTTP wait send returning after replacement
  cannot change the successor's wait cache, durable memo or control binding.
- First full test/race runs exposed an unregistered sender compatibility failure
  (`TestRunAgentStreaming_DraftUnsupportedFallsBackToEdits`, placeholder 0 instead
  of 77). Fixed the absent-registration case without allowing later recapture;
  added a regression covering both absence and a subsequent registration.
- Focused final compatibility/generation race tests, five repetitions: PASS.
- Independent review PASS; initial focused races 8.842 s and late HTTP test
  6.636 s; final compatibility delta review/races PASS (6.344 s).
- Final build/vet/all tests PASS (Telegram package 43.371 s).
- Final full Telegram race PASS (58.097 s). Required CI and advisory Sonar
  pending at this checkpoint.

## Boundaries

No foreground producer activation, reply-action parity, published-delivery
settlement or native/client qualification is claimed. Durable Store writes remain
best-effort under their existing error handling; this change fences registration
replacement, not a new transactional recovery protocol. Pi remains `757faa2`;
#178 and this slice are not deployed.
