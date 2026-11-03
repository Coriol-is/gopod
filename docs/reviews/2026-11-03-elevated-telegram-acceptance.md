# Elevated Telegram acceptance — 2026-11-03

Owner explicitly requested live Telegram checks. Used the authorized standalone
Telegram Toolkit account in private chat `@Nanoclaw_go_bot`, bounded to messages
3176027–3176048 (11:03–11:06 UTC). CLI sent commands; the same toolkit's Telethon
client inspected markup and clicked exact message/button pairs. No credentials
were copied, login initiated or unrelated messages sent. Native visual rendering
was not inspected; actual callback execution and edited responses were observed.

| Probe | Message evidence | Result |
| --- | --- | --- |
| Initial status | 3176027 → 3176028 | ROOT off, ordinary conversation active |
| Provider picker | 3176029 → 3176030 | Claude/Codex inline buttons |
| Claude choice and Cancel | edited 3176030 | Full host-root warning and explicit start/cancel; cancellation created no session |
| Codex confirmation/start | 3176031 → edited 3176032 | Dedicated Codex image running, ROOT marker and controls |
| Codex fresh text | 3176033 → 3176034 | Admission notice; status button showed executing and one host exec |
| Stop-answer button | edited 3176032, reply 3176035 | Cancellation acknowledged; exec exit 137; only sleep remained in container |
| Journal button | edited 3176032 | Limited-journal warning and settled exec shown |
| Status after cancellation | 3176036 → 3176037 | Session retained, awaiting input |
| Close button | edited 3176037 | Container removed, ordinary routing restored |
| Claude confirmation/start | 3176038 → edited 3176039 | Dedicated Claude image running |
| Claude text and /stop | 3176040–3176044 | ROOT notices; cancellation settled, only sleep remained |
| /root stop | 3176045 → 3176046 | Session closed and container removed |
| Stale close button | edited 3176039 | Rejected as belonging to a completed session |
| Final status | 3176047 → 3176048 | ROOT off, ordinary conversation active |

Both test prompts explicitly prohibited tools and file access/changes. They were
cancelled; neither is evidence of successful inference. Direct native auth-status
checks in the corresponding running elevated containers showed Codex `Not logged
in` and Claude `loggedIn=false`, `authMethod=none`. Authenticated new/resume/tool
acceptance therefore remains open pending separate provider login.

Final live DB: zero active elevated sessions; both provider sessions closed with
`owner close`; three settled journal rows (two exit 137 cancellations and one
exit 0 preparation). No elevated container remains. All nine baseline containers
run; all seven existing healthchecks healthy. Controller feature flag stays on.

No defects observed in the exercised control/lifecycle paths. This does not prove
cross-generation rejection while another session is active, authenticated native
resume, arbitrary host-tool behavior, media refusal, or native visual rendering.
