# Telegram private status panel — product specification

Date: 2026-11-11. Status: **design draft; documentation authorized, implementation
not started**. [CP-041](../../proposals/CP-041-telegram-widgets.md).
Companion: [protocol and lifecycle](2026-11-11-telegram-widget-protocol.md).
Audited checkout: `da529a8`; no production probe or client acceptance performed.
The choices below are proposed implementation defaults, not a deployed ADR.

## 1. Outcome and scope

The owner opens a compact status panel in the private bot conversation and refreshes
it without adding another status message. The panel reports observable state; it
never launches an agent or changes execution settings. V1 supports the owner's
registered private chat, including its admitted topics. Other private users,
groups, inline messages and forwarded/shared widgets are outside V1.

First release contains current execution state, waiting queue count and configured
provider/model. A task browser, charts and control buttons are future candidates,
not acceptance requirements. No changes to existing task execution, foreground
publication flags, parallel sessions, login or restart semantics are authorized.

## 2. Entry and compatibility

Proposed command: `/panel`, registered through internal/control with OwnerOnly
permission plus a registered-private-context check. Owner status alone does not
permit use from a group. `/panel text` returns the same bounded snapshot as text.
CLI may expose the text view but must not create a Telegram widget implicitly.
No parameter picker is needed for the default command; the explicit text form is
an escape hatch, not a persistent preference change.

Proposed gate: `GOPOD_WIDGETS_ENABLED=0` by default. Disabled mode returns the text
view and sends no widget. Startup subscribes to widget_query after SDK support is
present even when the gate is off, so old cards can receive a disabled response.

In enabled mode, `/panel` creates a new panel in the originating conversation.
Each admitted command invocation is distinct; duplicate Telegram update delivery
must not create another logical send. V1 does not promise one pinned singleton.
Creation admission is limited to one request per conversation per 30 seconds;
refused requests receive text through existing safe command response machinery.

A panel has a 24-hour refresh lifetime. A new explicit `/panel` is required after
expiry; no background renewal or replacement sends. Its caption includes the
creation time, that it is a snapshot, and `/panel text` for unsupported clients.
The caption is intentionally not described as a live view.

Telegram does not supply a trusted per-user widget capability bit here. A successful
send does not prove rendering. Keep text commands accessible on every client;
do not infer compatibility from SDK support, getMe, or HTTP acceptance.

## 3. Display contract

User-facing copy is Russian. Repository templates and identifiers are English.
Use native Telegram colors, ordinary text and one Refresh button. Avoid external
assets, custom fonts, animation, scrolling containers and keyboard input. Target
320–430 logical-pixel layouts; allow content height to fit. No model-generated
HTML. Dynamic strings enter text nodes, never innerHTML or script source.

| Field | Meaning | Missing/error behavior |
|---|---|---|
| Observation time | UTC server snapshot time, displayed in a readable local form | No successful timestamp advancement on failure |
| Execution | idle / queued / running / stopping / unknown, derived from an approved scoped read adapter | Unknown, never infer idle from missing data |
| Waiting | Queued items for the resolved execution scope, excluding running work | null means unavailable, zero means observed empty |
| Provider/model | Effective configured selection for that scope | null displayed as unknown/default as appropriate; not a claim about an active process |
| Active elapsed time | Derived only from a known current-run start time | Hidden when no authoritative start time |
| Scope label | Conversation, or shared workspace when current legacy routing aggregates topics | Must not imply per-topic isolation that does not exist |
| Freshness | Snapshot age, disabled/expired/unavailable indicators | Last good data remains visibly stale on errors |

Do not display prompts, task bodies, output excerpts, filesystem paths, credentials,
session identifiers, other users' state or fabricated progress percentages.
Configuration and observed execution are separate concepts. If the configured
model differs from the active process, V1 labels it as configuration rather than
claiming the process changed model.

Initial card data is a historical snapshot. On activation, request fresh data once;
thereafter update only on Refresh. No interval polling in V1. Coalesce repeated
activation/refresh requests within five seconds and allow only one request in
flight per instance. A client-side guard is an optimization, not authorization.

A successful query answer redraws only the requesting instance. Another device
refreshes independently. The persisted message snapshot and caption remain at
creation time. On timeout/error, retain last good data, show stale/error state and
re-enable Refresh. Never leave a permanent loading spinner. Server-side deadlines
and local error handling must cover disabled, expired and unavailable responses.

## 4. Source-of-truth adapter

Control owns a typed read-only snapshot service consumed by text and widget
presentations. Do not scrape command text. Controller wiring supplies read adapters
from existing store/queue/runtime APIs; Telegram performs no raw SQL.

The implementation must inventory the actual provider-resolution and queue scopes
before coding the adapter. Current legacy mapping may share execution state across
topics. Preserve that mapping and label its scope; never enable CP-037 to make the
panel look more precise. Use a consistent store read where possible. Runtime and
store samples need individual availability flags if they cannot be captured
atomically; do not advertise a transactionally consistent runtime snapshot.

Read operations must not refresh external connectors, start Docker containers,
probe credentials or invoke LLMs. Collection budget: two seconds; unavailable
subfields become null with partial=true. Responses must remain useful on an idle
or temporarily unavailable runner.

## 5. Suggested delivery slices, after implementation authorization

1. Qualify a pinned SDK version with widget models, methods and update decoding;
   add protocol fixtures and verify existing bot behavior remains compatible.
2. Build the scoped snapshot adapter and `/panel text` with authorization tests.
3. Add durable widget creation and receipt/ownership handling, gate still off.
4. Add fixed template, read-only query answers and expiry/disable handling.
5. Qualify native clients and rollout only after required review/CI and an explicit
   implementation task. Gate defaults stay off until acceptance is recorded.

These are dependency boundaries, not an executable implementation plan or a change
to the approved roadmap priority. No ADR is appended by this draft.

## 6. Acceptance matrix

| ID | Check | Evidence required |
|---|---|---|
| W01 | Flag off and `/panel text` return bounded useful text | Control/transport tests, no widget HTTP |
| W02 | Owner private main and topic targets work | Exact message route and durable ownership tests |
| W03 | Non-owner, group, unregistered, wrong-message and retired-topic requests denied | Router and ingress tests; no state leakage |
| W04 | idle/running/stopping/partial/unknown and legacy shared scope displayed honestly | Snapshot fixtures and rendered examples |
| W05 | Refresh and reactivation update one instance without new chat messages | API trace plus native client observation |
| W06 | Another device refreshes independently; initial snapshot stays historical | Two-client acceptance with versions recorded |
| W07 | Unsupported client has usable caption and text escape hatch | Native observation where such a client is available; otherwise explicitly unqualified |
| W08 | Light/dark, narrow layout, long Unicode model names, stale/errors | Native screenshots or direct observed evidence with versions |
| W09 | Timeout, restart, send ambiguity, DB-ACK failure and duplicate update | Fault tests from companion spec |
| W10 | Backup idle, topic fences, retention and restore do not lose unresolved effects | Store/integration tests |
| W11 | Gate off after prior creation, 24-hour expiry and unknown template version | Query tests and user-readable states |
| W12 | No runner invocation or mutations from arbitrary query payloads | Negative tests and independent review |

API success is transport evidence only. Browser preview is layout evidence only.
Do not close client acceptance from either. Source changes require build, vet,
tests and mandatory CI; concurrency changes additionally require race coverage.

## 7. Evidence and limits

Official sources, checked 2026-11-11:
- [Bot API sendWidget](https://core.telegram.org/bots/api#sendwidget)
- [Widgets guide](https://core.telegram.org/bots/widgets)

Production widgets are currently private-chat-only. Templates have strict byte
limits and no network access. Unsupported clients expose a caption, not the UI.
Recheck availability and client behavior before implementation/rollout.

Local anchors:
- [Pinned dependency](../../../go.mod)
- [Update subscription and bot wiring](../../../internal/telegram/bot.go)
- [Existing callback authorization](../../../internal/telegram/callback.go)
- [Current transport operations](../../../internal/telegram/outbox_transport.go)
- [Active gaps](../../BACKLOG.md), [prior reliability decisions](../../DECISIONS.md)
