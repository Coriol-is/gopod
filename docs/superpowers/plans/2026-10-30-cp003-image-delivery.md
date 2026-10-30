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
  bytes once (bounded by the opener's per-file bound, `store.DefaultMaxArtifactFileBytes` = 20 MiB by
  default), then
  - sends a **document** (no photo call) if the bytes are not PNG/JPEG, exceed 10 MB, have a side over
    10000, width + height over 10000 (each side is bounded before summing, so it cannot overflow on any
    GOARCH; the Pi is arm64, 64-bit, and a 32-bit build is a separate gate below), a side ratio over 20
    (symmetric, so EXIF rotation is irrelevant), an unreadable header, a CMYK JPEG or an animated PNG
    (`acTL` before `IDAT`);
  - otherwise calls `sendPhoto` with the same buffer;
  - falls back to **one** `sendDocument` of the same buffer only when Telegram answers 400 and the
    description, parsed as `Bad Request: <TOKEN>` from go-telegram v1.27.0's `"%w, %s"` error, has a TOKEN
    that is **equal** to `PHOTO_INVALID_DIMENSIONS` or `IMAGE_PROCESS_FAILED`. If the attempt context has
    ended by then, no document is sent: the part is released as `not_sent` with the distinct code
    `photo_refused_canceled`, which the dispatcher does not refund (unlike `canceled_before_dispatch`), so the
    attempt is counted and the retry budget of five bounds repeated photo sends (dispatcher-level test).
  - **Assumption, not evidence:** that such a 400 means Telegram stored nothing, and that these two strings
    are what Telegram sends, are assumed from memory. Nothing in the repository or in a cited Bot API page
    supports them. `PHOTO_SAVE_FILE_INVALID` and `PHOTO_EXT_INVALID` were considered and dropped for the same
    reason. Verify in the live smoke; until then treat the fallback as best effort.
  - **Decision for unlisted 400s:** refused, no document. A 400 we cannot name may concern the
    destination, the caption or the thread, where a document would fail or misroute the same way, and a
    wrong guess here costs a duplicate upload while a missed guess costs one missing photo that the
    agent sees as a rejected receipt and can re-request as a `document`. Growing the list is a one-line
    change once the smoke shows real strings.
- Not changed: timeout, 5xx, malformed reply, transport error, 429, 403, thread-gone and any other 400 are
  classified exactly as for a document and never produce a second upload. A failure of the fallback
  document is classified on its own and not repeated. The uncertain-outcome handling and "no replay" rule
  of the dispatcher are untouched: **one DB attempt, at most two API calls** (a document only after a proven
  photo refusal; the second call is logged at info with the part and attempt id). A success reply without a
  usable message id stays `malformed_reply` (ambiguous) and is never followed by a document.
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

### PR C. Runner-side per-run capability issuer (done: where the code differs from this text, the code won)

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

**As built (PR C).** Differences from the design above, found by reading the code:
- The issuer lives in `internal/ipc` (`RunIssuer`, `artifact_issuer.go`), not in `cmd/gopod`; `cmd/gopod.artifactIssuer`
  only builds it, and returns nil (nothing issued) unless the *effective* `artifactIPC(...)` config is non-empty, so the
  flag, a running dispatcher, the durable route and a private owner chat must all hold. It shares the watcher's
  `*Capabilities`.
- The runner interface is `IssueArtifact(folder, thread) (token, end)` with no context; the issuer bounds its own
  snapshot with a 5 s timeout. Env name `GOPOD_ARTIFACT_CAP` (`runner.ArtifactCapabilityEnv`).
- A system run (`RunFresh`, thread 0) is not distinguishable from the main thread by `thread`, so the exec chain gained
  an `interactive` flag: `Run` and `RunStream` pass true, `RunFresh` false. Group providers return before any env is built.
- TTL: no per-turn timeout exists in the queue, so the grant TTL is `MaxGrantTTL - 1 min` as a leak backstop; the real
  revocation is the exec end (`EndRun`, with the registry's two-minute grace for a request written before the end).
  Each exec has its own `RunRef`, so ending one exec does not revoke a concurrent one.
- Unknown (a) is therefore answered by revocation at exec end, not by a TTL. Unknown (b): any thread of the owner chat
  gets a capability bound to that thread. Unknown (c), decided by the owner: **keep**. `runTaskSilently` calls
  `Runner.Run`, so scheduled silent turns in the owner chat get a capability like foreground turns; there is no
  restriction inside the owner scope, and the capability can only deliver owner-workspace files to the owner's own chat.
  A test pins this. Only `RunFresh` system runs and group providers are excluded.
- The request file's mtime is agent-controlled, so it is not passed to `ResolveAt` as is: the watcher uses
  `max(mtime, start of the previous sweep)`, fixed at first observation (`writtenBound`). A file that appears after a
  run ended is refused even with a backdated mtime, unless no sweep started between the run end and the file's
  appearance (at most one poll interval of slack, needed so a last-action write-then-exit is not lost). On the very
  first sweep of a process the mtime is used (capabilities do not survive a restart).
- `SetArtifactIssuer` is atomic (the wiring runs after the queue workers start), and issuance is bounded by a timeout:
  a stalled lookup yields no capability and a late result is revoked.

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

1. **CP-007 matched backup: NOT satisfied, not verified.** `GOPOD_OUTBOX_ARTIFACTS` must not be set until a
   matched DB plus artifact backup, lease and restore pass (D039). HANDOFF ("CP-007 PR 1") lists restore and
   activation, the Pi drill and capture of some trees as not done. I have no evidence that the current
   backup covers `<data>/artifacts` with the lease; link the run output before calling this gate passed.
2. **Restore drill on the Pi** with a published image artifact present (owner-run).
3. **Pi flag change**: `GOPOD_OUTBOX_DISPATCHER=1`, `GOPOD_OUTBOX_IPC=1` (already on for the personal chat) and
   then `GOPOD_OUTBOX_ARTIFACTS=1`. Owner only; this plan does not do it.
4. **Bounded live smoke** (also the only place to confirm the two refusal tokens and the "400 stored
   nothing" assumption: send an image that Telegram rejects as a photo and record the exact description) on the real bot in the owner's private chat: one small PNG as photo, one large or
   odd-shaped image proving the document fallback, one request retried to prove convergence on a single
   delivery. Needs the owner at the keyboard; uses a real Telegram call, so it is a separate authorized step.
5. **Architecture check**: the Pi is arm64 (64-bit `int`). Run the transport tests under `GOARCH=386` (or
   `arm`) if any 32-bit target is ever built; the dimension and chunk-length checks are written to avoid
   overflow but only a 64-bit run is exercised in CI.
6. **Caption-limit fix before PR B** (see "Known defect: caption limits" below). A 1024-emoji caption is
   accepted by our validators and refused by Telegram, so a photo or document request can be dropped.
7. Review that turning the flag on also activates PR 5 behavior (prepared code documents and prepared
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

## Known defect: caption limits (prerequisite of PR B, not changed here)

Telegram counts a caption as at most 1024 characters **after entity parsing, in UTF-16 units**. Our checks
differ: `internal/ipc/artifact_request.go` caps at 1024 **runes** (`maxCaptionRunes`) and
`store.MaxUploadCaptionBytes` is 4096 **bytes**. A caption of 1024 emoji is 1024 runes, 4096 bytes and 2048
UTF-16 units: it passes both validators and Telegram refuses the upload (a 400 that is not a photo token, so
under the decision above it is refused with no document), and the file is dropped. The document path has the
same defect today. The transport passes the caption through untouched (tested).
Fix before PR B, in the validators, not the transport: count UTF-16 units (`utf16.RuneLen` sum) against 1024
in `parseArtifactRequest`, and make the store bound consistent. Test sketch: table of captions (1024 ASCII
accepted; 512 emoji accepted; 513 emoji rejected as `invalid_request`; 1024 emoji rejected; a lone
surrogate-range rune counted as 1 or 2 consistently) run through `parseArtifactRequest`, plus a store
publish with a 4097-byte caption rejected.

## Honest unknowns

- No live Telegram call has been made for photos; all behavior here is fake-API tested, not a
  Telegram qualification. The two refusal strings and the meaning of that 400 are **assumed from memory**
  (see PR A) and matched exactly, case-sensitively; an unlisted description means no fallback (refused,
  no second upload). The 10 MB / 10000 / ratio-20 limits are likewise from memory of the Bot API.
- Photo recompression quality for charts is a product judgement, not measured.
- Pi memory use per upload: `ReadVerified` returns one in-memory buffer (up to the 20 MiB per-file bound),
  the opener wraps it in a reader, and the photo path copies it once more (`io.ReadAll`), so about 2x the
  file (up to ~40 MiB) is live, plus whatever go-telegram allocates to build the multipart body (not
  inspected). One upload runs at a time. Not measured on the Pi.
- Verified by reading the code: `artifactfs.ReadVerified` opens the blob through the no-follow descriptor
  walk, reads at most size+1 bytes into memory, checks size and SHA-256 on that buffer and returns it; the
  upload sends those bytes, so there is no pathname reopen and no time-of-check/time-of-use window between
  verification and upload.
