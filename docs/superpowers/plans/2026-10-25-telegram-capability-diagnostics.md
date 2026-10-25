# CP-017 — Telegram capability diagnostics implementation plan

Status: initial private-DM slice implemented, 2026-10-25; release verification
and real-client acceptance tracked in HANDOFF/ACCEPTANCE. This plan does not
authorize additional Telegram modes. Contract: [capability specification](../specs/2026-10-25-telegram-capability-diagnostics.md).
Related boundary: [CP-019 private service replies](../specs/2026-10-25-telegram-ephemeral-commands.md).

## Release boundary and decisions

Deliver a read-only, private-DM diagnostic path before broadening the capability
matrix. Registered private chats get `/telegram`; only the configured owner
**chat** gets `/telegram doctor`. Deterministic `/help guest`, `/help inline`,
`/help topics` explain limitations and configuration. No LLM, agent runner,
container, CLI/auth check, provider request or paid call participates.

CP-019 transport is not implemented. Consequently these new commands emit no
public group/channel output, including errors, permission denials, unknown
subcommands and help topics. Unknown chat kind fails closed. This is a narrow
CP-017 gate, not a conversion of every existing command to ephemeral delivery.
Group diagnostic/context-rights probes wait for CP-019. Ordinary conversational
DMs continue when diagnostics are unknown or degraded.

Keep observed BotFather facts separate from freshness, local implementation,
requested update types, local policy, context rights, runtime observations and
effective availability/reason codes. An enabled flag never means a working
handler. Multiple blockers must remain visible. Bot-to-bot enablement is unknown
unless a supported authoritative observation exists; do not infer it from group
privacy, inline or guest flags.

The current SDK v1.27.0 has `support_inline_queries` instead of Telegram's
`supports_inline_queries`; typed booleans also erase missing versus false.
Use a Telegram-private, one-endpoint raw `getMe` adapter with pointer booleans
and the official field spelling. Do not fork the SDK, add response-rewriting
middleware or introduce a generic capability/plugin registry. Share an explicit
HTTP client through public `bot.WithHTTPClient` while preserving the existing
one-minute polling timeout. Bound each diagnostic request by a five-second
context; do not apply a five-second client timeout to long polling.

Add `bot.WithSkipGetMe`: the SDK constructor otherwise performs a mandatory
getMe and aborts startup on failure. Start one asynchronous, bounded refresh
under `Bot.Run`'s context; polling must not wait for that refresh. Constructor
validation of empty token and missing store remains. Invalid credentials still
prevent Telegram operation; graceful diagnostic failure does not make an invalid
token usable. Verify SDK bot identity
consumers when skipping constructor getMe; hydrate needed identity from the
successful observation through a supported seam, or remove that dependency
before shipping. No speculative SDK private-field mutation.

No new guest/inline/business handlers, subscriptions or notices ship here, even
when BotFather enables them. Optional notice-only mode is a separately approved
follow-up. Do not map a guest identity into an owner session. Refresh does not
call getUpdates, setWebhook, setMyCommands, sendMessage or change settings.
Normal startup menu publication is separate from refresh.

## Ownership and execution waves

Use a feature worktree and the existing native docs workflow; no `.planning`
migration. One integration owner owns shared-file edits and final assembly.
Parallel workers must preserve other agents' changes and not edit integration
files. Proposed new filenames below are implementation targets, not existing
API claims.

| Task | Owner / owned files | Depends on | Wave |
| --- | --- | --- | --- |
| D01: production tracer | Integration owner; `internal/telegram/bot.go`, `handler.go`, `cmdbuttons.go`, `internal/control/control.go`, `handlers.go`, `cmd/gopod/main.go`; tracer tests/new diagnostic files | none | 1 |
| D02: observation/cache hardening | Telegram worker; `internal/telegram/capabilities.go`, `capabilities_http.go` and their tests | D01 | 2 |
| D03: reports and help | Control worker; `internal/control/telegram.go`, `telegram_test.go`, report fixtures | D01 interface frozen | 2 |
| D04: integration and privacy regression | Integration owner; all shared-file wiring, menu tests, callback/command tests, documentation | D02, D03 | 3 |
| D05: independent review and release | Reviewer plus release/integration owner; evidence/acceptance docs | D04 | 4 |

D01 intentionally produces a complete small path. Do not spend wave 1 building
a full matrix without a safe executable command. In wave 2, workers request
shared-file changes from the integration owner rather than concurrently editing
`bot.go`, `handlers.go` or `main.go`.

## D01 — minimal production tracer first

1. Add a small immutable snapshot/view contract consumable by `internal/control`
   without importing `internal/telegram`. Telegram owns fetching; inject reader
   and owner refresh callbacks when registering commands. Start with inline
   setting, observation time, freshness and unsupported implementation. Include
   explicit unknown values; no zero-value false fallback.
2. Implement the raw getMe envelope/DTO and a single bounded read. Request the
   fixed HTTPS Telegram API endpoint using a private token closure. Injectable
   HTTP doer/base URL or fetch function is test-only/internal plumbing, never a
   user-supplied diagnostic URL. Validate envelope `ok`, result identity and
   field types; malformed/missing booleans are unknown, never disabled. Preserve
   valid unrelated fields when safe; record schema degradation explicitly.
3. In `Bot.New`/`botOptions`, share HTTP transport, skip constructor getMe and
   retain all existing offset/middleware/handler/update options. In `Bot.Run`,
   launch the bounded initial refresh with cancellation and shutdown cleanup.
   Initial unknown state must not block polling or normal DM dispatch.
4. Register canonical `telegram` (ChatLocal, explicit registered-private check
   for a nonowner summary)
   and hidden `telegram.doctor` (OwnerOnly, empty SlashName). `/telegram doctor`
   must invoke `Router.Dispatch` with the original trusted Caller and canonical
   name; never call the privileged handler directly. `/telegram doctor refresh`
   passes a validated `refresh` argument through that same owner-only route;
   plain doctor reads cache. No other route triggers manual refresh. Other arguments get a
   deterministic private usage response.
5. Populate minimal server-trusted chat kind in `control.Caller` at Telegram
   message and callback entry points. Retain owner-chat semantics. Reject a
   non-private or unknown caller in diagnostic handlers even if another frontend
   constructs that Caller. Registration is checked through the existing trusted
   lookup, not `PermChatLocal` alone. Owner doctor relies on Router owner-chat
   authorization and must work on the first owner message before a registered
   row/folder exists; do not accidentally require runner-backed registration.
6. Immediately after existing `persistMessage`/`noteInboundThread`, compute
   trimmed raw `messageText(m)` (Text, otherwise Caption) and call a narrow
   `dispatchRawTelegramDiagnostics(ctx, m, raw) bool` recognizer for `/telegram`
   with any arguments and the three targeted help topics. Route these to existing
   `dispatchSlash` and return when consumed, before `runner == nil`, album
   handling and media download/ASR. Keep existing
   login, voice and ordinary-message ordering intact. Do not reinterpret voice
   transcripts as raw commands or move all slash handling; existing OAuth input
   already excludes slash commands. Cover initial owner-chat
   registration through the existing trusted path: diagnostics must work without
   a runner or `GOPOD_NO_CONTAINER`, but never grant registration to strangers.
   In `dispatchSlash`, parse command and arguments, validate qualified suffixes and
   silently consume CP-017 commands outside private chats **before** router
   lookup, unknown-command fallback, permission rendering or error sends.
   Gate `/telegram` with any arguments and the three `/help` topics. Drop
   foreign `@otherbot` commands (case-insensitive username comparison); malformed
   or multiple suffixes fail closed. SDK numeric ID is not a username. When own
   identity is unknown, fail closed for
   qualified commands rather than assuming ownership. Cache verified bot identity
   from getMe, retain it across transient refresh failures, and never make
   dispatch fetch it. Unsuffixed private commands work while identity is unknown. Non-private gates perform zero
   diagnostic fetches as well as zero sends. No new buttons/callbacks ship in v1. Bound every diagnostic/help response to one Telegram message before generic rendering; never invoke automatic page/button generation. Test maximum-sized doctor output and error copy against that bound.
7. `publishCommands` excludes `/telegram` from default/group menus and adds it
   only to `BotCommandScopeAllPrivateChats`. Hidden doctor is never published.
   Generic `/help` must filter the new entry using trusted chat kind, including
   failure/unknown-kind cases. Keep existing command behavior outside this scope.
8. Wire registration in `cmd/gopod/main.go` after Telegram construction and
   before `Run`, alongside existing control registrations.

Tracer tests: official `supports_inline_queries:true` yields enabled; explicit
false yields disabled; missing gives unknown. Fake getMe timeout still permits
`New` and dispatch of an ordinary registered DM when a runner is available. Exercise
`defaultHandler` through to the diagnostic response with no runner, initial owner
registration, captions and media attached; assert zero download/ASR/container
work. Cover album captions, pending OAuth text, unrelated `/ping` and existing
voice-transcript behavior without broad routing changes. Test own, foreign,
malformed and unknown-identity qualified commands. Owner doctor succeeds through
Router; registered nonowner cannot call its canonical or slash route; group
owner actor and unknown-kind caller cannot obtain output. Fake transport shows
one bounded getMe, no model execution and no diagnostic send probe. Private
`/telegram` displays the observed setting and honest unsupported reason.

Checkpoint: run focused tests and review this end-to-end tracer before D02/D03.
It may be deployed independently only with the same privacy/release gates as D05;
otherwise keep it as a reviewable feature-branch checkpoint.

## D02 — observations, freshness and bounded refresh

Extend the tracer's DTO/snapshot with every getMe flag named in the accepted
spec: private topics, user topic creation/deletion, group invitation/privacy,
inline, guest and business. Verify official JSON names against sanitized API
fixtures. Optional missing/ill-typed fields remain unknown. Store bot identity
only where required; never retain raw response bodies or tokens.

Use an injected clock and mutex/in-flight completion channel. Proposed defaults:
15-minute successful TTL, 30-second manual-refresh cooldown, failure backoff
30 seconds doubling to a five-minute ceiling; document constants in tests.
Startup and owner refresh share a single in-flight request. Waiting callers may
cancel without canceling other callers' service-owned request; all requests end
on Run shutdown or the five-second deadline. Never hold locks across network I/O.
Return the cached view immediately for summary reads. No request per message.
After expiry mark stale; a failed refresh retains the last good fields/time,
adds attempt time and sanitized failure class, and does not flip flags to false.
Before any success, show unknown with degraded acquisition state. Owner refresh
reports cooldown/backoff/in-flight states honestly; it cannot bypass bounds.

Classify timeout, transport, authentication/API rejection, rate limit and schema
failure using bounded enums. Respect a bounded server retry hint without leaking
response text. Never log/render raw errors, `url.Error`, request URLs, token,
headers, raw body or updates; Telegram embeds the token in its API URL. Test
synthetic secrets in URL/body/wrapped errors against both logs and reports.

Populate implementation states and required-versus-requested update sets from
actual handlers and the existing `allowedUpdates` source. Do not add subscriptions.
Read local runtime facts only from configured runner and pre-existing observed
health; absent evidence stays unknown. Do not launch work to infer readiness.
No getChat/getChatMember probes in this DM-only release. Explain that group
privacy and invitation flags do not prove membership/admin rights and business
enablement does not prove connection or `can_reply`.

Tests: fake-clock TTL/cooldown/backoff boundaries, true/false/absent/malformed
fields, envelope error/invalid JSON/non-2xx, 429, caller cancellation, shutdown,
concurrent refresh single-flight, stale recovery, immutable snapshot copies and
race safety. Assert refresh's outbound method allowlist is getMe only.

## D03 — deterministic reports and help

Implement compact registered-DM summary and detailed owner-DM report using the
injected contract. Doctor includes observation source/time/age, last attempt,
freshness, per-setting tri-state, implementation, required/actual subscriptions,
policy/runtime/context unknowns and effective reasons. Summary avoids owner-only
configuration details. Full doctor is never exposed through summary `Data`,
help, error text, logs, buttons or unknown-command suggestions.

Treat effective state as a diagnostic explanation, never an execution gate.
Table tests must cover enabled-but-unimplemented, implemented-but-not-subscribed,
policy-restricted, missing runtime evidence, stale enabled observation and unknown
flag. Distinguish private topics from group forum topics. Do not claim deletion
callbacks or bot-to-bot support absent evidence.

Extend `helpHandler` with exact guest/inline/topics argument handling. Explain
BotFather enablement versus local support and what the user can currently do;
private topic support may be partial. Use deterministic bounded copy, generic bot
DM URL and the literal command to enter there. Do not advertise an unimplemented
`/start help_guest` deep link. Unsupported arguments retain safe usage behavior.
No unsolicited notices; no read of real updates/config secrets for help.

Tests: summary/doctor permission matrix and nested Router dispatch, registered
versus unregistered private DM, full report not present in nonowner payload,
private help topic fixtures, no LLM fallback, missing data and stable reason codes.

## D04 — integration verification

Integration owner merges workers' changes and traces every new output path:
message slash dispatch, router error, malformed arguments, command discovery,
unknown chat kind, qualified foreign commands and the diagnostics-only early
route before runner/media handling. Keep v1 text-only; no callback/token scope
extension is authorized. Test
zero outbound sends/edits/errors and zero diagnostic fetches for group,
supergroup and channel CP-017 invocations, including
`/telegram@bot doctor`, unknown subcommands and all new help topics. Existing
group commands are outside this change; do not claim CP-019 completion.

Regression tests verify unchanged allowed_updates, update cursor/middleware,
ordinary DM handling under failed getMe, startup/shutdown without leaked refresh
goroutines, and menu publication only at normal startup (not refresh). Inspect
SDK identity use after WithSkipGetMe rather than assuming startup success proves
all commands work. Topic replies retain ThreadID; permissions stay chat-scoped.

Run and record:

```sh
gofmt -w <changed-go-files>
go test ./internal/control ./internal/telegram
go test -race ./internal/control ./internal/telegram
go build ./...
go vet ./...
go test ./...
```

Use actual changed paths instead of the formatting placeholder. Fakes serve all
local tests; no live credentials or paid calls. Update user-facing diagnostics
instructions and HANDOFF/status with implemented versus deferred scope only after
code exists. Add ADR only if implementation makes a durable architectural choice.

## D05 — review, CI, deployment and acceptance

Independent reviewer checks the final diff and attempts permission bypass,
group/error leakage, token-in-error leakage, SDK constructor/identity regression,
cache races and accidental subscription/probe additions. Fix findings, rerun
relevant checks, then require green repository build/vet/test CI and any required
repository quality checks on the reviewed commit before squash merge. Do not
call a successful local test or push deployment proof.

Release owner follows the current Pi deployment procedure: record exact merged
SHA and green CI, online SQLite backup with integrity/mode check, previous image
reference, build with VERSION set to the new SHA, and activate only the gopod
service with `docker compose up -d --no-build gopod`. Preserve agent image tags,
credentials, mounts, provider choice, sessions, history and scheduled tasks.
No migrations or BotFather toggles are expected. Record running image/revision,
startup, restart/OOM state and actual polling initialization. Roll back to the
recorded prior image on startup/polling regression, unintended public output or
secret leakage; preserve the database and sanitize incident evidence.

Read-only live acceptance, using existing enabled settings and a real Telegram
client (no settings toggles, guest invitations or model calls):

- Registered owner private DM summary and doctor match a sanitized current
  getMe observation, with timestamp/freshness and unsupported modes explained.
- Registered nonowner private DM sees only summary; doctor denied privately.
  If no test account exists, mark this client check pending; fake tests remain
  evidence only for the tested authorization path.
- Existing group invocation of new diagnostics/help topics produces zero public
  response, including malformed/denied variants; confirm private menu discovery.
- Owner refresh changes attempt/time state within bounds and preserves cache;
  startup/network-failure behavior is proved with fake transport, not by breaking
  production connectivity or credentials.
- Existing voice/tasks functionality, polling and task/session counts remain
  intact. Do not spend tokens to requalify model execution for this feature.

Write `docs/deployments/<date>-<sha>.md` with evidence and remaining client checks.
A healthy container is insufficient to close Telegram client acceptance. Planning
completion is not implementation, deployment or acceptance completion.
