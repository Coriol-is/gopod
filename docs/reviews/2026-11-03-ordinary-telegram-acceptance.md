# Ordinary Telegram acceptance — 2026-11-03

Owner authorized autonomous acceptance. Standalone Telegram Toolkit sent bounded
commands to the owner's private `@Nanoclaw_go_bot` conversation and inspected
actual replies/markup through its Telethon client. Native visual rendering was
not inspected. No task was paused/resumed, provider switched, session adopted,
container restarted or deployment performed.

| Check | Message IDs | Observation |
| --- | --- | --- |
| Routing baseline | 3176213 → 3176214 | ROOT Codex open and idle; ordinary native prompts withheld to avoid elevated routing |
| Tasks paging | 3176215 → 3176216 | 15 tasks, page 1/5; Next edited 3176216 to page 2/5, Previous restored page 1/5; task state unchanged |
| Model picker | 3176217 → 3176218 | Default selected; clicking the already selected default edited the message but removed all buttons |
| Silent picker | 3176219 → 3176220 | Initially off; clicking on edited the message and enabled silent, but removed all buttons |
| Restore silent | 3176227 → 3176228; 3176229 → 3176230 | Explicit `/silent off`, then `/silent` read-back: off, buttons `on` and `✓ off` |
| Doctor refresh | 3176225 → 3176226 | `/telegram doctor refresh`: fresh getMe at 13:13:03 UTC, no error, cooldown reported; startup readiness explicitly historical |
| Session state | 3176223 → 3176224 | Legacy mapping; no adoption request; zero durable sessions/contexts |

The initial `/telegram refresh` probe (3176221 → 3176222) correctly returned
usage; the supported command above succeeded. The first scripted silent restore
attempt looked for an off button in the edited message and found none; it did
not restore the flag. The subsequent explicit command and read-back did.

## Confirmed defect

The silent/model selection handlers return text only. Telegram correctly replaces
the picker with that response, so the buttons disappear. This violates the
project's picker convention (retain choices and mark the current choice) and
the A21 in-place picker scenario. Fix successful selection responses, preserve
Router authorization and callback scoping, and qualify the edit path in tests.

A01 pagination and A18 refresh have live evidence; other scenarios in those rows
remain open. A11/A21 picker behavior fails until a corrected build is deployed
and retested. No ordinary-provider inference, media/Stop/recovery behavior or
visual client rendering is claimed by these probes.

## Fix verification checkpoint

Branch `fix/persistent-command-pickers` retains the silent and model choices on
successful selection responses. The model catalog is copied before appending
default; error and authorization paths are unchanged. The Telegram regression
`TestSettingPickerRetainsChoicesAfterSelection` exercises both real command
handlers, callback dispatch and two edits of the same message, including the
reverse selection and absence of extra sends. Full build/vet/tests passed.
An independent reviewer passed the diff and reran the focused tests. Custom
model IDs outside the catalog still have no marked choice (existing behavior).
PR #159 merged as `88a9aa3` after green test/isolation/Sonar. Live retest of
the corrected build remains pending deployment.

## Additional diagnostic finding

A subsequent `/version` probe (3176256 → 3176257) returned
`gopod dev (go go1.26.10)`. It does not identify the deployed revision and cannot
verify it. This diagnostic discrepancy is noted for a separate fix; no deployment
was inferred from that output. The last recorded deployment remains the elevated
login rollout report, not a fresh binary-version verification in this session.

## Post-deploy picker retest

Build `4350e1b`: silent reply 3176275 retained buttons across on/off and restored
off; model reply 3176277 retained the default picker after selection. ROOT status
3176279 is off after normal deployment shutdown; session status 3176281 remains
legacy. See [rollout](../deployments/2026-11-03-backlog-wave.md).

Version diagnostic resolved and deployed in `ec51ee6`: command 3176288 received
reply 3176289, `gopod ec51ee6 (go1.26.10)`, matching startup identity.
