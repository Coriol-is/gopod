# Durable delivery and uncertain outcomes

Date: 2026-10-26. CP-002. Status: **proposed design; not implemented or enabled**.
Planning wave: 1. [Proposal](../../proposals/CP-002-telegram-outbox.md) ·
[Common contract](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Executable plan](../plans/2026-10-26-reliability-delivery.md).

## Outcome and source baseline

A completed model turn must survive a failed Telegram delivery without rerunning the model.
A timeout after a possible send must remain visibly uncertain rather than silently duplicated.
Intermediate streaming remains best effort; personal text, generated code attachments and voice
behavior remain available during staged migration.

Source reviewed at `33e026a291fbe577efabf43e9d0b6e760fd19ece`:
`internal/store/{schema,store,turns,continuity,registered_chats}.go`,
`internal/queue/queue.go`, `internal/runner/runner.go`,
`internal/ipc/{messages,authz,receipts,namespace}.go`,
`internal/telegram/{streaming,resume,handler,document,longcode,voice,errors,login_handoff}.go`.
Today IPC sends text directly and remembers terminal outcomes in memory. Queue recovery can rerun
unfinished turns. `sendFinal` retries explicit 429; other helpers have formatting/topic fallbacks.
`CompleteConversation` records execution continuity before confirmed delivery. None is a durable outbox.
These observations establish the gap, not live acceptance.

## Shared scope foundation

CP-002 owns one additive foundation reused by CP-004/020/021/022; no competing namespace registry:

- Add `namespace_id TEXT UNIQUE` (host-generated UUID), `namespace_epoch INTEGER NOT NULL DEFAULT 1`,
  `registration_revision INTEGER NOT NULL DEFAULT 1` to `registered_chats`. Backfill each existing
  registration once under the migration transaction; identities are never reassigned to another chat.
- `ScopeRef` contains namespace ID, epoch, registration revision, trusted chat JID, diagnostic folder,
  thread ID, visibility and execution profile. JID/folder alone never confer owner authority.
- `Store.SnapshotScope(ctx, chatJID, threadID, visibility)` returns the registration snapshot;
  the host adds/checks profile and actor eligibility. `Store.ValidateScope(ctx, ref)` compares the
  current registration and retirement state. Both are required alongside actor/task grants.
- Any host registration/rebind/profile-policy mutation increments registration revision; namespace
  invalidation increments epoch. Thread retirement invalidates that thread's intents. Stale references
  fail closed. Future CP-020 namespace isolation uses these same identifiers.
- Initial admission is personal-only. Group execution/delivery remains closed until CP-020; CP-019
  determines protected service-response visibility. A private approval does not turn group work into
  a personal execution scope. No main-topic, owner-chat or public fallback is allowed.

This metadata does not implement CP-020 filesystem isolation or authorize all registered chats.

## Durable records and transaction boundaries

Names below are the implementation contract; additive SQL migrations must preserve existing rows.
All timestamps are UTC; identifiers are host UUIDs; hashes are SHA-256 over canonical bytes.

| Record | Required fields and constraints |
|---|---|
| `turn_results` | `result_id` PK, ScopeRef snapshot, execution kind, provider/model/profile fingerprint, immutable final text/hash, created_at; authority=`model_reported` |
| `turn_result_members` | result_id FK, turn_id FK, attempt; UNIQUE(turn_id,attempt), supporting coalesced interactive turns |
| `delivery_groups` | delivery_id PK, result_id nullable FK, ScopeRef, actor/task/run refs, delivery_owner, ordering sequence, expiry, immutable render-plan hash, aggregate state |
| `outbox_parts` | part_id PK, delivery_id FK, ordinal UNIQUE within delivery, exact method/payload or artifact reference, parse mode/markup, target placeholder if applicable, state, next_attempt_at, remote_message_id, reason_code |
| `outbox_attempts` | attempt_id PK, part_id FK, dispatcher fence, start/end, outcome class and sanitized diagnostic; never credentials |
| `ipc_message_requests` | namespace_id/epoch + request key UNIQUE, payload hash, delivery_id, accepted/rejected outcome; durable dedupe tombstone |

`FreezeTurnResult(ExecutionRef{memberTurnIDs/attempts, ScopeRef, provider/model, kind}, text)`
commits before runner emits its final Result or releases the execution slot. Interactive completion
may atomically perform the existing assistant continuity append once. **Isolated task completion
must not call foreground `CompleteConversation`, append foreground session history, or set
`turns.reply_msg_id`.** Task attempts reference this single result ID rather than copying final text.
Freezing a result is neither publication nor delivery and does not certify external side effects.

`PublishDelivery(resultID, immutablePlan, requestIdentity)` atomically inserts the group, all parts,
request dedupe record and artifact references. Frozen results can exist before publication while
artifacts are prepared; recovery finishes preparation/publication without native model replay.
No filesystem copy or network request occurs inside a SQL transaction. Store helpers accept the
existing transaction internally: the current single database connection makes nested Store calls unsafe.
Wake in-memory dispatch only after commit. Managed task admission uses the same transaction helpers.

## One delivery owner and immutable plan

Each execution selects one host-authorized owner: `legacy_agent_ipc` or `controller_result`.
Legacy scheduled agents keep IPC delivery; their stdout/result is never automatically published.
Controller delivery is opt-in for new managed work, never inferred from arbitrary model JSON.
An IPC retry uses the same request identity and bytes; different bytes under the same key conflict.
V0 identities use namespace/epoch + request filename; retained tombstones prohibit silent key reuse.

Freeze chunk boundaries, destination, thread, visibility, ordered attachments, parse mode and keyboard
before publication. Retries do not re-render or reread source files. A plan contains at most 32 parts.
A known placeholder final edit is an explicit part. Stop/deregister the streaming edit loop before
publication so it cannot race the durable final edit. Intermediate previews carry no delivery claim.

Keep one in-flight part per conversation (namespace/epoch/thread/visibility ordering key), ordered
by committed sequence and ordinal. An uncertain head blocks later sends in that conversation;
other conversations can progress. Initial dispatcher is one bounded worker under an exclusive
controller-instance lock. This is not a multi-host leasing protocol.

## State machine and transport contract

Parts: `pending -> sending -> sent | pending | uncertain | failed`; pending can become
`expired` or `cancelled`. Aggregate group state reflects every required part, including partial
acknowledgements. Before any API call, commit sending + attempt + fence. On restart, sending
without a persisted authoritative acknowledgement becomes uncertain; never reclaim it for send.

The durable adapter makes **one API attempt**. Do not reuse helpers that hide retries, send a plain
fallback after a timeout, or switch a missing topic to main. Classification:

- Valid explicit 429 refusal: known not accepted; honor retry_after within expiry.
- Proven pre-dispatch failure: retryable only when the adapter can demonstrate no request was sent.
- Explicit terminal refusal, revoked scope, retired topic or missing rights: failed/cancelled, no reroute.
- Timeout, connection loss after possible write, generic 5xx, malformed response, or crash around API
  acknowledgement: uncertain. A generic server error is not proof of non-delivery.

Default normal expiry: 24 hours; maximum five attempts. Known-unsent retry delays 1, 5, 15, 60 seconds
with bounded jitter; larger authoritative retry_after wins, but cannot exceed expiry. Expiry during
sending does not turn an ambiguous request into known-unsent. Deterministic formatting alternatives
may be precomputed in the frozen plan and selected only after an allowlisted explicit nonacceptance;
no fallback after an ambiguous outcome. Initial adapter may fail unsupported formatting safely.

Persist each successful remote acknowledgement separately. Record delivered `messages` history
for acknowledged content only; do not append execution continuity a second time. Set interactive
compatibility `turns.reply_msg_id` by compare-and-set only once the required final plan is acknowledged;
never use that field as the sole dedupe authority. Task-only results do not set this field.

Recovery checks frozen results/publications before invoking the runner. Delivery failure cannot return
a model-retry signal. A pre-freeze interrupted native execution has unknown effect status and requires
CP-021 reconciliation rather than inferred safety from missing text. Retry controls distinguish
status/redelivery from an explicit new model execution. An operator-requested uncertain resend creates
an audited new intent with duplicate warning, never rewrites the original outcome to pending.

## Protected login and visibility

Ordinary payload tables must not contain raw reusable login URLs/device codes. A protected adapter
stores an opaque challenge reference, generation, private recipient and expiry, resolving the current
challenge only at dispatch after owner/private eligibility checks. Require at least ten seconds of
remaining validity and bound network deadline to that validity. Lost-process references expire;
never restart login automatically. No claim is made about Telegram's eventual display time.
Keep current private login handoff until this adapter is qualified. Group requests never publish
challenge material to the group, even when the private recipient cannot be reached.

Receipts report accepted/queued, sent, partially sent, uncertain, failed, cancelled or expired.
Accepted means committed publication, not delivered. Persist sanitized reason codes; omit secret
payloads and private filesystem paths from group-visible diagnostics. CP-019 controls service responses.

## Retention, migration and release gates

Proposed defaults are policy/configuration, not existing behavior. Keep pending/sending/uncertain
records and their references until reconciled; quota pressure rejects new publication rather than
removing evidence. Keep terminal payloads 30 days; compact dedupe tombstones without payload indefinitely
until namespace retirement and explicit audit-approved purge. Bound queue bytes/rows with configurable
admission quotas; initial defaults 10,000 unresolved groups globally and 1,000 per namespace.

Feature flags default off. First qualify personal text IPC; retain legacy streaming foreground until
CP-003 supports existing generated-code attachments and automatic voice output. Drain old streams
before switching final ownership. Do not invent outbox acknowledgements for historical messages.
Artifact rollout requires CP-007 matched DB/artifact backup and restore verification; group rollout
additionally requires CP-019/020. DB schema being additive does not make old binaries safe: stop sender,
reconcile in-flight rows and fence/disable native recovery before rollback, or restore a matched snapshot.
An older binary must never rerun turns whose frozen results it cannot understand.

## Acceptance

Use fake transport and deterministic crash hooks, not production sends: crash before dispatch, after
remote acceptance/before commit, after per-part ack and during publication; no model rerun after freeze,
no automatic uncertain resend, no duplicate acknowledged prefix. Verify explicit 429, generic 5xx,
thread retirement, scope epoch/revision changes, queue ordering, coalesced results, IPC duplicate/hash
conflict, isolated task history separation, streaming final ownership and old-binary rollback fencing.
Source-backed planned tests/files and rollout evidence are in the executable plan. This design makes
no exactly-once Telegram guarantee and does not enqueue every preview/reaction by default.
