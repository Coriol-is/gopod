# Ordinary native acceptance — 2026-11-03

## Scope and planned checks

Owner authorized three parallel backlog streams and autonomous Telegram
acceptance. This stream is the sole Telegram actor for the coordinated wave.
Use the owner's private bot conversation, the existing standalone Telegram
Toolkit authorization and bounded read-only runtime inspection. No login,
provider switch, restart, deployment, schedule change or deletion of user data.

1. Verify ROOT is off, record `/version` and selected provider, and check that no
   owner work is running before native probes.
2. Send two harmless no-tools turns with a disposable nonce to check ordinary
   session continuity. This does not prove continuity after recreation/restart.
3. Start an explicitly named harmless 30-second sleep probe, stop that exact
   turn with its own control, then check process exit and a subsequent reply.
4. Ask for a tiny PDF report through the agent's existing attachment contract;
   inspect actual document metadata and delivery evidence.

Pause if owner activity appears. API-side replies and callbacks are evidence of
delivery/behavior, not native visual rendering. Every result below names its
message IDs and states the untested boundary.

## Baseline checkpoint

- `/root status` 3176294 → 3176295: ROOT off, ordinary conversation active.
- `/provider` 3176296 → 3176297: Codex selected; no switch performed.
- `/version` 3176298 → 3176299: `gopod ec51ee6 (go1.26.10)`.
- Read-only SQLite inspection immediately before native probes: no pending,
  running or interrupted turns. Existing ordinary owner container is running;
  no elevated container appears in the bounded Docker inventory.

The active provider and release match the planned ordinary-context checks.

## Two-turn continuity checkpoint (A02/A03 partial)

- Seed 3176300 → 3176301: a bounded no-tools prompt supplied a disposable nonce
  and requested exactly `ACCEPTANCE READY`; the expected final answer arrived.
- Recall 3176302 → 3176303: the next no-tools prompt asked for that nonce without
  repeating it; the exact nonce arrived. Both replies gained the usual final
  reply-action row after the temporary Stop row.
- The seed turn is durable row 255, `done`, attempts 1, thread 0, with a final
  reply memo. A subsequent read-only check found no unfinished turns.

Telegram account message IDs and the bot's message IDs differ; the store row was
correlated with the unique test nonce, not an assumed cross-account ID equality.
This establishes an authenticated ordinary Codex two-turn conversation. It does
not establish fresh Claude behavior, restart/recreation continuity, distinct-topic
isolation, native visual rendering, or a native-context identity trace.

## Stop and subsequent reply checkpoint (A08 partial)

- Prompt 3176304 explicitly requested one harmless `sleep 30` and no other
  tools/files/network. Its reply 3176305 carried the Stop button.
- Before stopping, Docker's process inventory showed the requested sleep child
  (PID 241419) under Codex (PID 241230), distinct from the container's idle sleep.
- Clicked only the Stop button on 3176305, after verifying it replied to the
  test prompt. Callback answer: `Останавливаю…`; the same message became
  `(stopped)` without buttons. No user turn was targeted.
- Durable turn 257: `stopped`, attempts 1, thread 0, final reply memo present;
  no unfinished turns. Docker inspection independently showed `Running=false`,
  `Pid=0`, exit 137, `OOMKilled=false`, finished 2026-11-03 14:17:57 UTC.
  This cancellation stopped the sandbox container; process exit is proved
  separately from the Telegram acknowledgement.
- Subsequent no-tools prompt 3176306 → 3176307 returned the exact earlier nonce.
  Ordinary conversation continued after Stop and sandbox restart/recreation.

This qualifies one real Codex inline-Stop path and a subsequent contextual
reply. It does not qualify Claude, `/stop`, waiting-admission cancellation,
groups, duplicated/stale/foreign clicks, or a native session-ID trace.

## Test-topic authorization checkpoint

The owner separately requested the Telegram Toolkit topic-creation fix during
this wave. The coordinating agent authorized one live creation through the new
`topics create` command, after the active native test finished. The next
read-only database check found no unfinished turns. Create only the named
disposable topic `Gopod acceptance 2026-11-03`; retain it, do not delete it.

## Topic creation and PDF checkpoint (A22 partial)

- One CLI invocation created topic 3176309 with the exact requested title.
  Readback confirmed `MessageActionTopicCreate` and the same title. No retry or
  deletion occurred. Independent Toolkit review and its seven targeted tests
  passed; acknowledgement requires the request's matching random ID.
- PDF prompt 3176311 and progress/final reply 3176312 stayed in topic 3176309.
  The first attachment attempt emitted refusal message 3176313 in main
  (`reply_to` and thread both absent), reporting `source_missing` for the test
  PDF and incorrectly claiming the file remained in the working folder.
- Without operator retry, the agent recovered and delivered document 3176315
  in topic 3176309: `application/pdf`, 668 bytes, exact requested filename
  `gopod-acceptance-20261103.pdf`. Download of only this test document confirmed
  `%PDF-1.4`, the requested marker and date. No native visual rendering checked.
- Normal durable IPC delivered the document: group
  `00ffe17f-df51-42c8-8d61-c91b23707d26`, owner `legacy_agent_ipc`, thread 3176309,
  state `sent`; upload part `sent`, attempts 1, bot message 1437. The artifact
  is ready, 668 bytes. There is no evidence of a direct Bot API workaround.
- Durable turn 259 finished `done`, attempts 1, thread 3176309. No pending,
  running or interrupted turns remained at the final read-only checkpoint.

Two concrete defects were handed to the coordinating agent:
`internal/ipc/attachments.go:123` resolves only folder→chat and sends a refusal
through `msgs.Send`, losing topic routing; line 160 claims file existence even
for `source_missing`. Final document success does not erase either defect.
A22 remains partial: fresh-topic Codex discovery/delivery worked after a visible
refusal, while clean first-attempt behavior and native visual proof are open.

## Handoff

Production remains ec51ee6; no deployment or manual restart was performed.
ROOT remained off and provider Codex was not changed. Stop, two-turn recall,
post-Stop contextual continuation, topic creation and PDF delivery have the
bounded evidence above. Test topic and PDF are intentionally retained.
Telegram writes paused after the finished PDF turn, allowing root to coordinate
further changes/deployment. Parent owns the central HANDOFF/ACCEPTANCE register
updates and the refusal-routing/wording fixes. No acceptance status here implies
Claude, group behavior, full topic isolation or visual-client qualification.
