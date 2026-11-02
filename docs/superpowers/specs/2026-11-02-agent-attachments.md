# Agent attachments: the agent-facing file delivery contract

Date: 2026-11-02. [CP-040](../../proposals/CP-040-agent-attachments.md). Status:
**implemented on `feat/cp040-agent-attachments`; real-client acceptance pending**.
Host side: [immutable artifact delivery](2026-10-26-immutable-artifact-delivery.md)
(CP-003 PRs 4–5), [D039](../../DECISIONS.md), [D052](../../DECISIONS.md),
[D057](../../DECISIONS.md); decisions made here: [D058](../../DECISIONS.md).
Plan: [2026-11-02-agent-attachments](../plans/2026-11-02-agent-attachments.md).

## Outcome

An agent in the owner's private chat that produced a file sends it to the user as
a Telegram attachment in the same turn, by writing one IPC V1 request. When that
is impossible the agent gets a named reason in its receipt and the user gets a
host notice naming the file and the reason. Nothing is silently dropped, and the
agent never needs anything beyond the text described in "Discoverability".

The host pipeline (capability, rooted no-follow snapshot, quotas, outbox, upload
from verified bytes) is unchanged. This spec adds: the agent-facing text, the
`as` presentation field, three refusal codes, the refusal notice, and the rule
that a V1 file request which cannot reach the artifact route is refused instead
of being sent as text without its files.

## Discoverability (what the agent reads)

| Surface | Who sees it | Reaches existing workspaces |
|---|---|---|
| `AttachmentsPolicy` in the runner system prompt appendix (`internal/runner/policy.go`) | every owner turn, Claude (`--append-system-prompt`) and Codex (prompt prefix) | yes, every turn |
| `/workspace/chat/ATTACHMENTS.md` (`chattmpl/ATTACHMENTS.md.tmpl`) | owner workspace | yes: seeded when missing, never overwritten |
| "Sending files" section in the owner `CLAUDE.md`/`AGENTS.md` template | new owner workspaces | no (identity files are never rewritten) |
| `container/skills/attachments/SKILL.md` | agents with the container skills mount | yes |
| "Files" section in `RESTRICTED.md.tmpl` | new non-owner workspaces: attachments are unavailable; say where the file is | no |

The policy is owner-only, like `IntegrationsPolicy`: only the owner's private
chat receives a capability (D039), so telling another tier how to attach would
only produce refusals.

## Request (the only format; IPC V1)

Publish atomically to `/workspace/ipc/messages/<name>.json` (write `<name>.tmp`,
then rename; the watcher reads only `*.json`). `<name>` is unique and is reused as
`requestId`.

```json
{"version":1,"requestId":"attach-1730500000-12345","capability":"<$GOPOD_ARTIFACT_CAP>",
 "files":[{"path":"/workspace/chat/banks/report.pdf","caption":"Bank assessment","as":"auto"}]}
```

| Field | Rule |
|---|---|
| `version` | `1`. Its presence routes the request to the V1 parser. |
| `requestId` | `^[A-Za-z0-9][A-Za-z0-9_-]{7,63}$` |
| `capability` | the run's `GOPOD_ARTIFACT_CAP`, verbatim. Empty with files: `artifacts_disabled`; empty without files: `invalid_request`. |
| `chatJid` | optional; if present must be the capability's chat, else `recipient_unavailable`. Agents should omit it. |
| `text` | optional, at most 48 KiB, sent before the files. The guide tells agents to leave it out and answer in the normal reply. |
| `files` | 0–4 entries; `text` or `files` required. |
| `files[].path` | required; absolute, `path.Clean`-clean, under `/workspace/chat/`. Otherwise `path_outside_workspace`. Inside, hidden components, `memory/` at the top, database/WAL suffixes and mount-blocked paths are `path_denied` (D039, unchanged). |
| `files[].caption` | optional plain text, at most 1024 UTF-16 units (D052). |
| `files[].filename` | optional display name; default is the path's base name. Sanitized (no path components, control or bidi characters, at most 120 bytes keeping the extension). |
| `files[].mime` | optional claim, `type/subtype`; never decides authorization. |
| `files[].as` | optional: `auto` (default), `document`, `photo`. Any other value (including other case) is `invalid_request`. |
| `files[].presentation` | legacy D052 field (`document`/`photo`/empty, empty = document). Kept for compatibility; setting both `as` and `presentation` is `invalid_request`. |

Unknown fields anywhere reject the whole request (`invalid_request`). `voice`
stays refused (`voice_unavailable`).

## Presentation (document vs photo)

"PNG/JPEG candidate" means: `mime` is `image/png` or `image/jpeg`, or the
sanitized filename (explicit `filename`, else the path's base name) ends in
`.png`, `.jpg` or `.jpeg` (case-insensitive).

| `as` | Upload part marker | What the user gets |
|---|---|---|
| `auto` / omitted, candidate | photo | `sendPhoto` when the snapshot bytes are a PNG/JPEG within Telegram's photo limits (10 MB, side sum 10000, ratio 20, not CMYK, not APNG); otherwise, or on an explicit Telegram photo refusal, the same bytes as a document (D052 transport, unchanged) |
| `auto` / omitted, not a candidate (PDF, spreadsheets, archives, text, GIF, WebP, anything else) | none | `sendDocument` with the sanitized original filename and the caption |
| `document` | none | `sendDocument`, also for images (original quality) |
| `photo`, candidate | photo | as the first row |
| `photo`, not a candidate | — | request refused, `unsupported_type` |

Decision recorded in D058: `auto` infers a photo from the agent's name/MIME
claim. This supersedes D052's "the host never infers a photo from an image MIME
or extension" for the `as` field only. It cannot widen what is sent: the bytes
are the verified snapshot either way and the transport checks them before
`sendPhoto`. The legacy `presentation` field keeps D052 behavior exactly. The
voice marker stays host-only.

No content-type allowlist is added: Telegram documents accept any type, and D039
path rules, not extensions, are the security boundary. `unsupported_type` is
therefore only the forced-photo case above.

## Limits

- Effective per-file limit: the D039 store limit, 20 MiB (`store` default
  `MaxFileBytes`), per request 40 MiB (`min(files x 20 MiB, 40 MiB)`), per run
  8 files / 80 MiB, per namespace per hour 40 files / 200 MiB, namespace quota
  512 MiB, global 2 GiB. Telegram's 50 MB bot API limit is above all of these,
  so a 60 MB file is refused by the host before any upload, as `file_too_large`.
- CP-040 names this reason `too_large`; the existing D039 code `file_too_large`
  is kept rather than introducing a second name for the same refusal.
- At most two file-bearing requests are prepared per folder per watcher tick.

## Refusals

A refused request ends with `.failed` in the archive and a receipt
`/workspace/ipc/results/messages/<name>.result.json`:

```json
{"status":"error","requestFile":"<name>.json","error":"<code>",
 "help":"The file was NOT attached. Read /workspace/chat/ATTACHMENTS.md. ..."}
```

`help` is the attachment help for the file-related codes (all codes in the table
except `invalid_request`, `recipient_unavailable`, `request_expired`,
`too_many_parts`, which keep the durable help).

| Code | When |
|---|---|
| `artifacts_disabled` | (new) the request carries files and either the artifact route is off (`GOPOD_OUTBOX_ARTIFACTS=0`, outbox dispatcher or durable IPC route not running, artifact store failed to open) or the folder is not the owner folder, or the request has no capability |
| `path_outside_workspace` | (new) path not clean, not absolute, or not under `/workspace/chat/` (includes relative paths, `..`, `/etc/...`, `/workspace/ipc/...`) |
| `unsupported_type` | (new) `as:"photo"` on a non-candidate |
| `path_denied` | inside the workspace but hidden, `memory/`, database/WAL name, mount-blocked, or a symlink on the way (D039) |
| `file_too_large` | over the per-file or remaining request bound |
| `empty_file`, `source_missing`, `source_changed`, `not_regular_file`, `hardlink_denied` | D039 snapshot checks |
| `stale_capability` | capability unknown, revoked or expired |
| `quota_exceeded`, `storage_full` | retried a bounded number of passes (`status:"retrying"`), then refused |
| `rate_limited` | per-run or hourly cap |
| `recipient_unavailable`, `invalid_request`, `request_expired`, `too_many_parts`, `voice_unavailable` | existing V1 codes |

### User-visible notice

When a request that names at least one file reaches a terminal refusal for the
first time (not on replay of an already refused request), the host sends one
plain-text message to the chat the IPC folder is registered to (destination from
the folder, never from the request), in Russian:

```
Не удалось приложить файл /workspace/chat/banks/report.pdf
Причина: отправка файлов в этом чате выключена (artifacts_disabled).
Файл не отправлен и остался в рабочей папке чата.
```

Several files are listed one per line. Paths are display-only: control and bidi
characters removed, at most 200 bytes each with `…`. The notice is best effort
through the legacy IPC message sink (thread 0, 10 s timeout); a failed notice is
logged and the receipt remains the authority. A refused request delivers
nothing else: its `text` is not sent without its files.

### What the agent is told to reply

On `queued`/`sent`: say briefly that the file was sent; do not repeat the path.
On `uncertain`: do not resend. On `error`: say the file is at its path and could
not be attached, with the reason; never claim it was sent; retry only after
fixing the cause, with a new name and `requestId`.

## Routing summary (watcher)

1. Owner folder, artifact route on, `version` present: V1 artifact route (D039),
   refusals with files produce the notice.
2. Otherwise, `version` present and `files` non-empty: `artifacts_disabled`
   receipt, `.failed`, notice.
3. Otherwise: unchanged (durable text route or legacy V0 path).

## Out of scope

Agent-initiated downloads from the internet, inline images in streamed text,
editing or deleting sent files, files above the limits (refused; chunking or
external storage is a separate CP), voice (CP-003 PR 5), CP-014 media input,
group delivery (CP-019/CP-020), topic-aware notices (the notice lands in thread
0 like every IPC message), the Pi activation record and retention/restore checks
listed under CP-040 item 4 (owner-run).

## Acceptance

Real-client rows A22–A25 in [ACCEPTANCE](../../ACCEPTANCE.md). Unit coverage:
`internal/ipc/attachments_test.go`, `artifact_presentation_test.go`,
`artifacts_test.go` (path cases), `internal/telegram/outbox_attachment_test.go`,
`internal/runner/policy_test.go`, `attachments_skill_test.go`,
`internal/runner/chattmpl/chattmpl_test.go`.
