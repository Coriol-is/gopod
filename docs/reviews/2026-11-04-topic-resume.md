# CP-016 durable main-message ownership

## Plan checkpoint

Scope: pre-dispatch host provenance journal for registered-private text sends
that target main, including source-topic fallback and normal main controls.
No text, login URL, code or credentials are stored. Acknowledged IDs bind to the
precommitted scope/source/target; crash gaps and uncertain sends retain unresolved
rows that block quiescence. Callback/edit lookup must prioritize durable provenance
before trusting an apparent main-chat context. Existing historical rows are not
invented or backfilled without evidence; unknown main controls expire while topic
lifecycle fences or unresolved ownership are present. Known main/sibling controls
retain independent authority.

Own files: store ownership schema/helpers/tests; Telegram ownership helper,
transport/callback/quiescence, narrow handler/streaming/login text send closures.
Parallel final-reply worker confirmed no overlap or schema migration allocation.
Root owns global HANDOFF/ROADMAP/BACKLOG. No destructive command activation or
live Telegram/auth/deployment work in this slice.

Implementation and regression validation in progress.

## Implementation checkpoint

Implemented a metadata-only `telegram_message_ownership` journal. Registration
snapshot, source/target and opaque attempt ID commit before the send; message ID
acknowledgement settles exactly that attempt. Definite refusal removes a pending
attempt; unknown result, process interruption or receipt-write failure keeps it.
Same-source independent output remains allowed, including after restart. Pending
attempts block quiescence, not general conversation admission.

Common rendered text, placeholders, private login text and error/retry replies
use the helper for registered-private main targets. Normal topic and group sends
retain their transport paths; lifecycle/restart/IPC notices without controls are
outside this provenance slice. No historical source guesses/backfill are made.
Lookup prioritizes persisted scope and source before the host cache or supplied
main context; stale scope and DB failures fail closed. Unknown main controls are
rejected while any lifecycle fence (including retired) or pending attempt exists;
known main/sibling controls remain available.

NoReplay errors prevent parse/keyboard/thread/429 retry of a failed ownership
operation. Stream placeholder failure drains the running result without sending a
replacement; acknowledged IDs remain available even if their receipt write fails.
The next independent operation can still send. Login preserves opaque NoReplay
without exposing credentials/errors. The backup count allowlist includes the table.

Focused tests cover preinsert/no-call, interrupted pending row across Store reopen,
unknown send, accepted-before-receipt failure, independent concurrent/later sends,
immutable source acknowledgement, stale registration, fallback callback after
cache loss, conflicting cache, known main/sibling controls, unknown legacy controls,
error-reply controls, unknown placeholder without fallback, and unregistered setup.

Initial full suite found the missing backup-count allowlist entry (fixed). A later
full run stalled in the silent-Stop test: it started streaming before registering
Stop, so streaming could capture nil. SIGQUIT located the waiting test/stream.
The test now registers Stop before starting its goroutine; production Stop code
is unchanged. Combined full/race validation follows integration of PR #169.

## Remaining activation gates

No `/thread delete` command is enabled. Unresolved attempts have no automatic
retry/TTL/reconciliation authority; they deliberately retain evidence. Historical
main-fallback controls without provenance can only be expired conservatively,
not reconstructed. Main-message ownership retention/compaction remains explicit
future work. Noncontrol lifecycle/restart/IPC output, ordinary native-login exit
qualification, managed queue settlement, remote ambiguity reconciliation and the
confirmed delete/outcome/local-retirement sequence remain outside this slice.
