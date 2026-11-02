# CP-040 — Agent attachments: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make "attach this file to my reply" a discoverable agent action on top of the CP-003 artifact pipeline: an agent-facing contract every owner turn sees, an `as` presentation field, honest named refusals with a user-visible notice, and documents sent with their original filename.

**Architecture:** No new IPC format and no host pipeline change. `internal/ipc` gains the `as` field (parsed in `artifact_request.go`, resolved to the existing store photo marker), three codes, a refusal notice (`attachments.go`) sent through the existing `MessageSink` to the folder's own chat, and a watcher branch that refuses a V1 file request which cannot reach the artifact route. `internal/runner` gains `AttachmentsPolicy` (owner turns) and the `chattmpl` owner guide `ATTACHMENTS.md`; `container/skills/attachments` mirrors it. The Telegram transport already sends documents with the opener's filename and photos with document fallback; it is covered by a new test only.

**Tech Stack:** Go 1.27, stdlib `testing`, existing `internal/ipc` fixtures (`newArtFixture`, `durableFixture`), `internal/telegram` `fakeAPI`.

**Spec:** [2026-11-02-agent-attachments](../specs/2026-11-02-agent-attachments.md); decisions [D058](../../DECISIONS.md).

## Global Constraints

- Do not weaken any D039 path, authorization or quota rule; `path_outside_workspace` only splits the existing `path_denied` for paths outside `/workspace/chat`.
- `GOPOD_OUTBOX_ARTIFACTS` semantics unchanged (D057: on unless `0`).
- One IPC format: the strict V1 request; unknown fields and unknown `as` values are `invalid_request`.
- The notice destination comes from the folder registration (path authorization), never from the request; the notice never carries request text or raw errors.
- No change to `container/Dockerfile*` (a test reads them byte-for-byte); DB access only through `internal/store`; Go only.
- Commit messages `<area>: <imperative summary>`; no attribution lines.

## Review Focus

1. A V1 file request in a folder without the artifact route must not send its text without the files — `TestAttachmentDisabledRefusesWithReceiptAndNotice`.
2. A group folder's file request must be refused with a notice to that group's own chat, never reach the artifact route — `TestArtifacts_GroupAndOwnerAuthorityDenied`.
3. `as:"photo"` on a PDF is refused, `as:"document"` on a PNG stays a document, omitted `as` on a PNG is a photo hint — `TestParseArtifactRequestAsField`, `TestArtifactPartsMapRequestPresentation`.
4. Notice paths cannot inject control/bidi characters or unbounded text — `TestAttachmentNoticeSanitizesAndBoundsPaths`.
5. The policy reaches owner turns of both providers and no other tier — `TestAttachmentsPolicyIsOwnerTurnsOnly`.

---

### Task 1: `as` field and new codes in the V1 parser

**Files:** Modify `internal/ipc/artifact_request.go`, `internal/ipc/capabilities.go`, `internal/ipc/artifacts.go`. Test `internal/ipc/artifact_presentation_test.go`, `internal/ipc/artifacts_test.go`.

- [x] Write `TestParseArtifactRequestAsField` (auto/document/photo accepted; photo on PDF/GIF `unsupported_type`; `video`, `Document`, `as`+`presentation`, unknown sibling field `invalid_request`; files without capability `artifacts_disabled`; text without capability `invalid_request`). Run: fails to compile (`As` missing).
- [x] Extend `TestArtifactPartsMapRequestPresentation` with the auto/document/photo rows; flip the two D052 rows that assumed "empty means document" to the `as`-omitted semantics. Run: fails.
- [x] Add `As` to `artifactFile`, the codes `artifacts_disabled`, `path_outside_workspace`, `unsupported_type`, `photoCandidate` and `uploadPresentation`; validate in `parseArtifactRequest`; use `uploadPresentation` in `artifactParts`.
- [x] Add `ErrPathOutsideWorkspace` (wraps `ErrPathDenied`) to `Grant.MapPath` for the clean/prefix check; map it to `path_outside_workspace` in `prepareArtifacts`. Update the path-attack table rows for outside paths.
- [x] Run `go test ./internal/ipc/`: pass.

### Task 2: Refusal notice and disabled-route refusal

**Files:** Create `internal/ipc/attachments.go`, `internal/ipc/attachments_test.go`. Modify `internal/ipc/messages.go`, `internal/ipc/durable.go`, `internal/ipc/artifacts.go`.

- [x] Write `TestAttachmentDisabledRefusesWithReceiptAndNotice` (durable watcher without artifacts: receipt `artifacts_disabled` with attachment help, one notice to `tg:1` with the path, request text not sent, no delivery), `TestAttachmentRouteRefusalsNotifyTheUser` (outside path, too large, no capability, forced photo), `TestAttachmentAcceptedSendsNoNotice`, `TestAttachmentNoticeSanitizesAndBoundsPaths`. Run: fail.
- [x] Implement `refuseArtifact` (rejectDurable + notice on first terminal refusal), `v1FilePaths`, `refuseDisabled`, `noticeAttachmentRefused`, `attachmentNotice`, `displayPath`; route every artifact-route rejection (including `artifactFailure`, `retryOrReject`) through `refuseArtifact`; add the `messages.go` branch after the artifact route; set `attachmentHelp` in `finishRejected` for file codes.
- [x] Update `TestArtifacts_GroupAndOwnerAuthorityDenied`: group receipt `artifacts_disabled`, exactly one notice to `tg:-2`.
- [x] Run `go test ./internal/ipc/`: pass.

### Task 3: Transport coverage for documents

**Files:** Create `internal/telegram/outbox_attachment_test.go`.

- [x] Write `TestAttachmentDocumentKeepsOriginalFilenameAndCaption`: a document part and a photo-marked part with PDF bytes both go through `sendDocument` with the opener's filename, caption, chat and thread, same bytes.
- [x] Run `go test ./internal/telegram/ -run TestAttachmentDocument`: pass without production change (the transport already behaves; the test pins it).

### Task 4: Agent-facing contract

**Files:** Create `internal/runner/chattmpl/ATTACHMENTS.md.tmpl`, `container/skills/attachments/SKILL.md`, `internal/runner/attachments_skill_test.go`. Modify `internal/runner/chattmpl/chattmpl.go`, `CLAUDE.md.tmpl`, `RESTRICTED.md.tmpl`, `internal/runner/policy.go`, tests `chattmpl_test.go`, `policy_test.go`.

- [x] Write `TestAttachmentsGuideIsOwnerOnlyAndStatesTheContract`, `TestAttachmentsPolicyIsOwnerTurnsOnly`, `TestAttachmentsContainerSkillStatesTheContract`; update the seeded-file counts. Run: fail.
- [x] Add the guide template and seed it for the owner after `INTEGRATIONS.md`; add the owner "Sending files" section and the restricted "Files" section; add `AttachmentsPolicy` to `systemPromptWith` for owner turns; add the skill with frontmatter.
- [x] Run `go test ./internal/runner/...`: pass.

### Task 5: Docs

**Files:** `docs/DECISIONS.md` (D058), `docs/proposals/CP-040-agent-attachments.md`, `docs/proposals/README.md`, `docs/SKILLS.md`, `docs/CONTROL.md`, `docs/ACCEPTANCE.md` (A22–A25), `docs/HANDOFF.md`.

- [x] D058: `as` with auto inference (supersedes the D052 no-inference sentence for `as` only), the three codes, the disabled-route refusal and the notice.
- [x] CP-040 status `in-progress` with spec and plan links; registry row updated.
- [x] SKILLS.md and CONTROL.md mention the attachments skill and that attachments are an IPC contract, not a command.
- [x] ACCEPTANCE rows for the real-client checks, pending.
- [x] HANDOFF "Resume here" sentence.

### Task 6: Verification and PR

- [x] `gofmt -l` on touched Go files: empty.
- [x] `go build ./... && go vet ./... && go test ./...`.
- [x] `go test -race ./internal/ipc ./internal/telegram ./internal/runner`.
- [ ] Push `feat/cp040-agent-attachments`, open one PR, watch CI to green. Do not merge or deploy (owner/separate session).
