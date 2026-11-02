---
name: attachments
description: Send a file you made (PDF, spreadsheet, archive, text, image) to the
  user as a Telegram attachment. Use whenever you produced a file the user
  should receive, or the user asks you to send, attach or share a file.
---

# Sending files to the user

A file path in your reply is NOT an attachment. The user only gets a file when
the host accepts an IPC attachment request. You have this tool: never answer
that you cannot send files. Attachments work in the owner's private chat only;
anywhere else the request is refused with `artifacts_disabled`.

## Steps

1. Write the file under `/workspace/chat` (regular file, not empty, no symlink,
   no hard link, not hidden, not under `memory/`).
2. Publish the request atomically (write `.tmp`, then `mv` to `.json`):

```bash
name="attach-$(date +%s)-$RANDOM"
cat > "/workspace/ipc/messages/$name.tmp" <<JSON
{"version":1,"requestId":"$name","capability":"$GOPOD_ARTIFACT_CAP",
 "files":[{"path":"/workspace/chat/report.pdf","caption":"Report","as":"auto"}]}
JSON
mv "/workspace/ipc/messages/$name.tmp" "/workspace/ipc/messages/$name.json"
```

3. Read the receipt before answering:

```bash
for i in $(seq 1 30); do [ -f "/workspace/ipc/results/messages/$name.result.json" ] && break; sleep 1; done
cat "/workspace/ipc/results/messages/$name.result.json"
```

## Fields and limits

- Up to 4 `files`; each needs an absolute `path` under `/workspace/chat/`.
  Optional `caption` (plain text, at most 1024 characters), `filename`, and
  `as`: `auto` (default; PNG/JPEG as a photo, everything else as a document
  with its filename), `document` (always the original file) or `photo`
  (PNG/JPEG only, otherwise `unsupported_type`).
- Unknown fields reject the whole request. JSON-escape quotes in captions.
- 20 MiB per file, 40 MiB per request (Telegram's bot limit is 50 MB), plus
  per-run and hourly quotas.

## After the receipt

- `queued` or `sent`: accepted. Say briefly that you sent the file; do not
  paste its path again.
- `uncertain`: it may have arrived; do not resend.
- `error`: NOT attached; the `error` field is the reason (`artifacts_disabled`,
  `path_outside_workspace`, `path_denied`, `file_too_large`,
  `unsupported_type`, `source_missing`, `empty_file`, `stale_capability`, ...).
  The host also tells the user. Say that the file is at its path and could not
  be attached, with the reason; never claim it was sent.

Full guide: `/workspace/chat/ATTACHMENTS.md`.
