# gopod — Roadmap

Latest rollout: `757faa2` on Pi, 2026-11-05 15:11 UTC, includes #173–#177;
no worker. D068 guide migration and bounded read-only matching-consumer acceptance
passed; unrelated native/client gates remain. [Evidence](docs/deployments/2026-11-05-stop-ipc-rollout.md).


Live milestone tracker. Update the **Status** column whenever you change reality.
For the *why* behind each milestone, see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md);
for integrations, see [docs/INTEGRATIONS.md](docs/INTEGRATIONS.md); for memory,
see [docs/MEMORY.md](docs/MEMORY.md). For the rolling "what's actually happening
right now" view, see [docs/HANDOFF.md](docs/HANDOFF.md).

## Status legend

- ⬜ not started
- 🟦 in progress
- ✅ done
- ⏸️ paused
- ❌ dropped (link to ADR)

---

## Current backlog — 2026-11-04

The [code-backed backlog](docs/BACKLOG.md) records production call sites, gaps and
verification at `38e8eba`; use it before selecting implementation work.

Production is `d58cdd2` (2026-11-05): controller and owner runtime healthy,
zero controller restarts/OOM, all eight idle counters zero, durable state hashes
preserved. Exact-main CI and Telegram `/version` passed. Pi controller only;
[rollout evidence](docs/deployments/2026-11-05-queue-delivery-ownership.md).

| Work | Current status | Remaining step |
|---|---|---|
| Concurrent queue idle handoff | Fixed and merged as `01cc7e0` (PR #168); review and required CI pass | Deployed as `d58cdd2`; [evidence](docs/reviews/2026-11-04-queue-idle-handoff.md) |
| CP-002 final delivery | In progress; immutable ready plans and recovery deployed | Qualified foreground cutover and full preview/reply-action parity; D065 and D066 merged, rollout/native acceptance pending |
| CP-016 topic deletion | In progress; runtime/store/outbox fences deployed | Durable legacy ownership, ambiguity/managed settlement, native-login qualification, confirmed delete command |
| CP-007 backups | In progress; backup foundation deployed | Timer/retention, full capture and restore command/drill; leases only for online matched mode |
| CP-004 managed tasks | Partial implementation; activation off | Settlement/visibility and schedule policy; credential adapter remains owner-deferred |
| Healthchecks | Implemented/deployed; controller healthy now | Optional-runtime acceptance and separately authorized reboot qualification |
| CP-038 elevated | Implemented/deployed; bounded native Codex acceptance passed | Claude browser-completion acceptance; no current elevated runtime |
| CP-020 isolation | Implemented | Remaining provider/client qualification |
| CP-018 / CP-019 / CP-014 | Accepted designs, implementation queued | Per-conversation providers after CP-016; group UX; media queues |
| CP-035 hooks | Proposed research; documents merged | Research decision/experiments, no hooks activated |

CP-003, CP-033 and CP-037 retain partial implementation scope. CP-040 is
implemented/deployed, with A22–A25 acceptance gaps; CP-033 templates/outcome display
are already wired, while connector readiness/probes remain open. No work starts merely because this table is refreshed.

## Execution priority — 2026-11-04

Reprioritized at the owner's request after the code audit (`d3be6f8`). This order
supersedes the 2026-10-25 queue and the earlier November handoff recommendations.
It changes priority, not CP lifecycle, architectural contracts, activation flags
or owner-deferred scope. The reprioritization itself started no implementation; item 1 was subsequently
completed in PR #168. Owner update: CP-007 is lowered below the current product
queue; CP-002/003 and CP-016 implementation resume first. Existing restore and
activation gates remain requirements, not a prerequisite to unrelated code work.

| Order | Deliverable | Why now / completion gate |
|---|---|---|
| 1 — done on main | Concurrent queue regression fixed in PR #168 (`01cc7e0`) | Shared ordinary/managed idle retirement race; deterministic baseline failures, fixed stress/race, review and required PR CI pass. Deployed as `d58cdd2`. |
| 2 | CP-002 foreground delivery, with the necessary CP-003 slices | Finish an existing reliability investment: wire freeze/preparation/publication with fallback, previews, cancellation and reply-action parity. D063/D065 and staged acceptance remain the activation gates. Protected-challenge work follows its existing contract; do not redo shipped attachment delivery. |
| 3 | CP-016 complete topic retirement | Finish durable ownership, ambiguity reconciliation and managed settlement before command orchestration. Qualify native-login exit and the full confirmed API/outcome/retirement path under D064. The command stays unavailable until these gates pass. |
| 4 | CP-018 per-conversation provider/model selection | Follows CP-016 as already agreed; delivers everyday conversation control. Reconcile with CP-037 routing contracts without activating paused parallel execution. |
| 5 | CP-019 group command/service UX | Bounded user-visible improvement after core conversation reliability; keep ordinary group output public. |
| 6 | CP-033 connector readiness and discovery | Finish probe/setup/recheck/offer behavior around already shipped presets. Do not add more presets before readiness is trustworthy; connector consent remains with the owner. |
| 7 | CP-014 media queues | Q1 fidelity/cards/links first, then Q2 document/image/speech, then Q3 video. Keep each queue separately reviewable. |
| 8 — lowered by owner | CP-007 recoverability, then automation | Existing snapshots do not cover full session/auth/workspace recovery. First complete capture coverage, restore epoch handling and the restore/activation path; qualify an isolated scratch restore and Pi drill without overwriting live state. Then timer/retention under the accepted maintenance window. Do not expand this into CP-039 migration or online matched mode. |

**Acceptance lane, alongside independent implementation:** CP-040 A22–A25 first,
then remaining ordinary Stop/restart and CP-020 provider/isolation checks. Keep
one Telegram actor and avoid overlapping tests with deployment or auth changes.
Record partial results honestly; visual/native checks require their actual evidence.
Flag-off/restart cases need a suitable isolated setup or the applicable operational
authorization. CP-038 Claude browser completion waits for the owner-mediated login
step; it does not block ordinary reliability work. Optional-runtime health checks
run when those runtimes are used; no standalone reboot project is introduced.
Any confirmed security, data-loss or ordinary user-facing failure found here moves
ahead of feature work, with the evidence and revised priority recorded.

**Held outside the active sequence:** CP-004 broad managed-task activation remains
blocked on the owner-deferred credential adapter; only settlement primitives needed
by items 1/4 belong in those slices. CP-037 P2–P7/T5/T5b remain paused. CP-035 hooks,
CP-039 migration, new providers/runtimes and other proposed research stay deferred.
CP-011/012/032/034 remain accepted designs outside this execution sequence.
No new dates or effort estimates are asserted before each slice is scoped.

CP-002/003 successful presentation parity merged in PR #169 (`c1966dc`) with
review, full checks and green required CI/Sonar; remaining foreground gates stay
open. CP-016 main-control ownership merged in PR #170 (`dacefc0`) after combined
checks, independent review and required CI/Sonar. Remaining ambiguity, native and
managed settlement gates keep the delete command unavailable. Both slices were deployed as `d58cdd2`; neither completes its parent proposal. [HANDOFF](docs/HANDOFF.md#completed-implementation-wave--2026-11-04)
records evidence and remaining gates. Downstream items remain queued; CP-037 stays paused.

2026-11-05 continuation: receipt-backed DB-ACK repair merged in PR #171
(`930586a`); durable photo-to-document attempt selection merged in PR #172
(`a016b82`). Both passed independent review, full checks/races and required CI.
Both were deployed as `d58cdd2`. Code-inline alternatives subsequently merged in
PR #173 (D065), with independent review, full checks/races and required CI;
that new slice is not deployed. Foreground cutover, preview/cancellation/reply-action
integration and unknown remote reconciliation/delete orchestration remain open;
backup priority unchanged. PR #174 adds a bounded process-local successful-final
handoff against late Stop after joined previews; review and local full/race checks
and required CI pass; merged as `40f368f`, not deployed. Persistent frozen cancellation, terminal-error UI and
complete preview ownership remain separate gates. PR #175 adds a publication
fence against currently stopped result members and boot recovery holds; review
and full checks/races/required CI passed; merged as `a63c324`, not deployed. Settlement of already published delivery remains a separate gate. The owner accepted [D066](docs/plans/2026-11-05-durable-stop-intent.md); PR #176 implements durable Stop and late completion evidence. Review, final checks/races and required CI passed; merged as `51fbb7a`, no deployment or foreground activation. The task IPC race is fixed in source by D067/D068, PR #177 (`cbdd9e3`), after independent review, full checks/races and required CI. [Claim/recovery and matching-consumer evidence](docs/reviews/2026-11-05-task-ipc-claims.md). Deployment and existing-consumer migration remain pending; resume preview/control generation ownership and reply-action parity before foreground cutover.

## Parallel sessions — CP-037 (P0 and P1 merged and deployed in legacy mode, P2+ not started)

🟦 P0 lifecycle safety merged as `5c36b81` via [PR #126](https://github.com/Coriol-is/gopod/pull/126)
([D054](docs/DECISIONS.md), [acceptance](docs/reviews/2026-11-01-p0-acceptance.md)):
no busy-runtime eviction, `/restart` drain, confirmed `/restart force`, startup
quarantine with a live control plane, durable Stop-control repair, opt-in Pi 4
profile. Deployed in `be7575f` on 2026-11-01; real Telegram acceptance open. P2–P7 remain unstarted.

🟦 P1 durable session identity merged as `3409907` ([PR #133](https://github.com/Coriol-is/gopod/pull/133), [D056](docs/DECISIONS.md)) and deployed with mapping `legacy`: additive session/binding/claim tables, atomic `/sessions adopt` at next restart, default stays legacy; real native resume after adoption remains open.

Owner reopened design on 2026-11-01: private chats split by thread plus background
tasks; persistent topic folders and explicitly attached shared projects.
[Architecture and implementation contracts](docs/superpowers/specs/2026-11-01-parallel-sessions.md).
Lifecycle safety P0 precedes parallel execution; ordinary restart drains, explicit
`/restart force` cancels background work. Mixed-provider routing, model changes and explicit quota fallback are consolidated
in contract D. These remain specifications only.
Owner authorized P0 code, parallel worktrees, acceptance and automatic merge.
T5/T5b parallel-runtime implementation remains paused; T9 memory-cgroup reboot was separately authorized and completed on 2026-11-03; broader T5 activation remains deferred. The separately authorized reliability rollout was deployed as `a0de4dc` ([evidence](docs/deployments/2026-11-02-reliability-streams.md)).

## Elevated host session — CP-038 (implemented; remaining acceptance open)

✅ Implementation shipped. Design merged as `2ca967c` via [PR #128](https://github.com/Coriol-is/gopod/pull/128).
E0 images (#150), E1 profile/guard (#151, D059) and E2 journal/reserved identity (#152, D060) are merged and deployed as `75b3be3`; E3–E5 merged in PR #154 (`294e8a7`) after independent review and green CI, and deployed on Pi. E6 host cgroups and lifecycle are qualified on Pi; Telegram lifecycle/callback acceptance passed; bounded native Codex acceptance subsequently passed; Claude completion remains open. Explicit Telegram login merged in PR #155 (`500a094`, D062), startup correction #156 (`474aac0`) and credential-preserving preflight #157 (`8412e3b`), with independent review and CI passed. Live prompt/cancel checks passed; owner restored Codex auth after the re-login incident. Completion notification fix #158 (`1a3b9e1`) is deployed; auth preservation and a bounded native Codex reply passed. Claude browser completion remains open. See [rollout evidence](docs/deployments/2026-11-03-elevated-login.md). Controller flag defaults off; owner-authorized rpi4 configuration is on. Codex ROOT is authorized; the idle session closed normally during the backlog rollout and no elevated runtime is present in the 2026-11-04 snapshot. [CP-038](docs/proposals/CP-038-elevated-host-session.md)
is `implemented`; outstanding client/provider checks remain in the acceptance register.

An explicitly started owner-private conversation with host authority on the Pi:
`Privileged: true` plus host PID/network/IPC, the Docker socket and the host root at
`/host`, its own container profile and policy role, bounded by explicit close rather than
a timer. [Spec](docs/superpowers/specs/2026-11-01-elevated-host-session.md) ·
[Plan](docs/superpowers/plans/2026-11-01-elevated-host-session.md).

Slice `E-pre` is done and was independent of acceptance: button presses carried no
`MessageID`, and could reject `/restart force|status|cancel`. The fix is included in the deployed `a0de4dc`. The hardened chat and group
profiles are not touched by any slice.

## Private-chat Stop — CP-036

✅ Explicit inline Stop and `/stop` for owner-private interactive answers merged
in [PR #124](https://github.com/Coriol-is/gopod/pull/124), `8a3ce75`, after green CI
and source re-review (including the buffered voice-only regression fix). Ordinary
messages remain queued. Deployed to Pi at 20:00:48 UTC with backups and state
verification; [evidence](docs/deployments/2026-10-31-private-stop.md).
A08/A10 real-client acceptance remains open.
[Proposal](docs/proposals/CP-036-private-chat-stop.md) · [Spec](docs/superpowers/specs/2026-10-31-private-chat-stop.md)

## Security fix wave — 2026-10-31

✅ All eight groups from the [2026-10-30 review](docs/reviews/2026-10-30-security-logic-review.md)
merged through `e586c4a` (PRs #113–120), group 1 first, with independent review and green CI.
See [acceptance evidence and limits](docs/reviews/2026-10-31-security-wave-acceptance.md).
Deployed to Pi as `e3b5401` on 2026-10-31 00:24 UTC; [operational checks passed](docs/deployments/2026-10-31-security-wave.md).
Existing feature flags preserved. M9 credential derivation and real-client acceptance remain open. This does not mark the larger CP-002/003/004 streams complete.

## Historical approved delivery queue — 2026-10-25

Superseded for execution order by [the 2026-11-04 reprioritization](#execution-priority--2026-11-04).
The owner approved this historical order. This is prioritization, not a claim that all
proposed designs are implemented or that every implementation's live acceptance
has passed. The proposal registry remains the single CP status source;
[ACCEPTANCE.md](docs/ACCEPTANCE.md) tracks live checks separately.

| Order | Workstream | Ordered work / dependencies | State |
|---|---|---|---|
| 1 | Qualify the deployed release | Tasks panel, delivery, upgraded CLIs, quota/provider continuity, Stop/recovery and media baseline — A01–A17 | Implemented/deployed baseline; live checks open |
| 2 | Reliability and diagnostics | CP-017 Telegram diagnosis -> CP-002 bounded outbox/uncertain delivery -> CP-004 isolated task sessions -> CP-007 scheduled backups/restore -> CP-006 maintenance/quiet hours | CP-017 private-DM slice implemented; live acceptance pending; CP-002/003/004/007 partially implemented and deployed; full cutover, managed execution and restore gates remain open; CP-006 planned |
| 2 | Conversation management | CP-016 topic deletion/retirement -> CP-018 per-conversation providers/models/quota scope | CP-016 store/outbox/runtime fences deployed; command/reconciliation and CP-018 pending |
| 3 | Group service UX | CP-019 ephemeral command/service/error replies; T14 scoped menu/strings work coordinated with transport | Spec saved; ordinary output stays public |
| 4 | Media, three queues | CP-014 Q1 cards/links/attachment fidelity -> Q2 document/OCR/image/speech -> Q3 video speech+frames/export; CP-003 typed IPC artifacts and CP-005 qualified tools support appropriate slices | Specs/proposals only beyond existing baseline |
| 5 | Platform extensions | CP-011 runtime, CP-012 registry; then additional providers/sandboxes, bounded bot-to-bot experiment if useful; secret gateway/MCP/metrics stay deferred | Design/research/deferred; no new backend or peer enabled |

Done implementation must not remain "in progress" just because a live check is
pending. Host memory-cgroup changes/reboot (T9) were completed on 2026-11-03; parallel
execution (T5/T5b) remains deferred and is not an implicit deployment step.

## Historical iteration plan — 2026-11-02 (superseded by current backlog)

Done since 2026-10-27 (all merged to `main`; production then ran `75b3be3`; outbox IPC/dispatcher and artifacts on, managed tasks off): CP-002 PR 1-5
(store, dispatcher, durable personal text IPC, immutable artifacts, voice/code preparation),
CP-004 PR 1-3 (storage, managed dispatch, policy-guarded execution), CP-007 backup foundation,
rich-message formatting P1 (D045), supervised owner `/restart` (CP-031).

| Step | Work | State / gate |
|---|---|---|
| 1 | Owner live acceptance A08–A11, A18, A20, A02, plus formatting and `/restart` edge cases | Owner in Telegram; failures become fix PRs |
| 2 | CP-020 final gates: Codex two-turn/tool canaries and cross-boundary/client checks; tree-budget fix #88 and default admission #89 shipped; historical Claude group new/resume succeeded | Private management only; no paid fallback |
| 2 | CP-002 PR 6-8 (foreground freeze/streaming ownership, protected challenges, staged release), CP-004 PR 4-5 (settlement/visibility, schedule policy) | One PR each; artifacts flag gated on CP-007 backup/restore qualification; capability issuer shipped in #110 |
| 3 | Rich messages: stickers P2/P3, [thread awareness](docs/superpowers/specs/2026-10-29-agent-thread-awareness.md), [buttons/hooks](docs/superpowers/specs/2026-10-29-agent-buttons-and-hooks.md), [reaction reading](docs/superpowers/specs/2026-10-29-telegram-reaction-reading.md) | Specs saved; no implementation |
| 4 | Task-role credential adapter (needed before `GOPOD_MANAGED_TASKS` can run anything); Pi Docker-inspect round trip | Deferred by the owner 2026-10-30 (B2 stays the recommendation) |
| 5 | Group triggers (`owner`/`mention`/`all`), owner-in-group identity for group-scoped commands, group `/login` guidance — [spec](docs/superpowers/specs/2026-10-29-group-trigger-and-owner-in-group.md) | Implemented and deployed (#93/#95/#97); security wave hardens callbacks; live client checks remain |
| Deferred | CP-014 media, CP-018 per-thread providers, T9 cgroup reboot, observability | Owner re-prioritises |

Open owner decisions: branch protection requiring `test` + `isolation-spike` (a ruleset requiring one approval exists; admin squash-merge is the working path); whether to add a CD workflow.

The owner resumed queue streams 1–3 in parallel on 2026-11-02. That
implementation wave started CP-002 PR 6 result-freeze integration and CP-016
deletion lifecycle groundwork, alongside release qualification. CP-018 follows
CP-016 inside stream 3; its older deferral in the iteration table is superseded
by this instruction. Partial foundations do not complete either proposal.
Deployment and destructive live acceptance remain separate gates.

### Reliability checkpoint — 2026-11-03

CP-002 PR 6b atomic batch attempt recording merged in PR #160 (`f5ba97a`);
foreground freeze/publication cutover remains open.
[Slice boundary](docs/reviews/2026-11-03-batch-start-foundation.md).
Live acceptance found a silent/model picker regression; fix PR #159 is deployed in `4350e1b`,
with silent on/off and model/default Telegram callback retests passed. CP-016 durable outbox fences merged
in PR #161 (`602403a`): unresolved deletion holds pending delivery, refusal
reopens it, confirmed deletion cancels pending parts. At that checkpoint runtime/network quiescence and command/recovery/stream/
callback integration were still open; subsequent slices are recorded below. All three PRs passed
independent review and test/isolation/Sonar. Deployed in `4350e1b`; backups, runtime health and state preservation passed.

Parallel follow-up started from `09dbaf4`: CP-002 ordinary eligibility/frozen
recovery, CP-016 admission/quiescence and serialized Telegram acceptance in
separate worktrees. Integration/deploy subsequently completed after review and CI.
CP-016 ingress/native/callback/output fences merged in #163 (`b62eb22`); CP-002
result-aware recovery/hold and NoReplay propagation merged in #164 (`e8b58b8`).
Full foreground ownership and topic deletion remain incomplete and disabled.
Native Codex continuity/Stop and topic PDF delivery were observed; an attachment
refusal notice routing/wording defect is fixed in `33ba30a`. These slices are
deployed in `e6f0fad`; verified backup, exact state preservation and startup passed.

Final-plan/quiescence/health wave deployed as `a25f22a`: #165 healthchecks,
#166 immutable final intents/atomic preparation publication/ready-plan recovery,
#167 native/queue/transport leases and conservative fence restoration. Independent
review, full/race tests, required exact CI, backup/state checks and scoped Telegram
version acceptance passed. Full foreground cutover and `/thread delete` remain off.
[Rollout and remaining gates](docs/deployments/2026-11-03-final-plans-health.md).
Known separate follow-up: managed queue concurrent Dispatch/Enqueue stress timeout
reproduces on pre-wave main (three failures in 100 repetitions); not masked by this release.

## Planning candidates — 2026-10-26

The [codex-agent adoption map](docs/proposals/2026-10-26-codex-agent-adoption.md)
links fuller CP-002–007 specs and new proposed CP-021–030, including deferred
Telegram account reading, location, peer mailboxes and optional heavy tool packs.
The map groups reliability, research and quality slices for discussion; it does
not change the approved queue above, mark any feature implemented, or enable
new credentials, integrations, automatic learning or tool runtimes.

The reliability documentation wave now has a [shared implementation order](docs/superpowers/plans/2026-10-26-reliability-wave.md)
and separate delivery, task and operations plans. This refines dependencies within
the reliability stream; the approved workstream queue and implementation statuses
above remain unchanged.

## Build order (reordered by [D018](docs/DECISIONS.md))

The numerical IDs below are stable identifiers cited from many places —
**they do not encode build order**. Per [D018](docs/DECISIONS.md), gopod
is built in this sequence so that the first end-to-end agent reply uses
Claude Code's web-auth (Pro/Max) inside a container, not a paid API key:

`M0 ✅ → M1 ✅ → M5 ✅ → M6 ✅ → M6.5 ✅ → M3 ✅ → M3.5 ✅ → M4 ✅ → M9 ✅ → M8 ✅ → P1+P2 ✅ → ST1–ST5 ✅ → M7 ✅ → M3.6`

M2 (Direct API) is no longer on the critical path; it is opt-in if and only
if `ANTHROPIC_API_KEY` is set in the environment.

## Phase 1 — Core (M0–M9)

| ID | Milestone | Status | Notes |
|----|-----------|--------|-------|
| M0 | Skeleton: `go.mod`, `cmd/gopod/main.go`, `internal/config`, `internal/store` schema, slog wiring | ✅ | sqlite-vec verified at startup (`vec_version=v0.1.6`); ncruces pinned to v0.20.0 per [D017](docs/DECISIONS.md) |
| M1 | Telegram echo: long-poll, default handler stores every message, `/ping` replies | ✅ | `internal/telegram` wraps `go-telegram/bot`; `internal/store/messages.go` + `chats.go` ingest. End-to-end with a real bot token works without an agent |
| M2 | Direct API agent (no container): `anthropic-sdk-go`, single chat, trigger pattern, per-chat session | ⏸️ | **Optional, off the critical path per [D018](docs/DECISIONS.md).** Auto-enabled if `ANTHROPIC_API_KEY` is set; otherwise skipped silently |
| M3 | GroupQueue: per-chat serialization + global concurrency cap + backoff | ✅ | `internal/queue` with per-chat worker, buffered-chan cap (default 3), coalescing, exponential backoff (5s→80s, 5 retries). Telegram default handler enqueues; queue worker runs agent async |
| M3.5 | **Control plane** (`internal/control` Router + Telegram frontend). C1/C2 and C5 logging implemented; C3 CLI and C4 extended handlers deferred. | ✅ | Router with auth enforcement, all slash commands dispatch through it, setMyCommands built from Router.List(). /login stays as Telegram-side special case (stateful) |
| M3.6 | **Observability** (`internal/observability`: opt-in Prometheus `/metrics` + opt-in OTel OTLP traces; logs are already covered by M3.5/C5). Sub-steps O1–O6 below. See [docs/OBSERVABILITY.md](docs/OBSERVABILITY.md) and [D014](docs/DECISIONS.md). | ⏸️ | O1/O2 implemented; remaining scope deferred.  Deprioritized to end of backlog — personal bot doesn't need metrics/traces yet |
| M4 | Scheduler: cron/interval/once via `robfig/cron/v3`, `/tasks` commands via Router, poller goroutine | ✅ | store CRUD, 60s poller, /tasks add/list/pause/resume/cancel, tasks push through queue same as messages |
| M5 | Container runtime: Docker SDK, mounts, exec attach, idle kill, label-based recovery | ✅ | `internal/runner` has `mountsec` subpackage, three-tier `BuildMounts`, full `BuildContainerArgs` flag assembly, Docker client with `EnsureRunning`/`Exec`/`Stop`/`Remove`/`CleanupLeftovers`, plus the `gopod-agent:latest` image (node:22-slim + pinned Claude Code 2.1.310 + tools). Integration tests behind `//go:build docker_integration` verified end-to-end. Idle watcher is wired |
| M6 | Agent SDK in container: synchronous `claude -p` via `docker exec`, registered_chats CRUD, /register + /whoami slash commands, owner auto-register, auth detection, idle watcher | ✅ | Verified end-to-end: real Pro/Max reply on Telegram. SDK migration (per [D015](docs/DECISIONS.md) wrapper script) deferred until streaming/tools/MCP land. Three runtime fixes: drop bogus `seccomp=default` SecurityOpt, add /home/node tmpfs + HOME env, skip .env mask when RepoRoot/.env absent |
| M6.5 | **Telegram-mediated `/login`.** Interactive OAuth proxy: spawns `claude auth login` with PTY+stdin via Docker SDK, captures URL from stdout, forwards to Telegram, intercepts user's next message as the OAuth code, pipes it to claude's stdin, confirms. Session-expired vs never-logged-in distinction in error replies. 10-min timeout with auto-cleanup. | ✅ | Full onboarding fits inside Telegram — no terminal access needed |
| M7 | IPC: filesystem watcher, container → host messages, task ops, owner gating | ✅ | 2026-05-07 |
| M8 | Recovery & polish: cursor backfill, structured SQLite logs + /logs command, Docker Compose production deployment, README rewrite | ✅ | Update offset persisted for restart replay, slog → SQLite handler with D012 redaction, multi-stage Dockerfile + compose with docker.sock mount |
| M9 | Native memory: OpenAI embedder, hybrid FTS5+vec0 search, /remember + /recall, auto-injection into agent prompt via --append-system-prompt | ✅ | Agent gets cross-session memory context automatically. User manages memories via /remember + /recall. Extraction, compaction and memory container skill implemented; live skill acceptance A14 |

### M3.5 sub-steps (control plane)

| ID | Step | Status |
|----|------|--------|
| C1 | Router scaffold: `Command`, `Caller`, `Perm`, `Response`, `Router.Register/Dispatch`, auth enforcement, unit tests | ✅ |
| C2 | Telegram frontend: `parseSlash`, dispatch wiring, `/ping`, `/whoami`, `/version`, `/help` | ✅ |
| C3 | CLI frontend: argv parser, `serve`/`migrate` reserved subcommands, `--json`, mirror of public commands | ⬜ | Deferred |
| C4 | Remaining extended handlers: queue/container/system; chat registration already implemented | ⬜ | Deferred |
| C5 | Logs subsystem: `internal/log/sqlite_handler.go`, retention task and `/logs` (CLI frontend remains C3) | ✅ | SQLite slog handler with D012 redaction landed in M8 |

### M3.6 sub-steps (observability)

| ID | Step | Status |
|----|------|--------|
| O1 | `internal/observability` scaffold: `Init`, no-op providers, config loader, shutdown | ✅ |
| O2 | Metric definitions in one place, registry, `127.0.0.1:9090/metrics` listener gated by `GOPOD_METRICS_ADDR` | ✅ |
| O3 | Wire counters/gauges/histograms into `store`, `queue`, `runner`, `telegram`, `control` | ⬜ |
| O4 | OTel scaffold: `Init`, no-op tracer when env unset, exporter selection, redaction wrapper | ⬜ |
| O5 | `runner.run` root span tree + propagation env vars on `docker exec` + MCP spawn | ⬜ |
| O6 | Smoke tests: hit a local Prom, hit a local OTel collector, verify cardinality | ⬜ |

---

## Phase 2 — Integrations (I1–I9)

After M0–M9 the core works. These add NanoClaw-style features one by one.
See [docs/INTEGRATIONS.md](docs/INTEGRATIONS.md) for full triage.

| ID | Integration | Tier | Status |
|----|-------------|------|--------|
| I1 | Reactions (👀👍👎) + typing indicator + reply threading | 1 | ✅ |
| I2 | Image/document baseline: download and scoped path handoff; universal extraction/OCR is CP-014 queue 2 | 1 | ✅ |
| I3 | Voice transcription (Whisper) + TTS replies + `/voice` mode | 1 | ✅ |
| I4 | Qualified PDF extraction — tracked within CP-014 queue 2, not a duplicate project | 1 | ⬜ |
| I5 | Markdown → Telegram HTML formatting + smart chunking | 1 | ✅ |
| I6 | Karpathy LLM Wiki convention | 1 | ⬜ |
| I7 | Bot-to-bot feasibility researched; implementation/agent swarm deferred to queue 6 | 1 | ⬜ |
| I8 | Ollama tool | 2 | ⬜ |
| I9 | Gmail tool mode | 2 | ⬜ |
| I10 | X tool, Parallel tool — as needed | 2 | ⬜ |

---

## Phase 3 — Skill ecosystem (S1–S?)

See [docs/SKILLS.md](docs/SKILLS.md). Originally dropped, now redesigned for Go.
Tracked as its own phase because the milestones are orthogonal to core work.

| ID | Step | Status |
|----|------|--------|
| S1 | Container-skills directory + mount into agent container | ✅ | Mechanism works: `container/skills/memory/SKILL.md` mounted. Per-chat `skills.json` filter deferred |
| S2 | MCP tool server: gopod as MCP stdio server proxying memory/tasks/control HTTP APIs → natively registered Claude tools via `--mcp-config`. Agent sees tools as first-class (no curl). Hybrid approach: HTTP API as backend, thin MCP wrapper as frontend. | ⬜ | |
| S3 | Skill manifest format + validation | ⬜ | |
| S4 | Per-chat skill enable/disable (`/skills enable/disable`) | ⬜ | |
| S5 | Dev-time Claude Code skills in `.claude/skills/` for gopod maintainers | ✅ | `.claude/skills/handoff/SKILL.md`; does not imply a runtime skill manager |

---

## Phase 4 — Multi-provider agent support

| ID | Step | Status |
|----|------|--------|
| P1 | AgentProvider interface: refactor runner to call provider methods instead of hardcoded `claude` commands | ✅ | `internal/runner/provider.go` — stateless interface with Name/Image/RunCmd/LoginCmd/etc. Runner calls provider methods everywhere |
| P2 | Claude provider | ✅ | Explicit session UUID/resume, fresh system turns, auth/model/stream handling in provider_claude.go |
| P3 | Codex provider | ✅ | Separate image/auth and explicit session ids; dangerous-bypass + skip-git flags, fresh --ephemeral system turns. Historical real login worked; upgraded0.164 native resume is A02 |
| P4 | Per-chat provider config and continuity | ✅ | D030/D031: choice persisted, switching waits and preserves sessions/history, resets incompatible model; confirmed quota retry. Per-topic choice is future CP-018 |
| P5 | Gemini CLI provider: `gemini -p` with Google OAuth (browser link auth like Claude), `--resume latest` for sessions, `~/.gemini/` persisted via bind mount | ⬜ |
| P6 | Goose provider: `goose run -t` — model-agnostic (15+ providers via env), MCP extensible, Rust binary, named sessions | ⬜ |
| P7 | Cline CLI provider: `cline -y` — multi-provider (Anthropic/OpenAI/Google/Bedrock/Azure), gRPC API, standalone since 2.0 | ⬜ |
| P8 | Aider provider: `aider --message --yes` — code editing specialist, 20+ models, official Docker image | ⬜ |
| P9 | API providers (Tier 2): OpenAI API / Ollama / any OpenAI-compatible — same interface, no container, HTTP calls | ⬜ |

## Phase 7 — Streaming output

Real-time agent output to Telegram. Replaces buffered wait-then-wall-of-text.
See [docs/STREAMING.md](docs/STREAMING.md) for full design.

| ID | Step | Status | Notes |
|----|------|--------|-------|
| ST1 | `Docker.ExecStream` — streaming exec with `io.Pipe` + `stdcopy` demux, `StreamHandle` struct | ✅ | `internal/runner/docker.go` |
| ST2 | `Runner.RunStream` — streaming run with `AgentStream` (chunks channel + result channel) | ✅ | `internal/runner/runner.go` |
| ST3 | Telegram helpers — `sendPlaceholder`, `editMessage`, `editMessageHTML` | ✅ | `internal/telegram/streaming.go` |
| ST4 | `runAgentStreaming` — streaming handler loop with 1s ticker, overflow, fallback | ✅ | `internal/telegram/streaming.go` |
| ST5 | Wire into queue handler, config flag (`GOPOD_STREAM_DISABLED`), `maybeVoiceReply` extracted | ✅ | `cmd/gopod/main.go`, `internal/config/config.go` |

**Build order:** ST1 ∥ ST3 → ST2 → ST4 → ST5

ST1 and ST3 are independent and can be built in parallel.

---

## Phase 6 — Secret gateway ([D019](docs/DECISIONS.md))

MITM HTTP proxy + HashiCorp Vault. Agents never see real keys.
See [docs/GATEWAY.md](docs/GATEWAY.md) for full design.

| ID | Step | Status | Notes |
|----|------|--------|-------|
| G1 | `internal/vault` — Vault client: KV v2 read/write, AppRole auth, in-memory cache with TTL, connection health check | ⬜ | Pure Go, no CGO. Uses `hashicorp/vault/api` SDK. Path convention: `secret/data/gopod/{shared,<chat>}/<name>` |
| G2 | `internal/gateway/ca` — Certificate authority: self-signed root CA generation + per-host leaf certs (ECDSA P-256), cert cache (24h TTL), root CA persisted to `${DATA_DIR}/gateway/` | ⬜ | `crypto/x509` + `crypto/ecdsa`, no CGO. CA cert mounted into containers for trust |
| G3 | `internal/gateway` scaffold — HTTP CONNECT proxy: TCP listener, Proxy-Authorization Basic extraction, agent_token lookup, plain TCP tunnel for unknown hosts. No MITM yet | ⬜ | Depends on G1 for token→chat resolution. Test: proxy curl through it, verify tunnel works |
| G4 | MITM TLS interception + header injection — terminate agent TLS with leaf cert from G2, parse HTTP request, apply injection rules (SetHeader/ReplaceHeader by host+path pattern), forward to upstream over fresh TLS, stream response back | ⬜ | Depends on G2+G3. Core of the gateway. Test: inject a fake header, verify upstream receives it |
| G5 | Container wiring — `agent_token` column on `registered_chats`, `ProxyEnvVars()` on AgentProvider, CA cert bind mount, `update-ca-certificates` / `NODE_EXTRA_CA_CERTS` in Dockerfile, remove secret env vars when gateway active | ⬜ | Depends on G4. Modifies `docker_args.go`, `mounts.go`, `container/personal/Dockerfile` |
| G6 | `internal/gateway/policy` — scoped secret resolution: per-chat overrides shadow shared secrets, resolution cache (60s TTL), rate limiting (optional token bucket per agent+host), block rules | ⬜ | Depends on G4. Test: per-chat override shadows shared, unknown host tunnels |
| G7 | Control plane — `/secrets list\|add\|remove\|rotate-token\|test\|status` commands via Router, all OwnerOnly. `/secrets add` prompts for value interactively (like /login flow) | ⬜ | Depends on G5+G6. Telegram-mediated secret management |
| G8 | SQLite fallback backends — Vault Transit encryption (Vault provides encrypt/decrypt, SQLite stores ciphertext), local AES-256-GCM fallback (dev only, key from `GOPOD_SECRET_KEY`). `secrets` table in schema | ⬜ | Depends on G6. For deployments where full Vault KV is overkill |

**Build order:** G1 ∥ G2 → G3 → G4 → G5 ∥ G6 → G7 → G8

G1 and G2 are independent and can be built in parallel.
G5 and G6 are independent once G4 lands.
G8 is optional — full Vault mode (G1–G7) is the primary path.

---

## Phase 8 — Telegram chat-UI parity ([D021](docs/DECISIONS.md), [D022](docs/DECISIONS.md))

Spec: [docs/superpowers/specs/2026-10-24-telegram-threads-and-drafts-design.md](docs/superpowers/specs/2026-10-24-telegram-threads-and-drafts-design.md). Implementation is merged and deployed. Detailed real-client acceptance is tracked separately in [ACCEPTANCE.md](docs/ACCEPTANCE.md).

| # | Item | Status | Notes |
|---|---|---|---|
| T1 | Bot API 10.3 library (`go-telegram/bot` v1.27.0) | ✅ | |
| T2 | Native streaming via `sendMessageDraft` (private chats, sticky edit fallback) | ✅ | D021 |
| T3 | Stop button (`can_stop`) ending turns `stopped`; runner kills the agent | ✅ | D021 |
| T4 | Conversations per private-chat thread: sessions, summaries, queue, sends, commands | ✅ | D022; needs topics ("Threaded mode") enabled in @BotFather |
| T5 | Parallel execution of threads | ⏸️ | Needs research: two agents in one container (`$HOME`) unverified; sequential per chat today |
| T5b | One container per thread (alternative route to parallel threads) | ⏸️ | Needs research. Idea: each `(chat, thread)` gets its own agent container, workspace and memory mounts shared. Real parallelism and no shared `$HOME` between sessions, at the cost of RAM and container lifecycle (idle kill, cleanup, per-thread `/provider` question). Compare with T5 (two agents in one container) once the concurrency spike has run. Owner raised it 2026-10-25; chosen fallback today is sequential per chat |
| T8 | Agent admission control | ✅ | Global gate, memory-aware admission and wait UI implemented. Native CLI NODE_OPTIONS acceptance is not a qualified heap/RAM cap; live A10/A16 |
| T9 | Enable the memory cgroup on the Pi so the per-container RAM limit is enforced (`cgroup_enable=memory cgroup_memory=1` in the boot cmdline + host reboot), then set the agent cap to ~1 GB | ⏸️ | **Backlog, not implemented** (owner, 2026-10-25). Today the 4 GiB limit in code is ignored on the deploy host (`docker inspect` shows `mem=0`) |
| T6 | Inline keyboards: reply actions, pickers and confirmations | ✅ | Implemented/deployed; live A08/A09/A11 pending. Copy has API/client limits |
| T7 | Rich messages (`sendRichMessage`: tables, thinking/details blocks) | ⬜ | Same spec, F13; plan phase 7, behind `GOPOD_RICH_REPLIES`; verify in private chats first |
| T10 | Callback layer, Stop button on the edit path, humane errors with Retry | ✅ | Implemented, merged and deployed; live acceptance A08 remains pending (see docs/ACCEPTANCE.md) |
| T11 | Reply actions under the newest reply: Regenerate, Copy, Steps, As file | ✅ | Implemented, merged and deployed; live acceptance A09 remains pending (see docs/ACCEPTANCE.md) |
| T12 | Visible state: status reactions, elapsed time and queue position in the draft | ✅ | Implemented, merged and deployed; live acceptance A10 remains pending (see docs/ACCEPTANCE.md) |
| T13 | Command buttons: model/provider pickers, confirmations, paged outputs | ✅ | Implemented, merged and deployed; live acceptance A11 remains pending (see docs/ACCEPTANCE.md) |
| T14 | Commands menu per scope and one strings file | ⬜ | Plan phase 5; owner Q4 |
| T15 | Input context: replied/quoted/forwarded text, albums, audio, video notes | ✅ | Implemented, merged and deployed; live acceptance A07 remains pending (see docs/ACCEPTANCE.md) |
| T16 | Optional task-result buttons and topic titles | ⬜ | Task management buttons shipped separately (CP-015 follow-up); result actions/topic titles remain open |

## Post-milestone work

| ID | Step | Status | Notes |
|----|------|--------|-------|
| DT | Durable turns — every agent run persisted in `turns`, recovered at boot | ✅ | [D020](docs/DECISIONS.md), spec `docs/superpowers/specs/2026-10-03-durable-turns-design.md`; manual `kill -9` test pending |

---

## Shipped proposal implementation

[The CP registry](docs/proposals/README.md) is the status authority. Acceptance
checks do not reopen an implemented proposal; use docs/ACCEPTANCE.md instead.

| CP | Shipped behavior | Verification boundary |
|---|---|---|
| CP-001 | Per-exec memory capability, chat/thread scope and revocation | Unit/integration scope tests; live unauth401. Authenticated real skill A14 remains open |
| CP-008 | Per-chat model selection/persistence and provider model flags | Tests and deployed command/picker; live model/restart/use A11 remains open |
| CP-009 | Bounded provider continuity preserving native sessions | Deployed; real roundtrip A04 pending |
| CP-010 | Explicit confirmed quota fallback | Deployed; quota UI/retry A05 pending |
| CP-013 | Speech transcript source/duration provenance | Deployed; real voice A07 pending |
| CP-015 | Agent scheduling guide/management/receipts; canonical-JID message delivery; paged task controls | Real receipts/API delivery proved; user task execution and client UI A01/A06 pending |
| D055 (no CP; owner request 2026-11-01) | `/silent` per-chat progress-only streaming: on/off picker, draft shows header and tool steps only, answer once at the end | Unit and race tests, Sonar; merged `6d11765`, deployed in `be7575f`; real Telegram acceptance pending |

Pending proposals and design-only CP-011/012/014/018–019 are assigned to the
approved queue above. CP-016 is in progress; a saved spec alone is not an active
implementation.

---

## Deferred

| Topic | Why deferred | Revisit when |
|-------|--------------|--------------|
| Honcho identity reasoning | External service, billing, network dep | Local memory layer (Layer 3) proves insufficient in practice |
| Multi-channel | Out of scope by design | Never (would be a different project) |
| macOS status bar | Not wanted by user | — |

---

## How to update this file

1. Change a status emoji.
2. If you changed scope, add a row or strike through and link the ADR.
3. Mention it in the next [HANDOFF.md](docs/HANDOFF.md) entry.

## Accepted design follow-ups (2026-10-25)

| Proposal | Status | Implementation scope |
|---|---|---|
| CP-016 topic deletion | 🟦 | Store/outbox/ingress and native/queue/transport fences through #167 deployed; command, durable legacy ownership/reconciliation and destructive acceptance remain open |
| CP-017 Telegram diagnostics | ✅ | Separate Telegram settings, subscriptions, code support and contextual rights; owner doctor and scoped help; [execution plan](docs/superpowers/plans/2026-10-25-telegram-capability-diagnostics.md) implemented for private DMs; group reports/notices deferred |
| CP-018 per-thread providers | ⬜ | Chat default plus topic overrides, provider-specific models and scoped confirmed quota fallback |
| CP-019 ephemeral command/service/error replies | ⬜ | Public conversational messages unchanged; private delivery never falls back to public |

Specifications are accepted for planning only; no associated mode or routing
change is enabled by the documents. CP-015 follow-up: the task control panel
adds bounded task pages and explicit Pause/Resume actions; UTF-8 truncation fixes
previously silent Cyrillic task-list replies.

## CP-020 — shared-space isolation security follow-up

[Audit](docs/research/2026-10-25-personal-group-isolation-audit.md),
[contract](docs/superpowers/specs/2026-10-25-personal-group-container-isolation.md)
and [implementation/acceptance plan](docs/superpowers/plans/2026-10-25-personal-group-container-isolation.md)
track the work. **Implementation deployed; final provider/client qualification remains open.**
The architecture merged in #66/#67; follow-ups #88/#89 fixed Codex tree-budget admission and
admit registered groups by default. #93/#95/#97 implement triggers, owner-in-group controls
and private group-login guidance. The current controller is `e3b5401`; security groups 1–8 are deployed.
Historical 2026-10-29 evidence records Claude group new/resume success after owner login;
final Codex two-turn/tool and cross-boundary acceptance remain open. Earlier TCP/offline-tool
checks are in the [2026-10-27 report](docs/deployments/2026-10-27-group-isolation.md),
current rollout in the [2026-10-31 report](docs/deployments/2026-10-31-security-wave.md).

Implemented: immutable namespace epochs and retired keys, protected separate
roots, two personal/group provider image sets, read-only native-subscription auth,
credential-free offline commands, exact inference-only HTTPS egress, scoped local
memory/IPC, owner controls and evidence-preserving drain/orphan recovery.
Native policy v2 passed 69 local cases over all 11 public catalog models; ARM64
release images were built locally from exact merged main. The production catalog
was installed; later default admission prepared the existing group (historical checkpoint in HANDOFF archive).
No personal service or paid API fallback is granted to groups. Group speech and
automatic maintenance remain unavailable; private behavior stays the baseline.

Current groups are owner-controlled sandboxes; targeted checks run incrementally.
Full cross-boundary acceptance precedes real-group admission. T9 host cgroup/reboot,
CP-006 group maintenance, arbitrary integrations and one-way private-to-group data
grants remain separate work. See [current policy/configuration](docs/ISOLATION.md).

### Rich agent messages P1 (2026-10-29)
Personal durable IPC formatting implemented on `feat/rich-messages-formatting`: Markdown
by default, sanitized HTML, literal plain text, admission validation and a recorded one-shot
plain fallback on explicit parse refusal. Existing outbox flags gate activation; legacy
messages remain literal. Merged as `3fcbf06` (PR #83), CI green; deployed in `8bdff46` with two acknowledged durable IPC smoke messages. Client rendering acceptance remains pending.
See [requirements](docs/superpowers/specs/2026-10-29-rich-agent-messages.md) and D045.
Buttons/hooks, inbound/outbound stickers and thread awareness remain separate work.

### Owner restart (2026-10-29)

- CP-031: `/restart` merged in PR #84 as `8bdff46`, CI green and deployed; owner/private-only; verified Docker supervision, durable request/replay fence and truthful container-readiness notices. [Specification](docs/superpowers/specs/2026-10-29-telegram-restart.md). Production restart acceptance remains separate.

### Reaction reading specification (2026-10-29)

- CP-032: [design](docs/superpowers/specs/2026-10-29-telegram-reaction-reading.md) saved for incoming emoji/custom reactions, scoped message correlation and no-admin capability handling. Private native reaction delivery is unavailable under the documented Bot API admin requirement. No implementation, activation or queue reprioritization.

## Owner TODO — container healthchecks (2026-11-03)

- Add appropriate Docker healthchecks/readiness checks for every gopod-owned
  container: controller, Claude/Codex personal and group runtimes, elevated
  runtimes and auxiliary images. Long-running containers must expose meaningful
  startup/liveness status; define a suitable readiness/exit check for one-shot
  tools. Checks must not trigger provider inference, credential refresh or writes.
  Include the health matrix and before/after-reboot verification in the runbook.
  Requested after observing that gopod/controller and owner agent had no healthcheck.
  Implemented in PR #165 (`067c016`): controller/relay readiness and native
  offline prerequisites, with explicit one-shot exceptions. Local full/race
  checks, disposable Docker matrix and required CI passed. Production rollout
  completed in `a25f22a`; controller and owner Codex runtime are healthy. Optional
  runtime acceptance and separately authorized reboot qualification remain.
  [Health matrix and runbook](docs/CONTAINER-HEALTH.md).
