# CP-016 noncontrol text ownership — 2026-11-06

## Scope and implementation checkpoint

Base: `6c20646`, branch `feat/topic-retirement-next`. Existing D064 contract;
no schema or ADR changes. Canonical HANDOFF/BACKLOG/ROADMAP consolidation belongs
to the parent integration lane.

Restart notifications, lifecycle start/shutdown notices, and IPC text notices
now use the existing registered-private main-target ownership journal. Lifecycle
fallback retains the original topic even when Telegram receives a main message.
Successful ACKs retain metadata across restart; ambiguous sends and ACK failures
retain the existing pending evidence. No message body enters the journal.

IPC maps NoReplay errors to `ipc.ErrPermanent` while preserving their wrapped
error and marker. This prevents the watcher replaying a possibly accepted whole
message. Definite transient refusal remains retryable; partial delivery remains
permanent as before. Neither restart nor lifecycle adds retries. Existing
host-authenticated IPC scope validation remains before every chunk, with no
fallback from scoped notices.

The raw SendMessage audit now leaves only ownedTextSend and the separately
durable outbox transport. This is coverage of these text entry points, not all
Telegram effects. Topic-target sends retain their existing transport contract;
this slice does not extend the journal beyond private main targets.

## Validation checkpoint

New regression coverage: persistent actual-main versus original-topic fallback
origins, both cached-gone and fresh-refusal fallback, all three helpers under
pre-send DB failure, unknown remote outcome, positive receipt ACK failure and
cancellation, durable restart blockers, IPC multipart no-replay, and retryable
429 refusal. Existing IPC stale-scope, per-chunk deletion and no-fallback tests
remain in the targeted set.

Validation PASS:
- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `go test ./internal/telegram -run 'TestNoticeOwnership|TestIPCSink' -count=1`
- `go test -race ./internal/telegram -run 'TestNoticeOwnership|TestIPCSink|TestTopicOwnership|TestMessageReceipt|TestLifecycle|TestTopicFence' -count=1`
- `git diff --check`

Independent source review found no blocker; reviewer focused race and exact-head
CI remain integration gates. No deployment or live Telegram mutation performed.
An initial new-test assertion incorrectly required NoReplay from the lifecycle
pre-send admission failure; corrected to require failure and zero HTTP calls.
Production admission correctly returns an ordinary error before any dispatch.

## Remaining gates

Historical unowned messages are not reconstructed. Unknown remote outcomes,
pre-crash requests without positive evidence, media/other nontext effects,
managed settlement and ordinary native-login exit qualification still need
explicit qualification. Positive receipt repair remains bounded and DB-only;
this slice does not infer absence from timeouts or restart. The confirmed
API/outcome/retirement command is still absent; `/thread delete` stays disabled.
