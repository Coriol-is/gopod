# Telegram topic deletion and conversation retirement

Date: 2026-10-25. Status: implementation design accepted for specification;
implementation deferred. Proposal: CP-016. Private-chat topics first.

## Evidence and scope
The official Bot API supports deleteForumTopic(chat_id, message_thread_id) in
private chats with users and forum supergroups. It removes the topic and all its
Telegram messages. Group deletion requires administrator can_delete_messages;
creation/editing use different rights. Private topics must be enabled for the
bot. The deployed bot reports has_topics_enabled=true and
allows_users_to_create_topics=true (read-only getMe, 2026-10-25).

There is no documented forum_topic_deleted update/service field. Created,
edited, closed and reopened events exist; closed is not deleted. Do not promise
instant cleanup after a user deletes a topic in their Telegram client, or use
MTProto/user-account access as a hidden substitute. No Bot API topic enumeration
or existence-query method was found; a local list contains only observed topics.

Current gopod routes private message_thread_id into history/session keys.
Group topics intentionally collapse into conversation 0. There is no deletion
command. threadgone.go groups TOPIC_CLOSED, TOPIC_DELETED and thread-not-found
into an in-memory send fallback to main; it does not retire sessions. /clear
invalidates this conversation's history and all-provider session mappings, but
does not delete the Telegram topic or erase every stored/native artifact.

## Commands and authorization
Add /thread info and /thread delete through internal/control. The initial delete
command acts only on the nonzero private-chat topic in which it is invoked.
Reject main/thread 0 and unsupported chat kinds. Do not accept arbitrary foreign
chat ids. Bind confirmation to actor, registered chat/folder, exact thread and
expiry; state explicitly that all Telegram messages in that topic will be lost.
Use the existing confirmation UI, adding actor binding if needed. Confirmation
must never move to main with a new inferred target. A success notice can be
sent in main only after deletion, without conversation content.

A future owner-only picker may select known topics, but cannot present its local
registry as a complete live Telegram listing. Group lifecycle is a separate
phase: group conversation isolation and current-user authorization must be
qualified before enabling a destructive command there.

## Durable lifecycle
Persist a scoped deletion intent and tombstone (folder, thread, status), with
observed topic metadata kept separate from confirmed Telegram state.
1. Revalidate confirmation and ownership; mark deleting and stop admission for
   this conversation. Cancel queued/running turns and wait for their writes to
   quiesce. Do not restart the entire chat container or affect another topic.
2. Call deleteForumTopic with the exact stored chat/thread. A definite refusal
   preserves history/session mappings and returns an actionable error. A timeout
   is uncertain, not success; preserve the intent for bounded reconciliation.
3. After confirmed success, retire all Claude/Codex session mappings, continuity
   tail/last-provider state, pending confirmations and per-topic counters/drafts.
   Prevent interrupted-turn recovery, late replies and old quota offers from
   recreating this conversation or leaking its reply into main.
4. Retain a durable deleted marker across restart/provider changes. Local cleanup
   is idempotent and restartable after Telegram success; no cross-system atomic
   transaction is claimed. Report pending cleanup honestly when necessary.

If another client deletes a topic, there is no immediate event. An explicit
TOPIC_DELETED send failure may trigger retirement after the current turn is
quiescent; error strings are observational and not a documented typed contract.
Generic 400, thread-not-found, permission/network errors and TOPIC_CLOSED do not
justify destructive cleanup. Closed/reopened service messages update availability
without removing sessions. Do not probe topics by posting messages on a timer.
Unknown/inaccessible topics may be quarantined and exposed for manual cleanup.

Retirement means removing resumability/context, not silently deleting chat-shared
files, the container, credentials, shared memories or every audit record. Apply
the existing /clear boundary to prevent transcript reseeding. Per-thread extracted
memories, audit transcripts, native session files and derived media need an
explicit retention/purge policy; a separate future forget/purge operation may
remove them. Never claim topic deletion is a complete personal-data purge.
Scheduled tasks and IPC delivery currently target main and are not topic-owned;
do not cancel unrelated chat tasks. Future topic-targeted jobs must consult the
tombstone and fail visibly rather than silently reroute to main.

## Delivery slices and acceptance
First ship the command/confirmation, scoped persistent lifecycle and cleanup;
then external deletion/error reconciliation and an observed-topic inspector.
Test foreign/expired/double confirmation, thread 0/group rejection, exact API
ids, denied/uncertain responses, closed versus deleted, provider roundtrips,
concurrent queue/recovery/late completion and crashes on either side of the API
call. Tests must show another topic/main remain usable and cannot inherit the
retired session. Real destructive acceptance uses an explicitly authorized
throwaway private topic; none was deleted during this research.

## Official references
- https://core.telegram.org/bots/api#deleteforumtopic
- https://core.telegram.org/bots/api#message
- https://core.telegram.org/bots/features#topics-in-private-chats
