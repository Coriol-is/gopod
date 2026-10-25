# Manual provider continuity

Date: 2026-10-25. Approved scope: owner request for seamless manual switching
and correct threads. CP-009; decision D030.

## Behaviour
The provider remains per chat. Sessions and continuity are per `(folder,
thread)`, including main as thread 0. A switch never deletes a CLI session.
The next turn on a different provider receives recent conversation history,
then its current request. A return to an old provider resumes its own native
session and catches up on work done elsewhere. Workspace and scoped memory
keep their existing mounts and semantics. Model overrides reset on a harness
change; login remains provider-specific. An exhausted old provider is never
called to produce a summary.

## Storage and prompt
Add `conversation_history(id, chat_folder, thread_id, role, content)` with a
conversation index. Record the original request before starting the model;
record a successful response and the conversation's last successful provider
in one transaction before releasing the agent slot. Failed/stopped attempts
retain their request; no fabricated success or assistant response is stored.
Delivery to Telegram is independent, so a completed agent answer can already
be available during switching even before its final send completes.

Once per conversation, import up to 24 terminal Telegram turns and their
recorded final messages, ordered chronologically. Do not import commands,
scheduler turns or other conversations. This is a compatibility bridge,
not reconstruction of missing historical tool transcripts.

Read up to 48 recent messages, at most 3000 Unicode characters per message,
and at most 48000 bytes of encoded JSON. Emit oldest first. A bridge explicitly
says history may be truncated, labels user/assistant roles as data, and
instructs the receiver to verify workspace state before repeating actions.
History goes into the user prompt, never a privileged system instruction.
Same-provider successful turns do not repeat the bridge. A failed new-provider
turn does not advance the provider marker, so its next attempt gets the bridge.

## Synchronization and failure
The chat agent slot covers ensure, provider selection, sync/stream/fresh model
runs, and transcript recording. Manual switch waits on that slot, stops/removes
the idle old container with checked errors, atomically persists provider and
model reset, then updates the in-process choice. Failure never announces a
successful switch. Normal boot restores the persisted provider as before.
A failed request-history write prevents model execution. A failed final-history
write fails the turn explicitly; no successful result or provider marker is
reported. Model actions may already have happened, so a manual retry must
check workspace state. Final persistence uses a five-second cancellation-detached
deadline after a successful run.

## Clear
`/clear` waits for that chat's active agent. In one SQLite transaction, remove
this thread's continuity messages and all its native session mappings, marking
legacy import complete. For main, mark both provider legacy adoptions complete
so old on-disk sessions cannot be re-adopted. Other threads and memory remain.
Native files and the existing Telegram/turn audit history are retained; orphaned
native sessions are not selected again. `/compact` keeps its existing summary
flow and clears the same conversation's continuity/session mappings afterwards.

## Limits
This is bounded context continuation, not byte-identical native session
migration. Older details, tool calls, hidden state and stopped partial answers
are not guaranteed to cross harnesses. No automatic retry, spend, login,
quota-based switch or deployment is introduced.
