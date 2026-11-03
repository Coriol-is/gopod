# CP-016 topic admission and fallback fences

## Implementation checkpoint

The next slice checks durable deletion intent/tombstone state at Telegram ingress
(before persistence, login, commands and media processing), callback dispatch,
and native runtime setup after the per-chat slot is acquired. Main, group and
unregistered public diagnostic paths keep their prior behavior. A refused intent
reopens admission; deleting/uncertain and deleted/retired remain fenced across
restart. Inbound messages cannot erase a durable tombstone.

Text, document, voice, error and lifecycle fallback-to-main paths recheck the
original topic. Drafts, placeholders and individual text chunks also check before
issuing a new request. An observed `TOPIC_CLOSED`/thread-not-found alone still does
not create a deletion intent or authorize destructive cleanup.

This is a bounded read/admission fence, **not full quiescence**. Existing native
executions, API requests already issued, threadless edit helpers, streaming/counter
cleanup, callback admission races and registration-change reconciliation still
need coordinated ownership/cancel/wait before `/thread delete` can be exposed.
No deletion command, remote deletion or new feature flag is introduced here.

Production installs the actual SQLite Store as the admission-policy registration
reader (separately from the session-state adapter), including legacy session
mapping. The no-container profile has no native Runner. Synthetic/legacy callers
without scope-authority methods retain compatibility; production does not use
that fallback.

## Verification checkpoint

Targeted regressions and full build/vet/tests passed. Coverage includes refusal
reopening, persistent admission after store reopen, unrelated main/topic/group
and unregistered public access, expired callbacks, closed-store refusal, no API
requests for guarded egress behind a durable intent, deletion appearing during a
failed send, and deletion between text chunks (including main fallback chunks).
Native setup is refused before container ensure; group nonzero-thread and legacy
reader compatibility are covered. Full Telegram/runner race suites and three repeated targeted race runs passed.
Final full build/vet/test and diff hygiene passed. Independent review and exact
CI remain parent integration gates. No live deletion performed.

## Independent-review correction

The streamed final fallback initially checked the source topic once and then
lost it while sending to main. The source now stays in a host-created context
through nested text chunks, 429 retry and long-code document uploads. Regressions
begin deletion after the first request and assert no subsequent content request.
This preserves the read-fence boundary; already-issued requests still need
transport quiescence before deletion is exposed.
