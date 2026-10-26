# CP-003 — Files and voice as typed replies via IPC

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-05 |
| Origin | feature survey of `rzaytsev/codex-agent` (2026-10-05) and the 2026-10-03 public-release audit |
| Size | M |
| Touches | `internal/ipc` (payload schema), `internal/telegram/ipc_sink.go`, `container/skills` (a `reply` skill or CLAUDE.md instruction) |
| Depends on | M7 (IPC, done) |
| Spec | [Design](../superpowers/specs/2026-10-26-immutable-artifact-delivery.md) |

## Problem
An agent that produces a file (a chart, a CSV, a generated document) has no way to hand it to the user except pasting a path into prose. Voice replies are decided host-side by reply mode, never by the agent. `codex-agent` solves this with a per-turn `outputSchema {text, voice, files[]}`, which would kill our streaming.

## Proposal

Extend text IPC with versioned optional files/voice intent. The host prepares
namespace-bound immutable snapshots with hashes, preserves original metadata,
and atomically publishes references through CP-002. Rooted/no-follow validation,
protected paths, storage quotas and scoped diagnostic receipts are required.
Keep text streaming unchanged; a queued artifact is not a delivered artifact.

## Out of scope
Structured output for the text itself. Inline images in streamed text. Files larger than Telegram limits (reject with a clear IPC `.failed` reason).

## Risks

Filesystem path authorization must resist symlink/hardlink substitution;
resolving a path once is not a complete race-safe boundary. Secret content copied
to innocent names still requires runtime isolation. Snapshot quotas, retention,
backup consistency and delivery uncertainty are part of this design.

## Acceptance
- Agent writes `{chatJid, text, files:[{path:"/workspace/chat/out.png"}]}`; the user receives the photo with caption within the watcher tick.
- A path outside `/workspace/chat` lands in `.failed/` with a reason and is never sent.
- `voice: true` produces a voice note using the existing TTS path.

## 2026-10-25 reconciliation
Text-only IPC now has diagnostic receipts under results/messages. Future typed
files/voice must return equivalent scoped reasons and preserve an explicit
public/private delivery target (CP-019), rather than treating a filesystem path
or a failed private send as permission to publish elsewhere. This proposal
remains unimplemented and supports appropriate CP-014 artifact output slices.

## 2026-10-26 adoption design

The linked spec now defines this candidate in the [adoption map](2026-10-26-codex-agent-adoption.md),
wave **1**. Status remains proposed; a saved design does not approve
implementation or change the existing delivery queue. New acceptance/migration
requirements in the dated spec supersede conflicting early shorthand here.
