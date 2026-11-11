# CP-016 confirmed topic deletion orchestration

Baseline: `da529a8`. Implementation branch: `feat/topic-retirement-completion`.
This is a connected, default-off command/orchestration slice. It does not claim
full CP-016 qualification or authorize deletion of existing production topics.

## Implemented

- `/thread info` through the control Router shows local topic state. In main it
  lists pending deletion operations for the current namespace, including revision
  drift; this is not Telegram topic enumeration or an existence probe.
- `/thread delete` is owner-private and nonzero-topic only. The control-side opaque
  confirmation additionally binds actor, chat, thread and full registration scope
  for five minutes. It is single-use; foreign/expired/stale confirmations do not
  grant deletion. Existing generic Yes/Cancel UI remains in use.
- `GOPOD_TOPIC_DELETION_ENABLED` defaults off. The command reports unavailability
  while off, and creates no intent or confirmation. Turning it on before the
  qualification gates below pass is not part of this slice's acceptance.
- Accepted deletion persists intent before entering a bounded worker channel. The
  worker is joined by Bot.Run and runs outside the originating callback ingress
  lease, so waiting for that lease cannot deadlock the command itself.
- Existing QuiesceTopic joins ingress/native/queue/transport and rejects unresolved
  ownership, native claims, login, history and remote ambiguity. No global container
  restart or cancellation of unrelated main/background work is added.
- `dispatch_started` is an additive topic_deletions column. Existing rows default
  to 1 and never acquire retry authority. Fresh intents start at 0; a CAS records
  1 before the exact DeleteForumTopic request. Failed preflight can record refusal
  only with dispatch_started=0. There is no automatic HTTP retry.
- Confirmed success retires context; definite API refusal reopens only the matching
  intent; unknown responses remain held. Fixed main-chat status notices contain
  no topic content and use normal durable message-ownership transport.
- A total of 64 slots is reserved before intent admission, covering queued,
  executing and unsettled work. Exact API outcomes are retained before persistence;
  transient outcome/cleanup/reopen failures receive SQLite/runtime-only retries.
  After settlement, one claimed fixed main-notice attempt is made, including
  delayed success. The notice itself is never replayed. Preflight refusal also
  retains failed reopening work, and cannot be replaced by a new intent.
  Receipt loss across process crash leaves the durable attempt held honestly.
- Startup and bounded periodic local cleanup consider only durably confirmed
  deleted intents. Each pass rotates through at most five records with one-second
  per-record and five-second total deadlines. It joins
  the current native/queue/transport gates before retirement, never repeats delete
  HTTP or a success notice, and never cleans deleting/uncertain intents.
- Backup work evidence now counts deleting/uncertain/deleted topic intents.

## Integration boundary with CP-018

The provider agent owns the new preference schema and helper
`retireConversationPreferencesQ(ctx, q, ref, thread)`. Integration must invoke it
inside RetireDeletedTopic before the final retired write. This branch does not
reference an absent helper or create a competing preference table.

## Qualification gates still open

1. Historical and nontext legacy output ownership plus explicit reconciliation of
   unknown remote outcomes remain prerequisites from D064. A gate-off command is
   not evidence these are solved. Refusing unknown work is deliberate.
2. Ordinary native login lifetime is refused while active. Managed work/unknown
   execution claims block rather than being force-settled. Paused task credentials
   and CP-037 work are not activated.
3. Native/provider and Telegram-client acceptance must use an expressly authorized
   disposable topic, coordinated with the root agent; no real topic was deleted.
4. A local pending-operation inspector does not provide an operator command to
   assert an unproven outcome. Unknown delete API outcomes are not replayed or
   cleared by a generic 400, TOPIC_CLOSED, missing-topic error, or age.
5. Existing transient controls are rendered inert by the tombstone and normal token
   expiry. This does not claim deletion of shared files, memories or audit data.
6. CP-018 preference retirement requires the integration hook described above.

## Verification

Focused command, dispatch, synthetic API, restart and exact receipt fault tests
pass. They cover actor/scope/expiry/re-registration and double confirmations,
private/group/main/CLI rejection, feature gate, exact API IDs, definite refusal,
unknown/false API result, duplicate dispatch, joining the originating ingress,
old-schema no-retry defaults, DB ACK failure followed by receipt-only recovery,
preflight uncertainty, confirmed local restart cleanup and backup busy evidence.

Full build/vet/tests passed after the retry/capacity review fixes (Telegram
57.668s); focused race tests passed (Telegram14.889s, control8.190s, store8.998s).
The final Russian status text and actual middleware-to-control main inspector test
passed focused command/Telegram tests. Root review identified
and fixed retryable refusal reopening, delayed result notification and repeated
local-cleanup gaps. Independent exact-head review and required CI remain integration
gates.
No deployment, external Telegram call or provider inference was performed.

## Exact external deletion observation follow-up

The same default-off gate now reaches Bot.Deps, not only the command callback.
When enabled, an owner-private text send can record an external deletion only
from a typed BadRequest whose complete SDK description is the known exact
TOPIC_DELETED spelling. CLOSED, generic400, not-found, network errors, substring
lookalikes, non-owner, main fallback targets and gate-off paths do not retire data.
This is explicitly observational error handling, not a documented Telegram event.

The send must already own durable pre-HTTP registration and exact nonzero
source/target provenance. One SQLite transaction verifies it, settles only that
refused send, and records the observation attempt ID on a deleted-topic intent.
No current registration is substituted for an old captured scope. An observation
preserves an existing intent ID, including an earlier refused command, so its
native/queue/transport fence cannot become orphaned. A queued command or obsolete
refusal/unknown receipt is converted to local settlement, releases its reserved
capacity after retirement and never reopens or issues DeleteForumTopic.

The caller returns NoReplay even though the send was definitely refused: outer
legacy delivery must not reroute context from a now-deleted topic into main. A
non-waiting serialized fence installation stops new work without joining the
current transport lease; the existing worker joins it later. Observation/DB failure
leaves pending send provenance and a truthful hold; it is not permission to infer
success, discard evidence or fall back. Exact observation settlement is atomic;
this slice does not add post-crash reconstruction of lost response evidence.

Confirmed observations use the existing bounded restart/local-cleanup path with
no deletion request or replayed notice. An unrelated external observation creates
no new notice promise; main `/thread info` exposes its pending local state. A
previous accepted command retains one truthful result notice through its receipt.

Focused tests cover exact classification, flag/owner boundary, main fallback and
ACK rejection, atomic DB rollback, re-registration, existing ingress drain,
restart recovery, and both queued-command and pending-refusal races. Nontext
sends/threadless edits without equivalent exact physical-topic provenance remain
outside this observation slice. Historical unknown effects and live native/client
qualification still gate activation; this follow-up does not complete all CP-016.

Post-retirement runtime cleanup removes the exact folder/topic auto-compact count
only after native work has joined and SQLite retirement succeeds, both for command
receipts and periodic observation/restart cleanup. Main, siblings, other folders,
shared activity, draft capability caches and durable admission fences are preserved.
An idempotent sibling-preservation regression covers this transient cache cleanup.
