# Live acceptance register

Last reconciled: 2026-10-25. Application deployed: `8df259d`.
Code merged/deployed is distinct from a real-client scenario being accepted.
Items below are pending unless explicit evidence is linked. Do not infer a pass
from unit tests, CLI --help/version, startup notices or a green CI job.

## Verified operational baseline
- [8df259d deployment](deployments/2026-10-25-8df259d.md): running version/image,
  SQLite integrity, restart/OOM state, session/history/task count preservation,
  startup/polling and new CLI version smoke in hardened ARM64 containers on Pi.
- [7bfd994 delivery](deployments/2026-10-25-7bfd994.md): real canonical-JID IPC
  message accepted by Telegram with ok receipt/.processed; invalid JID returns
  reason/.failed. API acceptance is not user reading.
- [1e0a21b scheduling](deployments/2026-10-25-1e0a21b.md): source-scoped read-only
  IPC receipts from both agent images; no test schedule was created.
- Historical Codex authenticated turn and persisted login after container
  recreation succeeded on 2026-10-05 with older CLI. It does not qualify new
  Codex0.164.0/Claude2.1.310 authenticated turns or native resume.
- Owner subsequently created the real FreeFeed schedule and received its task
  receipt. It is paused now; cron stays 09:00 Europe/Belgrade. No successful
  recurring execution+digest delivery is recorded.

## Pending current-release scenarios
| ID | Scenario | Acceptance / evidence still needed |
|---|---|---|
| A01 | Tasks panel (CP-015 follow-up) | Real /tasks with Cyrillic/emoji; >3 tasks page correctly, Pause/Resume edit the same message, stale/double/foreign clicks do not mutate unintended state |
| A02 | Authenticated upgraded CLIs | Harmless user turn on each new CLI; session resume after idle/container recreation, no first-turn loss; keep credentials/session mounts intact |
| A03 (old 00) | Topic continuity | Main and two private topics retain distinct native history on both providers across restart/recreation; expected shared memory remains shared by policy |
| A04 (old 0Z) | Manual provider continuity | Claude->Codex->Claude preserves native mappings and each topic's bounded handoff; /clear remains a boundary only in its topic; switching waits for active turn |
| A05 (old 0Q) | Confirmed quota fallback | No switch before consent; Cancel, Yes, stale/expired/restart/double tap; retry preserves voice/file/thread; target quota error has no reverse-switch loop. Current scope is chat-wide until CP-018 ships |
| A06 (old 0R) | Scheduled work and delivery | User-authorized real task runs once per slot and explicitly delivers its result; check next_run/status/log/receipt. Do not create/resume a live digest merely to close this audit |
| A07 (old 0Y) | Speech/media baseline | New voice/audio/video-note transcript metadata, typed captions, reply mode and partial/failure notices; albums and reply/quote/forward context on actual inputs |
| A08 (old 0U) | Stop, Retry and login buttons | Native draft and group/edit fallback; Stop kills process and next turn resumes; Retry one attempt with original topic/input; no duplicate button action; login lifecycle |
| A09 (old 0V) | Reply actions | Newest-reply row scope; Regenerate/Copy/Steps/As file, long replies, restart/expiry and secret masking on real clients |
| A10 (old 0W) | Reactions/progress/admission UI | Reaction rights/429, stall timers, elapsed/tools/queue display, Stop while waiting; no unintended run after cancellation |
| A11 (old 0X) | Command pickers/confirmations/pages | Model/provider/clear confirmations, real model use after restart, rendered HTML paging, actor/chat/topic scope, expiry and deleted-topic safe behavior |
| A12 | Durable-turn crash recovery | Explicit controlled crash mid-turn and send/memo window; one valid outcome in correct topic, no duplicated tool effects claimed without proof |
| A13 | Lifecycle notices | Graceful shutdown notice, abrupt crash/replay variant and cooldown on real Telegram client; startup API send is already verified |
| A14 | Memory capability from agent | Authenticated real skill request derives chat/thread from per-exec capability; no cross-scope query authority. Missing-token401 and unit tests already pass |
| A15 | Long code delivery | Real 20/100-line snippets in main/topic: collapse/document thresholds, balanced HTML, last-message buttons and no lost content |
| A16 | Resource envelope | Observe actual new native CLI memory and admission behavior on Pi; accepted NODE_OPTIONS is not proof of a V8/RAM cap. Host memory-cgroup/reboot remains a separately controlled operational item |
| A17 | Compaction correctness | Confirm source context reaches summarization, restart counters and serialization semantics; investigate old review notes before treating /compact as qualified |

Do not execute destructive crash/topic/host-reboot tests or incur external spend
solely to make a checkbox green. Plan bounded test cases appropriate to the
operator's current authorization. Ordinary topic deletion is not a full data
purge; ephemeral/group privacy and per-topic provider selection are unimplemented
specs, not acceptance tests for an advertised current capability.

## Procedure detail and retention
Original scenario steps and historical review notes are retained in
[the pre-housekeeping snapshot](history/HANDOFF-2026-10-25-before-housekeeping.md).
They are reference material, not a competing current queue. Old claims about
--full-auto, session clearing on switch, memory authentication and missing Pi
deploy have been superseded. For every accepted scenario record app/CLI version,
chat kind, inputs, observable result, date and a minimal sanitized evidence link.
