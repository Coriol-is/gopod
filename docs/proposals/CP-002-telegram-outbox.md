# CP-002 — Telegram outbox with retry and `uncertain` state

| | |
|---|---|
| Status | in-progress |
| Date | 2026-10-05 |
| Origin | feature survey of `rzaytsev/codex-agent` (2026-10-05) and the 2026-10-03 public-release audit |
| Size | M |
| Touches | `internal/store` (new `outbox` table), `internal/telegram` (all sends and edits go through it) |
| Depends on | DT (durable turns, done) |
| Spec | [Design](../superpowers/specs/2026-10-26-telegram-outbox.md) |

## Current reconciliation — 2026-11-05

Status remains **in-progress**. Pi `757faa2` includes #169 and #172–#176;
immutable plans, code-inline alternatives, presentation parity and durable Stop
foundations are deployed. General preview/control generation ownership,
reply-action parity, qualified foreground producer/cutover and native/client
acceptance remain open. `WithResultFreeze` and `PrepareFrozenFinalReply` have no
production callers. See [active backlog](../BACKLOG.md) and
[completed slices](../history/BACKLOG-2026-11-05-completed.md).
Dated sections below preserve design/implementation history; their old lifecycle
and undeployed statements do not override this reconciliation.

## Problem
Final Telegram delivery lacks a durable outbox; many sends and edits are best-effort (`sendFormatted`, `editMessage*`, `react`). Bounded retries can still exhaust on 429/5xx and lose a best-effort reply; a network blip after the send but before `SetTurnReply` leaves the turn without a memo, so recovery replays it and the user gets a duplicate (parked finding from the durable-turns review). Auth URLs and device codes are sent immediately and may be stale by the time Telegram delivers them.

## Proposal
An `outbox` table: one row per outbound message or edit (`pending → sending → sent | uncertain | failed`), a single sender goroutine that retries 429/5xx up to N times honouring `retry_after`, and marks network/unknown responses `uncertain` instead of pretending exactly-once. Login payloads are lazy: the row carries a reference, the sender resolves the current code at send time and drops expired ones. The reply memo (`turns.reply_msg_id`) is written by the sender when Telegram acks, closing the duplicate window.

## Out of scope
Streaming edits every second do not go through the outbox (too chatty); only final messages, reactions, notices and login payloads do. Exactly-once delivery.

## Risks
Ordering across chats and within a chat must be preserved by the sender. The streaming placeholder edit path and the outbox must not both write the final text.

## Acceptance
- Kill Telegram connectivity for 30 s mid-reply: a definitely unsent reply retries when connectivity returns; an ambiguous result is recorded as uncertain, without an exactly-once claim.
- A 429 with `retry_after` is honoured (test with a fake API).
- `kill -9` between send and memo yields a truthful uncertain/reconciliation state rather than blindly replaying a potentially delivered reply.
- Expired login codes are never sent; `/login` after expiry issues a fresh one.

## 2026-10-25 reconciliation
IPC message receipts and process-local terminal outcomes now exist (PR #37),
but are diagnostic and not a durable outbox. This proposal remains open.
Delivery target/visibility must be retained across retry; CP-019 private results
must never become public when eligibility expires. Do not broaden streaming
edits into a durable queue merely to implement final-message reliability.

## 2026-10-26 adoption design

The linked spec now defines this candidate in the [adoption map](2026-10-26-codex-agent-adoption.md),
wave **1**. Status remains proposed; a saved design does not approve
implementation or change the existing delivery queue. New acceptance/migration
requirements in the dated spec supersede conflicting early shorthand here.

## 2026-10-26 implementation planning

Canonical design refined against Go source at `33e026a291fbe577efabf43e9d0b6e760fd19ece`.
[Bounded implementation plan](../superpowers/plans/2026-10-26-reliability-delivery.md)
defines schema, failure recovery, test criteria and staged release gates. Status remains
**proposed**; no runtime code or feature enablement is included. The canonical spec supersedes
earlier retry shorthand: generic 5xx/timeouts are uncertain, never presumed safe to resend.

## 2026-10-27 implementation start

Owner accepted implementation on 2026-10-27. Work is split into the eight PRs of the
[delivery plan](../superpowers/plans/2026-10-26-reliability-delivery.md). PR 1 (store identity,
result and delivery foundations) is in progress: additive tables and Store APIs only, no dispatcher,
no Telegram changes, no behavior change for existing paths. Features stay disabled until the plan's
gates pass; the outbox is not enabled by merging PR 1.

## Implementation checkpoint — 2026-11-03

PR #145 provides the runner freeze seam; PR #160 (`f5ba97a`) adds atomic ordinary
batch attempt recording and trusted handler counters. Full foreground ownership,
frozen-result publication recovery and cutover remain open. See
[PR 6b evidence](../reviews/2026-11-03-batch-start-foundation.md).

## Parallel follow-up checkpoint

Ordinary frozen-result recovery/visible holds and NoReplay transport handling merged in PR #164 (`e8b58b8`). Published plans remain outbox-owned; unpublished results are not guessed into text-only plans. Foreground freeze/publication activation remains gated on voice/code/stream plan parity and late cancellation settlement. [Evidence](../reviews/2026-11-03-freeze-next.md).

## Implementation checkpoint — 2026-11-03

Immutable final intents, atomic multi-preparation publication and ready-plan boot recovery deployed in `a25f22a` (#166, D063). Foreground producer, preview/cancellation and conditional code-upload fallback remain gated.
See the [rollout evidence and remaining gates](../deployments/2026-11-03-final-plans-health.md). Status remains in-progress.

## 2026-11-05 implementation continuation

PR #172 (`a016b82`) makes the existing explicit photo-refusal document alternative a separate transactional attempt. Retries preserve the branch; ambiguity never permits replay. Conditional code-inline content subsequently merged in PR #173 (D065); preview/cancellation/reply-action and foreground cutover remain open.
Merged after review/full checks/required CI; not deployed. See [HANDOFF](../HANDOFF.md)
for evidence and remaining gates. Status remains in progress.

## D065 implementation — 2026-11-05

PR [#173](https://github.com/Coriol-is/gopod/pull/173), `0aa6628`, implements bounded
immutable code-inline alternatives with durable branch selection and no replay
after unknown effects. Four-document/2 MiB bounds fall back to all-inline
preparation; old plans remain immutable. Full checks, affected races, independent
review and required CI passed. [Evidence](../reviews/2026-11-05-code-inline-alternatives.md).
Not deployed; producer remains disconnected. Status remains in-progress.

## Successful-final ownership — 2026-11-05

PR #174 adds atomic Stop versus final ownership in the existing synchronous,
streaming and fallback senders. Exact generation binding survives delegation;
consumed results remain available to stopped settlement. Independent review,
full checks, full Telegram races and required CI pass; merged as `40f368f`. Not deployed.
[Evidence and scope limits](../reviews/2026-11-05-final-owner-handoff.md).
Persistent frozen cancellation, complete preview/control ownership, reply-action
integration and foreground producer activation remain open. Status in-progress.

## Stopped-result publication fence — 2026-11-05

PR #175 refuses new interactive-result publication when any member is currently
persisted as stopped, inside the publication transaction. Boot retains the plan
as a hold; existing publications remain outbox-owned. Independent review, full
checks/races and required CI pass; merged as `a63c324`. [Evidence](../reviews/2026-11-05-stopped-result-publication.md).
No schema or activation change. Immutable cancellation intent before native
completion and published-delivery settlement remain open. Not deployed.

## D066 implementation — 2026-11-05

Owner accepted the [durable Stop contract](../plans/2026-11-05-durable-stop-intent.md).
PR #176 implements immutable complete-batch intent before native cancellation,
reciprocal publication fences, typed Stop outcomes, preview sealing and late
completion evidence. Accepted Stop sends acknowledgement/control retirement only.
Merged as `51fbb7a`; independent review, final checks/races and required CI pass. [Evidence](../reviews/2026-11-05-durable-stop.md).
No deployment or foreground activation. Native exit/remote delivery remain
separate evidence; full preview metadata/reply-action parity and producer cutover
are still open. CP-002 remains in-progress.

## Durable reply-action readthrough — 2026-11-06

PR #183 (`261805e`) restores More/Back/As file from fully ACKed frozen results
with exact scope/topic/message/markup checks and conservative supersession.
[Review and validation](../reviews/2026-11-06-reply-actions-ack.md): full checks,
races, independent review and required CI PASS; not deployed. Legacy control
repair versus reused final edits, More/Back checkpoints, eager cleanup, mixed-path
ordering and integrated foreground qualification remain open. Status in-progress.

Deployment checkpoint 2026-11-06: all merged slices above shipped in `80f4b11`
([rollout](../deployments/2026-11-06-reliability-cycle-rollout.md)); implementation
and native/client activation gates remain open.

## Next reliability slice — 2026-11-06

PR #187 (`0cd8078`) adds local message edit arbitration and More/Back old-control
retirement, with fair held-row scheduling and obsolete Stop requeue protection.
Unknown remote repair settlement remains a foreground activation gate.
[Review](../reviews/2026-11-06-control-final-arbitration.md).
Full checks, races, review and required CI PASS; status remains in-progress.

Deployment closure 2026-11-06: #186/#187 shipped in `0cd8078`, with exact-main CI,
verified backup, health/state and Telegram version PASS.
[Rollout and cleanup](../deployments/2026-11-06-control-topic-rollout.md). Parent activation gates remain.

## Durable repair barrier — 2026-11-06

#189 (`a85b328`, D069) adds persistent repair attempts and exact-evidence settlement,
blocking conflicting edits and conserving outbox budgets across unknown outcomes.
[Review](../reviews/2026-11-06-durable-repair-outcomes.md). Full checks/races, review
and required CI PASS. Historical/non-repair requests, unsupported unknown resolution,
eager cleanup, mixed-path ordering and foreground qualification remain open.

Deployment closure2026-11-06: #189 shipped in `a85b328`; exact-main CI, verified
pre/post-migration backups, health/state and Telegram version PASS.
[Rollout and housekeeping](../deployments/2026-11-06-durable-repair-rollout.md). Parent gates remain open.

## Direct edit outcomes — 2026-11-06

#191 (`6820422`, D070) implements registered-private direct text/HTML/keyboard
provenance and durable attempts, symmetric barriers and outer NoReplay propagation.
Full checks/races, review and required CI PASS; [report](../reviews/2026-11-06-durable-edit-outcomes.md).
Historical missing provenance holds, groups/unregistered/non-edit operations, eager
cleanup, mixed-path order and integrated foreground qualification remain open.

Deployment closure 2026-11-06: #191 shipped in `6820422`; exact-main CI,
pre/post-migration backup verification, health/state and Telegram version PASS.
[Rollout and cleanup](../deployments/2026-11-06-direct-edit-rollout.md). Parent/compatibility gates remain.
