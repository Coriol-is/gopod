# CP-003 image delivery: what is missing end to end

Date: 2026-10-30. Status: **plan; nothing here enables anything**. Baseline: `main` at `8bc6b5d` plus
the transport slice in this PR.
Sources: [CP-003](../../proposals/CP-003-typed-replies-via-ipc.md),
[immutable artifact delivery](../specs/2026-10-26-immutable-artifact-delivery.md),
[delivery plan](2026-10-26-reliability-delivery.md), D039 and D041 in
[DECISIONS](../../DECISIONS.md), "CP-003 PR 4/5" in [HANDOFF](../../HANDOFF.md).

## Goal and the path today

Goal: the agent in the owner's container writes a PNG (a chart) under `/workspace/chat/`, asks the host
to deliver it, and it arrives in the owner's private chat as a photo.

The designed path, with what exists:

| Step | Piece | State |
|---|---|---|
| 1 | Run starts; host issues a per-run capability bound to folder, destination, owner, output root | **missing** (registry `ipc.Capabilities.Issue` exists, nothing calls it) |
| 2 | Container learns the capability | **missing** |
| 3 | Agent knows how to ask and what to wait for | **missing** (no instructions for V1 requests) |
| 4 | Agent writes a V1 JSON request into `/workspace/ipc/messages/` | schema exists (`artifact_request.go`) |
| 5 | Watcher admits: capability, scope, path policy, snapshot into the private blob store, durable delivery, receipt | exists, behind `GOPOD_OUTBOX_ARTIFACTS` |
| 6 | Request `presentation: "photo"` becomes a part presentation | **missing** (validated, then dropped by `artifactParts`; a test pins "agent parts never carry a presentation") |
| 7 | Dispatcher uploads the verified snapshot bytes | exists |
| 8 | `sendPhoto` with Telegram's limits and a document fallback | **this PR** (transport only; no producer sets it yet) |
| 9 | Foreground reply goes through the same outbox | not done (PR 6); the direct path stays |

Every V1 request today ends in `stale_capability` (step 1), so the flag proves storage and plumbing only.

## PR sequence

Each item is one reviewable PR. "Gate" marks what only the owner can do.

### PR A (this PR). `sendPhoto` in the artifact transport

Code only, flag untouched.

- `store.UploadPresentationPhoto` ("photo"), carried in an upload part's `ParseMode` field like the voice
  marker. Unlike voice it is accepted by the generic publish path: it is a hint whose safe outcome is a
  document, so it cannot widen what an agent may do. It is part of the part hash and so of the plan identity.
- Transport (`internal/telegram/outbox_photo.go`): a photo-presented upload reads the verified snapshot
  bytes once, then
  - sends a **document** (no photo call) if the bytes are not PNG/JPEG, exceed 10 MB, have
    width + height over 10000, a side ratio over 20, or an unreadable header;
  - otherwise calls `sendPhoto` with the same buffer;
  - falls back to **one** `sendDocument` of the same buffer only when Telegram answers 400 naming the picture
    (`PHOTO_INVALID_DIMENSIONS`, `PHOTO_SAVE_FILE_INVALID`, `PHOTO_EXT_INVALID`, `IMAGE_PROCESS_FAILED`),
    which proves nothing was stored.
- Not changed: timeout, 5xx, malformed reply, transport error, 429, 403, thread-gone and any other 400 are
  classified exactly as for a document and never produce a second upload. A failure of the fallback
  document is classified on its own and not repeated. The uncertain-outcome handling and "no replay" rule
  of the dispatcher are untouched; the dispatcher still counts one attempt.
- Original quality: the document is the form that keeps the bytes. Presentation `document` (the default)
  never calls `sendPhoto`. Telegram recompresses and may downscale photos, which matters for dense charts.
- Limits are from the Bot API as understood at writing (10 MB, 10000, ratio 20). **Unknown:** whether the
  live API currently enforces exactly these for every client; the fallback on explicit refusal is the safety net.
  WebP and GIF are deliberately sent as documents (stdlib decoders only, no new dependency).

### PR B. Request presentation reaches the part

Small. `artifactParts` maps `presentation: "photo"` to `UploadPresentationPhoto`; "document" and empty stay
empty. Replace `TestAgentUploadPartsNeverCarryAPresentation` with: photo maps to the photo marker, voice
still unreachable, an agent MIME claim never selects a presentation. Update D039 ("uploads are always
documents") with a superseding line in a new ADR. Decide first whether the photo hint is applied to a file
whose MIME is not an image (recommended: yes, the transport already falls back to a document).
Open design choice for the owner: should the host default an image MIME to photo when presentation is empty?
Recommendation: no; keep document as the default and let the instructions (PR D) say when to ask for a photo.

### PR C. Runner-side per-run capability issuer

The only large unknown-free-but-wide slice, so not attempted here. Existing pieces and what is missing:

- `ipc.Capabilities.Issue(Grant)` already validates folder, destination, output root (only
  `<ChatsDir>/<folder>`, protected trees denied), owner and a TTL of at most 6 hours, and `EndRun(runRef)`
  revokes with a two-minute grace for a request written before the end.
- The delivery to the container follows the existing per-exec precedents: the memory capability
  (`GOPOD_MEMORY_CAPABILITY`, minted in `Runner.agentEnv`, passed in the exec env, revoked when the exec
  has fully finished) and the task report capability (`GOPOD_TASK_REPORT_CAP`, `taskexec`). Exec env is
  not part of the container configuration digest, so adding a name does not disturb the managed-task
  guard. Proposed name: `GOPOD_ARTIFACT_CAP` (no KEY, SECRET or TOKEN in the name). The value is an opaque
  random hex string held only in memory; a restart revokes everything, which is the designed fail-closed behavior.
- To build:
  1. A consumer-side interface in `internal/runner` (like `CapabilityMinter`): `IssueArtifact(ctx, folder,
     thread) (token string, end func())`, nil when the feature is off.
  2. Implementation in `cmd/gopod` over `ipc.Capabilities`: resolve the registration with
     `SnapshotConversation(jid, thread, VisibilityConversation)` for the **owner chat only** (everything else
     returns no capability; groups stay closed), `Owner = OwnerLegacyAgentIPC`, `OutputRoot = ChatsDir/<folder>`,
     `RunRef` unique per exec, TTL = turn timeout + margin, and the same `*ipc.Capabilities` instance that the
     watcher uses (today `openArtifactStore` builds it; the runner needs the same pointer).
  3. Both exec call sites that call `agentExecEnv` (`runner.go` stream at ~575 and `execAgent` at ~876) append
     the env pair and call `end` after the streams close, never when the handler returns.
  4. Tests: fake issuer proving the env is present only when enabled, only for the owner chat, revoked after
     exit, never present in a group or task exec, and no value in logs (the slog redactor already strips
     `token|key|secret|password|cookie|auth`; keep the name outside those words).
- **Unknowns:** (a) a long agent turn that outlives the TTL, handled by choosing TTL from the configured turn
  timeout, to be confirmed against `queue` timeouts; (b) whether threads other than 0 in the owner chat
  should be allowed (the grant carries the thread, so a reply goes where the turn runs; likely yes);
  (c) whether the background/scheduled silent turn (`runTaskSilently`) should get a capability at all
  (recommendation: no, it has its own report path).

### PR D. Agent-facing instructions

The request is only usable if the agent knows the schema, the env name and the receipt. Container skills mount
only for Claude (`/home/node/.claude/skills`, owner tier, read-only); Codex reads `~/.codex` and
`AGENTS.md`. The scheduler already solved this for both: an embedded guide seeded into the chat workspace
(`chattmpl` seeds `CLAUDE.md`, `AGENTS.md`, `SCHEDULING.md`, even into existing workspaces) plus a pointer in
the system prompt (`internal/runner/policy.go`) and an optional skill for discoverability.
Plan: embed `DELIVERY.md` the same way (path under `/workspace/ipc/messages/`, the request fields, "write
the file only after the PNG is fully written", wait for the receipt before claiming delivery, `document` for
original quality or large images, a photo for quick viewing, never put the capability in a file in the
workspace or in the reply text), one skill file `container/skills/delivery/SKILL.md` that points at it, and one
sentence in the system prompt only when the capability is issued (so the text is absent while the flag is off).
**Unknown:** the receipt location and exact filenames are in `internal/ipc` and should be copied from the code
into the guide with a test that the guide mentions the real constants, not remembered.

### PR E. Foreground cutover (CP-003 PR 6) and what remains of CP-003

Not needed for a first image, which travels the IPC path. Remaining from the delivery plan and D041:

- Freeze foreground text before the final emit, stop preview edits, route the personal final reply and its
  prepared attachments through `PublishVoiceReply`/the outbox, recovery that does not re-run the model,
  delivery-status messages instead of model retry on delivery error (PR 6 in the delivery plan).
- Agent-requested voice (needs PR C), operator resend of uncertain uploads, live TTS/billing qualification.
- Artifact quota/GC under real load and Pi memory/disk qualification: the defaults (20 MiB per file, 40 MiB
  per request, 512 MiB per namespace, 2 GiB global, 40 files and 200 MiB per hour) are untested on the Pi.
- Groups: closed until CP-020 and CP-019.

### Gates before any enablement (owner, not code)

1. **CP-007 matched backup**: `GOPOD_OUTBOX_ARTIFACTS` must not be set until a matched DB plus artifact
   backup, lease and restore pass (D039). HANDOFF shows backup create/verify exists, but the restore and
   activation command, the Pi restore drill, and capture of some trees are listed as not done. **Whether the
   current backup already covers `<data>/artifacts` with the lease is unverified here; check before the drill.**
2. **Restore drill on the Pi** with a published image artifact present (owner-run).
3. **Pi flag change**: `GOPOD_OUTBOX_DISPATCHER=1`, `GOPOD_OUTBOX_IPC=1` (already on for the personal chat) and
   then `GOPOD_OUTBOX_ARTIFACTS=1`. Owner only; this plan does not do it.
4. **Bounded live smoke** on the real bot in the owner's private chat: one small PNG as photo, one large or
   odd-shaped image proving the document fallback, one request retried to prove convergence on a single
   delivery. Needs the owner at the keyboard; uses a real Telegram call, so it is a separate authorized step.
5. Review that turning the flag on also activates PR 5 behavior (prepared code documents and prepared
   voice with a billed backend) because they share the flag. Accept that explicitly or split the flag.

## Rollout and rollback

Order: PR A, B, D can merge with the flag off and change nothing observable. PR C is inert while the
flag is off (no capability is minted). Steps on the Pi, each owner-run: backup + restore drill; flag on in a
quiet window; smoke; observe `class=ack` attempts and quarantine counts in diagnostics.

Rollback: unset `GOPOD_OUTBOX_ARTIFACTS` and restart. Effects of that restart, stated so nothing is a surprise:
capabilities vanish (in-flight requests become `stale_capability`), the dispatcher stays up with no opener,
so queued uploads are refused as `upload_unavailable` rather than sent, and prepared snapshots stay on disk
until the reconcile sweep or the owner removes them. Anything already accepted and sent stays in Telegram;
an upload recorded as uncertain stays uncertain and is not resent (no operator resend exists yet).
Code rollback of this PR is a revert: a stored part with the photo marker would be refused by the old
transport as `invalid_presentation`, so revert only with the flag off.

## Honest unknowns

- No live Telegram call has been made for photos; all behavior here is fake-API tested, not a
  Telegram qualification. The exact error strings of a refused photo are from Telegram's documented behavior
  and common reports, matched case-insensitively; an unlisted string simply means no fallback (refused, no
  second upload).
- Photo recompression quality for charts is a product judgement, not measured.
- Pi memory use with one 20 MiB buffer plus a 10 MiB photo copy per upload is bounded by the single-worker
  design but not measured.
