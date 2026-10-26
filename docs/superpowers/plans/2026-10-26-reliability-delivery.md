# CP-002/003 implementation plan: durable delivery and immutable artifacts

Date: 2026-10-26. Status: **executable proposal; no runtime implementation or enablement**.
Source baseline: `33e026a291fbe577efabf43e9d0b6e760fd19ece`.
[Outbox contract](../specs/2026-10-26-telegram-outbox.md) ·
[Artifact contract](../specs/2026-10-26-immutable-artifact-delivery.md) ·
[Common contract](../specs/2026-10-26-codex-agent-adoption-common-contract.md).

## Delivery order and invariant

Bound each numbered slice to one reviewable PR; split a slice further when its diff cannot be reviewed
independently. Flags stay off until their listed gates pass. No live Telegram, model, TTS or account calls
are required for local development. Source paths below exist today unless explicitly labelled new.
Future implementation checks are required; this docs-only planning change uses link/diff review rather
than waiting for runtime CI. No workflow configuration changes are included.

A model result, an authorized publication and an acknowledged remote send are three separate facts.
After a durable result, delivery failure must not trigger native model replay. A possible remote write
without acknowledgement is uncertain. Immutable snapshots plus SQLite require crash reconciliation;
there is no filesystem/database atomic transaction and no exactly-once network guarantee.

## PR 1 — Store identity, result and delivery foundations

Files: `internal/store/{schema,store,registered_chats,turns,continuity}.go`; new
`internal/store/{scope,results,outbox}.go` and corresponding `_test.go` files.

Tasks:

1. Add/backfill registered namespace UUID, epoch and registration revision transactionally; every
   registration mutation updates revision, explicit invalidation updates epoch. Define shared ScopeRef
   and SnapshotScope/ValidateScope interfaces. Keep host profile and actor authorization separate.
   Do not create a second scope registry for tasks or CP-020.
2. Add result/member, delivery/part/attempt and durable IPC identity tables with the spec's constraints.
   Add internal transaction-aware helpers: current Store uses one connection, so no nested transaction
   or callback to public Store APIs from inside a transaction. Expose immutable result freeze and
   publication APIs; dispatcher notification is after commit.
3. Add typed execution kind: interactive freeze can append continuity once; isolated task freeze must
   never append foreground history or set reply_msg_id. Multiple coalesced turn members share one result.
4. Add unique request identity/hash handling, immutable plan hashes, per-conversation sequence allocation,
   quotas and terminal tombstone compaction. Add compare-and-set compatibility reply memo.

Acceptance: migration from a pre-change fixture preserves registered chats/history; restart keeps IDs;
concurrent registration and publication reject stale ScopeRef; duplicate same hash returns same delivery,
changed hash conflicts; transaction rollback leaves no partial group; isolated task result has no foreground
history effects. Run targeted `go test ./internal/store`; tests use temporary SQLite, no production data.

## PR 2 — Single-attempt dispatcher and truthful recovery

Files: new `internal/telegram/{outbox_transport,outbox_dispatcher}.go` and tests;
`internal/telegram/{streaming,handler,document}.go` read-only reference initially;
`internal/store/outbox.go`; controller lifecycle wiring found during implementation.

Tasks:

1. Implement typed immutable text/edit/upload transport with one API attempt; separate it from existing
   fallback helpers. Fake transport returns explicit refusal, pre-dispatch failure, ambiguous error or ack.
2. Commit sending/attempt/fence before call; persist each acknowledgement atomically. Enforce exclusive
   controller instance and one in-flight part per ordered conversation. No transaction spans API calls.
3. Retry only proven unsent/429 within five attempts and expiry, respecting retry_after. Generic 5xx,
   timeout, malformed reply and restart with sending become uncertain. Uncertain head blocks only its
   own conversation. Revalidate scope/thread/visibility before every attempt; never main-topic fallback.
4. Implement bounded worker shutdown and restart sweep; diagnostic status reports partial acknowledgement.
   Persist sanitized reasons, not raw credential-bearing errors. Keep dispatcher disabled by default.

Acceptance: fake 429 delay/expiry, explicit refusal, generic 5xx, crash after fake acceptance before DB ack,
crash after part ack, stale revision, retired topic, ordering and worker shutdown. Assert no hidden second
API call or reroute and no resend of acknowledged prefix. `go test ./internal/telegram ./internal/store`.

## PR 3 — Personal text IPC durable admission

Files: `internal/ipc/{ipc,messages,authz,receipts,namespace}.go`, corresponding tests;
new Store request/publication helpers; existing IPC documentation/instructions located at implementation.

Tasks:

1. Route compatible V0 text requests to durable publication; bind namespace/epoch + filename + hash.
   Preserve thread zero for V0; owner folder alone no longer authorizes an unmanaged durable recipient.
   Leave legacy direct path behind a feature flag while source-by-source migration is reviewed.
2. Recreate receipts from durable state after crash; accepted means queued. Persist terminal dedupe before
   archive so failed archive/receipt does not cause a second publication after restart.
3. Separate actor grants from destination scope, apply personal-only gate, validate quotas/expiry and expose
   queued/sent/uncertain/failed status. Never auto-send legacy scheduled stdout in addition to IPC.
4. Add recovery integration that does not make legacy IPC delivery failure rerun a finished model execution.

Acceptance: restart between commit/receipt/archive, repeated filename, conflicting bytes, unauthorized
recipient, stale scope and group denial. Test legacy task IPC remains one owner even when stdout exists.
Run `go test ./internal/ipc ./internal/store ./internal/telegram`. Personal text pilot needs a bounded,
authorized live smoke later; local fake success does not certify deployed Telegram delivery.

## PR 4 — Immutable artifact inventory and secure preparation

Files: new `internal/ipc/{artifacts,capabilities}.go` and tests; `internal/ipc/{messages,namespace}.go`;
new `internal/store/artifacts.go` and tests; `internal/store/schema.go`.

Tasks:

1. Implement strict V1 schema and active per-run capabilities tied to ScopeRef and delivery owner.
   Revoke unaccepted requests at run end; document waiting for a committed receipt. Host workspace mapping
   accepts only ordinary output roots and blocks protected/external roots.
2. Implement rooted every-component nofollow, regular-file/hardlink checks and stable bounded source copy.
   Reserve full upper-bound bytes first; copy/hash/fsync private snapshots outside SQL; revalidate and mark
   inventory ready; atomically attach references with publication. Preserve duplicate admission behavior.
3. Enforce file/request/part and namespace/global quotas. Reconcile expired preparations/reservations,
   quarantined/missing/corrupt blobs and deleting states. GC never deletes a pending/uncertain reference.
4. Upload the exact verified bounded bytes/descriptor without a second pathname reopen. Keep ordered
   partial acknowledgement and safe explicit-refusal fallback rules from CP-002.

Acceptance: symlink ancestors/final paths, hardlinks, FIFO/device, traversal, protected roots, source race,
source removal after receipt, oversized files, quota races, disk full and interruption at every fsync/DB
boundary. Duplicate concurrent requests converge. Hash mismatch quarantines without upload. Test stale
capability, group denial and uncertain artifact pinning. Run targeted IPC/store/Telegram tests plus race
checks for new reservation/GC concurrency. No benchmark or security claim from synthetic tests alone.

Gate: CP-007 matched artifact manifest/restore and bounded backup leases must pass before artifact flag
is enabled; CP-007 baseline DB backup can proceed independently. Do not block that baseline on this PR.

## PR 5 — Voice and generated-code preparation parity

Files: `internal/telegram/{voice,longcode,document}.go` and tests; artifact preparation interfaces;
host configuration/grant resolver identified in implementation.

Tasks:

1. Adapt automatic oversized code documents into immutable snapshots before publication.
2. Add TTS preparation interface and explicit scope/backend/credential/cost authorization; fake backend
   tests only. Ambiguous remote generation becomes preparation_unknown and cannot silently rebill.
3. Route existing personal automatic voice behavior through immutable preparation while preserving its
   current selection semantics. Do not grant group access to personal API keys or create provider fallback.
4. Verify generated bytes and source text join one immutable plan with truthful partial delivery status.

Acceptance: unchanged personal feature selection, code attachment bytes stable, TTS unauthorized scope
rejected before call, ambiguous TTS not retried, cancellation/expiry, quota failure and no duplicate voice
upload. `go test ./internal/telegram ./internal/ipc ./internal/store`. Live TTS/billing qualification requires
its own authorized scope and evidence; personal foreground cutover waits for parity rather than dropping voice.

## PR 6 — Foreground freeze, streaming ownership and queue recovery

Files: `internal/runner/runner.go`, `internal/queue/queue.go`,
`internal/telegram/{streaming,resume,handler,errors}.go`, associated tests and Store result integration.

Tasks:

1. Pass trusted ExecutionRef/attempt membership to runner completion. Freeze text before final Result
   emission/slot release. Preserve interactive CompleteConversation semantics once; isolated managed tasks
   use result-only freeze and share result_id with CP-004/021. Commit before memory queue notification.
2. Stop preview edits before final publication. Freeze final placeholder edit/new-message/attachment plan;
   sync text and voice follow the same final owner. Drain old streaming runs before enabling new ownership.
3. Recovery checks frozen result and publication before runner invocation. A frozen unpublished result
   resumes preparation/publication; pending/uncertain outbox resumes delivery/status only. An interrupted
   pre-freeze attempt is unknown and passes CP-021 reconciliation, not automatic guessed-safe replay.
4. Replace managed delivery-error model retry with delivery status. Update Retry callbacks to distinguish
   explicit new native execution from status/redelivery. Per-part ack writes delivered message history;
   execution history is not appended twice. Tasks never acquire foreground reply_msg_id.

Acceptance: fake runner invocation count stays one across delivery failure/restart; coalesced turns share
one result; stop during send remains truthful; streaming timer cannot overwrite final edit; multipart
partial ack preserved; legacy IPC stdout never auto-publishes; isolated task history stays isolated.
Replace old tests that expect errReplyUndelivered to rerun managed model work rather than preserving that
unsafe assertion. `go test ./internal/runner ./internal/queue ./internal/telegram ./internal/store`.

## PR 7 — Protected challenges and remaining service paths

Files: `internal/telegram/login_handoff.go`, auth challenge resolver/command paths found by a complete
Send*/Edit* call-site inventory; new protected outbox reference adapter and tests.

Tasks: inventory every direct final send and classify preview, final user result, service notice, reaction
or protected login. Migrate deliberately rather than enqueue every edit. Add opaque challenge refs,
generation/private-recipient checks, ten-second minimum validity and bounded request deadline. Keep raw
URL/code outside ordinary durable payloads/logs. Preserve group-to-private handoff authorization; missing
private delivery never routes publicly. Qualify CP-019 visibility adapter before group service cutover.

Acceptance: expired/revoked/lost-process challenge never resolves; wrong recipient/group route denied;
private reachability failure contains no challenge in group receipt; slow network timeout is uncertain,
not evidence that a challenge was never displayed. Current private handoff remains until qualification.

## PR 8 — Operational acceptance and staged release

Files: implementation/operator docs and feature configuration; no workflow changes prescribed.

1. Run full repository required checks after implementation, plus targeted race tests and deterministic
   crash matrix. Inventory all final paths to prove no bypass before enabling foreground flag.
2. CP-007: verify a matched DB/artifact backup, interrupted-copy reconciliation, restore into isolated
   nonpolling environment and hash/reference verification. Restore must not launch another poller/sender.
3. Pilot personal text IPC, then files, then parity-qualified voice/code and foreground. Validate Pi disk,
   memory, queue latency and quotas on the exact device without claiming local tests prove Pi behavior.
4. Observe status/receipt semantics and uncertainty reconciliation with bounded authorized smoke. Groups
   remain closed until CP-020 isolation and CP-019 visibility acceptance; do not infer permission from getMe.
5. Before rollback stop sender, journal/reconcile potentially sent parts, pin artifacts and disable native
   recovery for frozen results. Old binaries do not understand new result fences; additive schema alone
   is insufficient. Use a guarded compatible binary or restore a verified matched snapshot without replay.

## Review and final acceptance checklist

- One namespace foundation; actor roles and delivery-owner choices are host-authorized.
- Result freeze precedes native completion publication; isolated task/history boundaries are explicit.
- No automatic model replay after result freeze; no blind retry of uncertain remote effects.
- No hidden topic/public/private fallback; exact immutable payload and ordering survive restart.
- File preparation and DB publication have an explicit reconcile protocol, quota and GC protection.
- Generated code/voice personal defaults survive staged migration; no new API billing is inferred.
- Protected challenge data never enters ordinary outbox or group-visible errors.
- Matched backup and old-binary rollback fence verified before enablement; groups independently gated.

There are no unresolved product questions blocking these bounded implementation slices. External live
qualification, CP-019/020 group acceptance and CP-007 matched restore are release gates, not claims that
this planning change has executed them. Any new implementation evidence that weakens these assumptions
must update the canonical specs before enabling the affected path.
