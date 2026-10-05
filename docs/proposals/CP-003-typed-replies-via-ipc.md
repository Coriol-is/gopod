# CP-003 — Files and voice as typed replies via IPC

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-05 |
| Origin | feature survey of `rzaytsev/codex-agent` (2026-10-05) and the 2026-10-03 public-release audit |
| Size | S |
| Touches | `internal/ipc` (payload schema), `internal/telegram/ipc_sink.go`, `container/skills` (a `reply` skill or CLAUDE.md instruction) |
| Depends on | M7 (IPC, done) |
| Spec | `—` |

## Problem
An agent that produces a file (a chart, a CSV, a generated document) has no way to hand it to the user except pasting a path into prose. Voice replies are decided host-side by reply mode, never by the agent. `codex-agent` solves this with a per-turn `outputSchema {text, voice, files[]}`, which would kill our streaming.

## Proposal
Extend the existing IPC `messages/*.json` payload with optional `files: [{path, caption}]` and `voice: true`. Paths must be under `/workspace/chat` (host-side path check, path-based authorization as always). The IPC sink sends documents/photos via Telegram and, for `voice`, routes the text through the existing TTS path. The agent learns about it from the seeded CLAUDE.md. Streaming is untouched because text still flows through the normal turn.

## Out of scope
Structured output for the text itself. Inline images in streamed text. Files larger than Telegram limits (reject with a clear IPC `.failed` reason).

## Risks
A path check that is too loose lets an agent exfiltrate the owner's project mount; the check must resolve symlinks like `mountsec` does.

## Acceptance
- Agent writes `{chatJid, text, files:[{path:"/workspace/chat/out.png"}]}`; the user receives the photo with caption within the watcher tick.
- A path outside `/workspace/chat` lands in `.failed/` with a reason and is never sent.
- `voice: true` produces a voice note using the existing TTS path.
