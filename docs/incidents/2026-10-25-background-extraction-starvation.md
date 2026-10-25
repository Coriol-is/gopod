# Background extraction stalls all topics of a chat

Date: 2026-10-25. Incident observed on deployed 8df259d.

After a chat-wide Claude -> Codex switch, topic3172331 answered normally using
codex-cli0.164.0. A later message in topic3172664 appeared acknowledged but had
no reply; subsequent messages in both topics waited. Container restarts/OOM
were not involved. SQLite showed turn63 running and64/65 pending; Docker process
inspection (arguments classified without logging prompt/environment) confirmed
the active Codex process had --ephemeral, not resume. It was system extraction,
not the blocked user's native conversation. It held the per-chat execution slot
for several minutes without a deadline. It ended naturally before a targeted
TERM attempt (the old PIDs no longer existed); no session/history was cleared.
Turns63–66 subsequently completed with nonzero Telegram reply ids.

## Correction
- RunFresh receives a one-minute deadline (a shorter caller deadline wins).
- Foreground work cancels background work only in the same chat, and waiting
  foreground work takes priority over new background starts.
- Buffered foreground and fresh agent runs use a pid wrapper and bounded
  process-group TERM/KILL cleanup, like streaming. Cancellation closes the
  hijacked Docker attachment so StdCopy does not wait forever after cancel.
- Slot release waits for the cleanup attempt. Existing kill failure is logged;
  Docker outages or tools that deliberately escape their process group are not
  claimed to be solved by this change. No exactly-once/tool rollback guarantee.
- Canceled/deadline compaction returns without clearing history/session mappings
  or turn counters. Interrupted best-effort extraction can be skipped; original
  turn history remains persisted. Full maintenance rescheduling/quiet hours
  remain CP-006, not implemented by this fix.

## Verification scope
Regression tests exercise same-chat preemption, other-chat independence,
foreground priority, fresh deadlines, cleanup ordering for sync and background
runs, and canceled compaction preserving its context. A real local Docker test
uses a silent sleeping shell process in a hardened no-network ARM64 container;
no credentials/model call. It verifies the process is gone before foreground
slot reuse. Full checks/review/CI and final deployment evidence are recorded by
the release handoff; an unmerged document is not deployment proof.

Provider choice is still chat-wide and execution serialized per chat. Different
providers in different topics and concurrent CLI processes are CP-018/runtime
planning, not current advertised functionality.
