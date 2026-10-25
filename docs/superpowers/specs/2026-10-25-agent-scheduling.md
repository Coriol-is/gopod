# Agent scheduling capability

**Date:** 2026-10-25  
**Scope:** Generic recurring work. No live schedule is created by this change.

## Findings behind the change
The failed live request used `type:"schedule_task"`, snake_case schedule fields
and redundant destination fields. The watcher expects `op:"schedule"` and
camelCase fields; the live failure log said `unknown op ""`.

Separately, the IPC sink previously inserted an active task with next_run=0,
which GetDueTasks excludes. The owner database file bind was RW=true, but its
parent directory was on a read-only root filesystem: the file bind does not
provide writable adjacent WAL sidecars. Source and live identity documents
were stale and disagreed. Task creation belongs to IPC/control, not direct SQL.
The reference control container runs in UTC and has no system zoneinfo package.

## Agent-facing entry points
The binary embeds `/workspace/chat/SCHEDULING.md`, seeded if absent on Ensure
for both existing and new chats. Existing CLAUDE.md/AGENTS.md/custom guide files
are never overwritten. A host-supplied system appendix always references this
guide and the supported IPC path for Claude/Codex user turns. The guide is
agent-writable documentation, not an authorization source. An optional
`container/skills/scheduler/SKILL.md` adds discovery when skills are mounted;
correct operation does not depend on the deployment's skills directory.

## Request protocol
Publish one complete regular JSON file atomically in
`/workspace/ipc/tasks/<unique-request>.json`; write a `.tmp` then rename. Payloads
are limited to 64 KiB before reading/decoding. FIFOs/devices/symlinks are rejected,
including swap races at open. Unknown fields and extra JSON values are errors.

```json
{"op":"schedule","taskId":"daily-review","prompt":"Review my notes and save /workspace/chat/review.md.","scheduleType":"cron","scheduleValue":"CRON_TZ=Europe/Belgrade 0 9 * * *"}
{"op":"schedule","taskId":"hourly-check","prompt":"Check the project status.","scheduleType":"interval","scheduleValue":"1h"}
{"op":"list"}
{"op":"get","taskId":"daily-review"}
{"op":"update","taskId":"daily-review","scheduleType":"cron","scheduleValue":"CRON_TZ=Europe/Belgrade 0 10 * * *","prompt":"Updated review instructions"}
{"op":"pause","taskId":"daily-review"}
{"op":"resume","taskId":"daily-review"}
{"op":"cancel","taskId":"daily-review"}
{"op":"delete","taskId":"daily-review"}
```

Schedule may omit taskId: the host derives a stable id from the source namespace
and request filename. A supplied taskId is useful for later management. Repeating
the same id/definition does not duplicate or reactivate a schedule; changing a
definition uses update. Task ids from receipts and `/tasks list` are full ids.

`targetChatFolder` is optional and defaults to the source folder. Only owner
source namespaces can target another registered folder. Source scope is derived
from the mounted directory and host registration, never JSON claims. All get,
list and mutation paths enforce stored task ownership; cancel no longer trusts
a globally supplied id. Updating cannot move a task to another chat.

## Scheduling semantics
A single FirstRunAt helper validates and computes initial slots for IPC and
Telegram task creation. It rejects malformed/unreachable cron, nonpositive or
sub-millisecond intervals, unknown types and non-future once times. Once uses
RFC3339 with timezone. IANA timezone data is embedded in the Go binary so
`CRON_TZ=Europe/Belgrade ...` and `CRON_TZ=Europe/Moscow ...` work in Alpine.
Explicit cron zones follow DST; an elapsed interval such as 24h does not promise
a fixed local hour. Bare cron uses process-local time (UTC in the reference
deployment); GOPOD_TIMEZONE is not implemented. Clarify ambiguous user timezones
rather than assuming Telegram, device or host location determines intent.

The host polls approximately once per minute. Execution can be delayed further
by queue/admission waits; this is not an exact-second timer. nextRun is Unix
milliseconds and nextRunAt is an explicit UTC RFC3339 display value.
Pause stops future occurrences; resume computes the next future slot. Completed
or overdue once tasks require an updated/new schedule, not silent resurrection.
Cancel marks done; delete removes the schedule. `/tasks cancel` retains its
existing delete behaviour. Neither operation terminates an already queued run.

Task mutation paths and scheduler dispatch share a per-store lock. The scheduler
refreshes a task row under that lock before queue insertion and advancement:
a stale poll snapshot cannot dispatch a now-paused/deleted/future-rescheduled
row or overwrite a newer definition. A slot already enqueued before a pause may
finish. Existing durable-turn source identity still deduplicates re-fired slots.

## Result receipts and safe replay
The host publishes `/workspace/ipc/results/<unique-request>.result.json`:

```json
{"status":"ok","requestFile":"request.json","op":"schedule","result":{"taskId":"daily-review","task":{"taskId":"daily-review","chatFolder":"owner","chatJid":"tg:<actual id>","prompt":"...","scheduleType":"cron","scheduleValue":"CRON_TZ=Europe/Belgrade 0 9 * * *","nextRun":1793088000000,"nextRunAt":"<UTC RFC3339>","status":"active"}},"help":"Read /workspace/chat/SCHEDULING.md ..."}
```

Error receipts have status error and an actionable error string; no fake success
or guessed SQL fallback. Original requests move to `.processed`/`.failed`.
Receipts are written atomically with mode 0600 through an os.Root capability.
Namespace-scoped read/archive/prune operations cannot follow agent-created
symlinks outside the source IPC root or block on a special file channel.

Agent-writable receipt files are not replay authority. Task mutations and their
response JSON commit in one SQLite transaction in a host-owned request journal,
keyed by source/request filename and a payload+authority fingerprint. Retried
operations replay that committed response, including update/delete, rather
than applying again. Reusing a filename for another payload is rejected.
Resolve path-derived routing before the transaction; tx-aware store methods
avoid deadlock on the store's single connection.

Unarchived successful requests are never evicted. They are acknowledged only
after the host archives the input. A bounded pending backlog stops new mutations
rather than discarding replay authority when receipt/archive writing is broken.
Archived response retention and file archives are bounded (default 200 per
source). Do not intentionally resubmit old acknowledged filenames after retention;
use a new filename to query fresh state. A crash after archive but before journal
acknowledgement may retain a harmless pending record for operator reconciliation.

## Result delivery and execution context
Scheduled tasks remain background work in main (thread 0), not the topic where
a user created the task. Their returned stdout is not automatically delivered
to Telegram. No delivery or successful model run may be inferred from scheduling
acceptance. For a requested notification, put an explicit IPC message step in
the task prompt and use the actual chatJid returned by the task receipt:

```json
{"chatJid":"tg:<actual id>","text":"The completed report"}
```

Publish atomically under `/workspace/ipc/messages/<unique-name>.json` and check
its processed/failed archive. Existing message delivery and cross-chat ownership
rules remain unchanged. Do not put credentials in task payloads or logs.

## Validation and limits
Tests exercise the real watcher and SQLite sink, not only fake Schedule calls:
first next_run, IANA zones/DST, lifecycle/scopes, strict payload errors, receipt
failure/replay, atomic rollback, bounded pending cache, stale dispatch, short ids,
special files/symlinks, and existing custom workspace guide delivery. Live
acceptance should inspect list and invalid-op receipts without creating a real
user schedule. No daily digest or chosen user timezone is part of this change.
