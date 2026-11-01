# Live acceptance register

Last reconciled: 2026-10-29. Application deployed: `8bdff46`.
Code merged/deployed is distinct from a real-client scenario being accepted.
Items below are pending unless explicit evidence is linked. Do not infer a pass
from unit tests, CLI --help/version, startup notices or a green CI job.

## Latest open gates
- Rich IPC P1: inspect Markdown/HTML rendering in the owner client; two API acknowledgements are recorded.
- CP-031: owner reported `/restart` checked on 2026-10-29. Baseline accepted by owner; negative cases and specific topic/provider scenarios remain unqualified.

## Verified operational baseline
- [Rich IPC/restart rollout](deployments/2026-10-29-8bdff46.md): gateway/owner startup, two acknowledged HTML sends, preserved schedules/counts/config and initial online backup verification.
- [Private-login rollout](deployments/2026-10-25-01c18fe.md): image/startup/polling and immediate before/after state preserved. No live OAuth flow invoked; actual owner-DM/client proof stays A20.
- [Doctor output rollout](deployments/2026-10-25-ea38581.md): running/startup/polling and state preservation verified. Corrected client rendering remains to be observed; group operation after privacy re-add confirmed by owner separately.
- [Startup readiness rollout](deployments/2026-10-25-0a25267.md): actual selected Claude container starts automatically, CLI/local auth ready, management quota honestly unknown; database/schedule preserved. Live Codex quota remains A19.
- [CP-017 rollout](deployments/2026-10-25-f1f4532.md): running revision/polling, integrity, preserved counts/schedule and read-only getMe/private-menu checks verified. Real diagnostic client behavior remains A18.
- [18e0c86 starvation fix](deployments/2026-10-25-18e0c86.md): real Pi Docker
  background-preemption test PASS, deployed version/image, startup/integrity,
  preserved sessions/history/tasks and no pending/running turns. Actual user/model
  multi-topic behavior after this fix is not established by a sleeping-shell test.
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
  Codex CLI 0.164.0/Claude Code 2.1.310 authenticated turns or native resume.
- Owner subsequently created the real FreeFeed schedule and received its task
  receipt. It was later resumed externally and is active in the 17:24 predeploy backup;
  the 2026-10-29 live predeploy snapshot instead has 13:00/18:00 Europe/Belgrade
  plus a separate AI digest at 20:00; all were preserved. No successful
  recurring execution+digest delivery is recorded.

## User-reported acceptance and incident follow-up
On 2026-10-25 the owner confirmed voice messages and /tasks work. This accepts
the reported baseline, not every A01/A07 scenario: task paging/toggles and audio
files/video notes/provenance still need separate evidence. New Codex CLI 0.164.0
produced authenticated replies in the live chat; post-recreation resume and new
Claude acceptance remain open. Both topics later stalled behind system
extraction (fixed/deployed in 18e0c86; real user/model follow-up pending), documented in [the incident](incidents/2026-10-25-background-extraction-starvation.md).

## Pending current-release scenarios
| ID | Scenario | Acceptance / evidence still needed |
|---|---|---|
| A01 | Tasks panel (CP-015 follow-up) | Baseline /tasks confirmed by owner; >3 tasks page correctly, Pause/Resume edit the same message, stale/double/foreign clicks do not mutate unintended state |
| A02 | Authenticated upgraded CLIs | Harmless user turn on each new CLI; session resume after idle/container recreation, no first-turn loss; keep credentials/session mounts intact |
| A03 (old 00) | Topic continuity | Main and two private topics retain distinct native history on both providers across restart/recreation; expected shared memory remains shared by policy |
| A04 (old 0Z) | Manual provider continuity | Claude->Codex->Claude preserves native mappings and each topic's bounded handoff; /clear remains a boundary only in its topic; switching waits for active turn |
| A05 (old 0Q) | Confirmed quota fallback | No switch before consent; Cancel, Yes, stale/expired/restart/double tap; retry preserves voice/file/thread; target quota error has no reverse-switch loop. Current scope is chat-wide until CP-018 ships |
| A06 (old 0R) | Scheduled work and delivery | User-authorized real task runs once per slot and explicitly delivers its result; check next_run/status/log/receipt. Do not create/resume a live digest merely to close this audit |
| A07 (old 0Y) | Speech/media baseline | Voice baseline confirmed by owner; audio/video-note transcript metadata, typed captions, reply mode and partial/failure notices; albums and reply/quote/forward context on actual inputs |
| A08 (old 0U) | Stop, Retry and login buttons | CP-036 private inline button and same-topic /stop (normal input remains queued), native draft and group/edit fallback; verify process exit and next-turn resume separately for Claude/Codex; Retry one attempt with original topic/input; no duplicate button action; login lifecycle |
| A09 (old 0V) | Reply actions | Newest-reply row scope; Regenerate/Copy/Steps/As file, long replies, restart/expiry and secret masking on real clients |
| A10 (old 0W) | Reactions/progress/admission UI | Reaction rights/429, stall timers, elapsed/tools/queue display, Stop while waiting; no unintended run after cancellation |
| A11 (old 0X) | Command pickers/confirmations/pages | Model/provider/clear confirmations, real model use after restart, rendered HTML paging, actor/chat/topic scope, expiry and deleted-topic safe behavior |
| A12 | Durable-turn crash recovery | Explicit controlled crash mid-turn and send/memo window; one valid outcome in correct topic, no duplicated tool effects claimed without proof |
| A13 | Lifecycle notices | Graceful shutdown notice, abrupt crash/replay variant and cooldown on real Telegram client; startup API send is already verified |
| A14 | Memory capability from agent | Authenticated real skill request derives chat/thread from per-exec capability; no cross-scope query authority. Missing-token401 and unit tests already pass |
| A15 | Long code delivery | Real 20/100-line snippets in main/topic: collapse/document thresholds, balanced HTML, last-message buttons and no lost content |
| A16 | Resource envelope | Observe actual new native CLI memory and admission behavior on Pi; accepted NODE_OPTIONS is not proof of a V8/RAM cap. Host memory-cgroup/reboot remains a separately controlled operational item |
| A20 | Private login and group handoff | Owner `/login` in a group sends the real step privately and one neutral line to the group, starts no CLI and asks no confirmation (D049); nonowner/bot/unregistered/other-bot paths and the private sign-in expose no URL/code/raw output in groups. Automated tests passed; actual owner-DM auth/client delivery remains pending |
| A19 | Startup runtime and management readiness | Restart restores owner provider/model, starts matching container, reports CLI/auth and leaves sessions/history/tasks unchanged. Codex subscription management quota and negative/unknown paths need live evidence; no model request or inferred quota recovery. Selected Claude startup verified by [rollout](deployments/2026-10-25-0a25267.md); live Codex quota/failure paths remain pending |
| A18 | Telegram capability diagnostics | Owner/private summary + doctor/refresh match read-only getMe; registered nonowner summary/doctor denial; group silence, private menu, stale/cooldown and no-model behavior. Owner supplied a real /telegram doctor report on 2026-10-25 (getMe fresh, expected flags); refresh/nonowner denial/group-silence client checks remain pending. Formatting/runtime-label fixes tracked separately |
| A17 | Compaction correctness | Confirm source context reaches summarization, restart counters and serialization semantics; investigate old review notes before treating /compact as qualified |
| P01 (CP-033) | Preset templates per provider | Owner-run, once per provider (`/provider claude`, then `/provider codex`), with Gmail and Google Calendar connected for that provider. 1. Private chat: `/proactive gmail urgent`, On, confirm Yes (status is `unknown` until the probe). 2. Wait for the next full hour inside the working window (or set `/proactive gmail morning` / `calendar agenda` time to the next hour within its range); do the same for `calendar agenda`. 3. Check: a Telegram message arrives only when there is something to report; `/proactive gmail urgent` shows `Last outcome:` with the right class; `chats/<owner folder>/proactive/<preset id>.last` is valid JSON (`at`, `result`, `note` at most 120 chars); `gmail.urgent.state.json` exists after a run with mail. 4. Unavailable path: disconnect the connector in the provider account (or run the other provider), wait for a run: outcome `unavailable`, one short message, and no second message on the next run the same day. 5. Confirm the agent used the connector without named tools (the wording is generic) and wrote nothing to the mailbox or calendar. 6. `cal.reminder` is unavailable until PR 4: `/proactive calendar reminder` must say "Not available yet" and `on` must be refused; nothing is created. Record app/CLI versions, provider, outcome per preset. Hostile-file check: in the owner chat workspace replace `proactive/<id>.last` with a symlink to a host file, and `proactive` with a symlink to another directory; the screen must show "ran, outcome unknown" and never the target content. Pins the spec section 5.1 TO VERIFY. Status 2026-10-30: settings screens and preliminary acceptance passed (owner); first real digests awaited, per-provider outcomes not yet recorded |
| P02 (CP-033) | Integrations guide in a live turn | Owner-run, per provider. After the next owner turn `chats/<owner folder>/INTEGRATIONS.md` exists (group chats must not have it). Send: "A new connector is connected: Gmail. Read INTEGRATIONS.md, tell me which connectors you can see, and propose recurring actions." Expected: the agent reads the file, lists only connectors it can really call, proposes two or three actions with runs per day, asks for the time of day and timezone, and creates nothing until you answer yes. Answer yes to one: a task appears in `/tasks` with `CRON_TZ=...`, the three paragraphs (untrusted data, IPC delivery, outcome file), no service text in the prompt, and the agent quotes the receipt. Then disconnect a connector and ask again: the agent points to `/proactive guide <connector>` and creates nothing. Delete the test task with `/tasks cancel <id>` |
| P03 (CP-033) | Scheduled run creates a `once` task through IPC | Owner-run, once per provider; settles the TO VERIFY of spec section 3 before PR 4 relies on it. In the owner chat ask the agent to create a throwaway hourly task whose prompt says: "create one once task in 5 minutes with taskId `p03-probe` through the IPC task route, whose prompt is: send the message P03 ok". After the hourly run check `/tasks` for `p03-probe` (type once, future), that it fires and delivers "P03 ok", then `/tasks cancel` both tasks. Failure to create the task from the scheduled run is a blocker for PR 4 |

Do not execute destructive crash/topic/host-reboot tests or incur external spend
solely to make a checkbox green. Plan bounded test cases appropriate to the
operator's current authorization. Ordinary topic deletion is not a full data
purge; ephemeral/group privacy and per-topic provider selection are unimplemented
specs, not acceptance tests for an advertised current capability.

## CP-037 contract A tests

Automated evidence for the CP-037 P1 durable session identity contract
([D055](DECISIONS.md)). These IDs are separate from the A01-A19 release
scenarios above.

| ID | Contract | Tests | Status |
| --- | --- | --- | --- |
| CP-037 A01 | A conversation resolves to one stable session and binding, scoped to the registration, retiring older namespace-epoch bindings; continuity (`/clear`, provider handoff) runs through the durable adapter | `internal/store`: `TestResolveConversation*`, `TestNativeContextRoundTrip`, `TestSessionSchemaConstraints`, `TestClearDurableConversationContinuity*`; `cmd/gopod`: `TestDurableAdapter*` (incl. `ImplementsContinuity`), `TestNewSessionStateStoreSelectsByMarker` | automated |
| CP-037 A02 | Legacy sessions are adopted atomically, only when quiesced, and idempotently; unreachable rows are skipped; a request can be cancelled | `internal/store`: `TestAdoptLegacySessions*` (atomicity, skip, busy, idempotent), `TestCancelSessionAdoptionClearsRequest`; `cmd/gopod`: `TestAdoptSessionsIfRequested*`, `TestSessionsAdoptRefusedWhileBusy`; `internal/control`: `TestSessions*` | automated |
| CP-037 A06 | An attempt is bound to its session and epoch; a stale epoch never runs | `internal/store`: `TestBindAttempt*`; `cmd/gopod`: `TestBindingHandler*` (incl. `RefusesStaleEpoch`) | automated |
| CP-037 A11 | Turn ordering and per-session execution claims; `execution_unknown` is recorded from the runner's proof and kept as evidence (P1: takeover allowed, re-execution gated by the runner, D055); stale held claims are released at startup; batches never span threads | `internal/store`: `TestExecutionClaim*`, `TestReleaseStaleExecutionClaimsDeletesHeldOnly`; `cmd/gopod`: `TestBindingHandlerSettlesUnknown*`, `TestBindingHandlerTakesOverClaimAfterCrash`; `internal/queue`: `TestBatchesNeverSpanThreads` | automated |
| CP-037 native resume after adoption (Pi) | Real native provider resume after `/sessions adopt` and restart on the Pi | none yet | open |

## Procedure detail and retention
Original scenario steps and historical review notes are retained in
[the pre-housekeeping snapshot](history/HANDOFF-2026-10-25-before-housekeeping.md).
They are reference material, not a competing current queue. Old claims about
--full-auto, session clearing on switch, memory authentication and missing Pi
deploy have been superseded. For every accepted scenario record app/CLI version,
chat kind, inputs, observable result, date and a minimal sanitized evidence link.
