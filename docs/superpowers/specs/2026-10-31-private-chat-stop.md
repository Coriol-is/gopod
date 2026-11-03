# Explicit private-chat Stop

Date: 2026-10-31. Owner accepted button plus /stop, explicitly rejecting automatic
interruption by a new message. CP-036; refines D021 private reply presentation.

## Contract

Owner-private interactive turns send an ordinary editable placeholder carrying
existing st:<turn-id> Stop before starting the agent. Agent wait notices update
that placeholder; streaming reuses it. Completion, failure and Stop retire its
Stop button. Buffered voice-only completion removes the temporary control and retains its
voice-only delivery policy. Other chats retain existing presentation. A failed placeholder send
must not prevent /stop from cancelling a registered run.

/stop is registered in internal/control with PermOwnerOnly and requires a private
Telegram caller, no arguments. Main wires the handler to the Bot's in-flight
registry. Match chat plus thread; never cancel another topic or a pending turn.
Respond with a cancellation-request acknowledgement, not a claim of process death.
Repeated Stop is idempotent. Commands continue through normal Router dispatch.

The registry gains thread identity. Both stream and buffered interactive handlers
register a child cancellation context before execution. Streaming setup fallback
uses that same context; Telegram feedback uses the parent context. Cancelled runs
return queue.ErrStopped. Parent cancellation remains shutdown/interrupted. Stop
alone never retries the turn, clears a session, or flushes pending messages. A
pending user message can naturally start when the slot becomes available.

Existing runner cleanup (TERM/KILL, bounded best effort), session capture and
resume behavior stay intact. No guarantee is added for detached tools, Docker
outages, interrupted native session writes, external effects or queued outbox
messages. Scheduled silent tasks remain outside the interactive stop registry.

## Implementation and validation plan

1. Add control command, topic-aware registry lookup and controller wiring.
2. Reuse a persistent private placeholder through setup/wait/stream and buffered
   execution; keep feedback context separate from agent cancellation.
3. Test permissions/source/arguments, idle and repeated stop, topic isolation,
   button lifecycle, buffered stop and fallback context, normal-message queueing.
4. Run build/vet/full tests, race tests and existing Docker process cancellation
   test. Review diff, PR and green CI before merge. No deployment in this scope.
5. Keep real Telegram button/command and native Claude/Codex resume acceptance
   explicit and open until tested on the deployed version.
