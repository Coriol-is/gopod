# Owner-private foreground delivery integration

Baseline: da529a8, with mixed reply-surface ordering checkpoint 7c15035.
Branch: feat/delivery-completion. This is connected, default-off implementation;
it is not production activation or completion of all CP-002/CP-003 scope.

## Production path

`GOPOD_OUTBOX_FOREGROUND` defaults false. Controller startup checks actual outbox
startup, not merely its configuration flag, then requires the owner and immutable
artifact preparation adapters before enabling the producer. Missing dependencies
fail requested startup instead of silently reverting a selected result to legacy.
Only the configured owner's registered private batch is selected; groups and
unregistered traffic retain their existing compatibility paths.

Batch admission captures reply mode and binds the complete ordinary attempt set
to runner result freezing. Sync, streaming and placeholder-failure drain paths
publish the already-frozen result after acquiring final ownership and joining
admitted preview writers. The immutable plan owns code documents, conditional
inline alternatives, final text/edit, keyboard retirement and prepared voice.
Preparation/publication failures carry NoReplay: a saved result is never a reason
to repeat native inference or send a second direct final answer.

Presentation input v3 captures overflow preview retirement. It edits the original
preview to bounded plain text with an empty keyboard before standalone text chunks.
Existing v1/v2 input identity and prepared plans are retained unchanged; recovery
still only publishes a ready saved plan, never renders, runs TTS or reads a mutable
workspace. Buffered voice-only placeholder deletion retains D071's exact outcome
journal; unknown deletion prevents further preparation/publication. Streaming voice
retains its established text-plus-voice behavior.

Final handoff seals new previews under the generation lock and joins previews
already admitted before preparing a competing edit. Stop admission is checked at
BeginFinalReply as well as staging/publication, so accepted Stop cannot start new
preparation of its frozen late result. A concurrent later Stop still fences final
publication through the existing transaction checks.

A separate delivery-awaiting reaction (`🕊`) stops the native-progress timer.
A complete durable ACK observes delivery success (`👍`), never publication alone.
The observation is best-effort UI and cannot create delivery/replay authority.
It is scoped to the current attempt, avoiding a stale ACK overwriting a retry's
status. No reply memo is populated before the existing all-part ACK transaction.

## Explicit unresolved gates

- Gone original topic: retain the frozen answer and report an error; no hidden
  main-chat reroute. The owner was asked whether to retain this behavior or design
  a frozen destination-override contract. No answer was received at this checkpoint.
- Eager stale keyboard cleanup is not implemented. Shared ordering revokes stale
  actions, but removing their keyboard requires exact retired-surface edit authority
  and durable edit evidence; ordinary newest-row authority cannot authorize it.
- Historical missing provenance and explicit unknown outcome reconciliation remain
  held. No age/absence-based success, retry or force-clear is introduced.
- Group/unregistered and wider nontext uncertainty coverage remains unqualified.
- Native/client tests, preview/Stop/full presentation qualification and rollout
  remain open. This branch does not enable foreground, topics deletion, managed
  tasks, workers or paused parallel sessions on production.

## Verification

Focused tests cover sync publication without direct final sends; missing freeze
NoReplay; streaming overflow retirement in the immutable plan; preview joining;
Stop before preparation; reply mode captured before a settings change; gone-topic
hold; buffered voice/text parity; and requested enablement with no started dispatcher.
Conditional-code overflow tests require preview retirement before standalone chunks.
Status tests cover the added distinct allowed reaction and all existing states.

Final build/vet/full tests and targeted race outcomes are recorded in the parent
handoff. Fake Telegram/API and runner tests do not establish native client rendering
or real provider continuity. Exact-main and Docker isolation CI remain release gates.

## Proposed ADR D072 addition — opt-in immutable owner foreground producer

Select one delivery owner before native execution and preserve it through completion.
Bind ordinary attempt identities to runner freezing, capture reply presentation mode,
and seal/join preview writers before preparing the frozen final plan. Only all-part
ACKs expose final action/memo authority. Preparation errors retain NoReplay and saved
result evidence. Preserve old presentation versions during recovery. Keep the new
producer off until remaining native/client and ambiguity qualification is accepted;
this contract does not authorize a different conversation target or group cutover.
