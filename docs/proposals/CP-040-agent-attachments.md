# CP-040 — Agent attachments: discoverable file delivery from the agent

| | |
|---|---|
| Status | proposed (owner request 2026-11-01) |
| Date | 2026-11-01 |
| Origin | Owner: the agent produced `banks/preduzetnik-bank-assessment.pdf` in the chat workspace and answered that it has no way to attach it ("нужна поддержка sendDocument в gopod"); a pasted path never becomes a Telegram attachment |
| Size | M |
| Touches | `internal/runner/chattmpl/` and `container/skills/` (agent-facing contract), `internal/ipc` (request validation/receipts, document vs photo intent), `internal/telegram` outbox transport (document presentation, size limits), `internal/config`/deploy (activation), `docs/SKILLS.md`, `docs/ACCEPTANCE.md` |
| Depends on | CP-003 (host artifact pipeline, PRs 4–5 merged, flag `GOPOD_OUTBOX_ARTIFACTS` off, [D039](../DECISIONS.md)); CP-002 outbox; CP-007 matched backups (artifact blobs are already covered by `matched_stopped` sets); CP-019 for group delivery targets |
| Spec | `—` until written (reuse [immutable artifact delivery](../superpowers/specs/2026-10-26-immutable-artifact-delivery.md) for the host side) |

## Problem

The host can already snapshot a file under `/workspace/chat`, hash it, publish it
through the outbox and send it with `SendDocument`/`SendPhoto`
(`internal/ipc/artifact_request.go`, `internal/telegram/outbox_transport.go`,
`outbox_photo.go`), but the feature is dark: `GOPOD_OUTBOX_ARTIFACTS` is off on
the Pi and no agent-visible text (chat templates, container skills, system prompt)
describes how to request a file send. The agent therefore writes the file, tells
the user it "has no tool", and the owner has to fetch the file by hand. CP-003
tracks the host mechanics and its activation gates; nothing tracks the
agent-facing product surface, which is what the owner actually hit.

## Proposal

Make "attach this file to my reply" a first-class, discoverable agent action and
turn the CP-003 pipeline on behind it:

1. **Agent contract.** A short, always-present section in the chat templates
   (`chattmpl`) and a container skill (`container/skills/attachments/SKILL.md`)
   that states: write the file under `/workspace/chat`, then request delivery
   with the existing IPC V1 request (`files: [{path, caption, as: "document"|"photo"|"auto"}]`),
   what limits apply (Telegram bot API 50 MB, allowed extensions/MIME, no
   symlinks, no paths outside the workspace), how the result is reported (the
   IPC receipt under `results/messages`, and the same text returned to the model
   when a request is refused), and that the reply text itself must not promise
   an attachment the host refused.
2. **Honest refusal while dark.** When artifacts are disabled or the request is
   invalid, the agent gets an explicit `.failed` reason (`artifacts_disabled`,
   `path_outside_workspace`, `too_large`, `unsupported_type`) instead of silence,
   and the user-facing reply says the file is at `<path>` and could not be
   attached, so the situation is visible rather than guessed.
3. **Documents, not only photos.** PDFs, spreadsheets, archives and text files
   go out as documents with the original filename and caption; images keep the
   photo presentation with the existing document fallback; `as` lets the agent
   force a document. Delivery stays per-chat/topic, owner-private first; group
   delivery follows CP-019's target rules.
4. **Activation.** Enable `GOPOD_OUTBOX_ARTIFACTS` on the Pi once the CP-003
   gates hold: a `matched_stopped` set that includes artifact blobs (already
   true), a scratch restore of one set with an artifact, a retention/quota check
   for the artifact store, and a rollback note. Record it as a deployment.

## Out of scope

Agent-initiated downloads from the internet, inline images in streamed text,
editing or deleting already sent files, files above the bot API limit (refused
with a reason; chunking/upload-to-external storage is a separate CP), voice
(CP-003 PR 5 owns it) and CP-014 media input.

## Risks

- The agent could attempt to attach secrets copied into the workspace; the host
  keeps rooted/no-follow path checks and protected-path denial from CP-003 and
  never trusts a model-supplied path outside the namespace.
- Enabling artifacts without retention fills the Pi disk; the activation gate
  must include the quota and a prune procedure.
- A refused attachment that the reply text still "promises" misleads the user;
  the contract and receipt loop exist to prevent exactly that.
- Group chats: a file sent to the wrong target leaks workspace content; keep
  owner-private delivery first and gate groups on CP-019.

## Acceptance

- In the owner's private chat the agent produces a PDF and, in the same turn,
  the user receives it as a Telegram document with the right filename and a
  caption; the reply text does not duplicate the path.
- An image is delivered as a photo; `as: "document"` delivers the same image as a
  file; a 60 MB file and a path outside `/workspace/chat` are refused with the
  named reasons, visible to the user and in the IPC receipt.
- With `GOPOD_OUTBOX_ARTIFACTS=0` the agent receives `artifacts_disabled` and the
  user is told where the file is; nothing is silently dropped.
- The skill/template text is the only thing the agent needs: a fresh Claude and
  a fresh Codex session attach a file on the first try without extra prompting.
- Activation on the Pi is recorded with the backup/restore evidence CP-003
  requires; rollback is a flag flip plus retained snapshots.
