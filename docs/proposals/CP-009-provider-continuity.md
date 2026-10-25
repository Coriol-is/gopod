# CP-009 — Continue conversations across providers

| | |
|---|---|
| Status | done |
| Date | 2026-10-25 |
| Origin | Owner request: implement seamless manual provider switching, with correct thread isolation; automatic quota fallback is later work |
| Size | M |
| Touches | store, runner, control, command wiring |
| Depends on | D022, D028 |
| Spec | [Provider continuity](../superpowers/specs/2026-10-25-provider-continuity.md) |
| Plan | [Implementation plan](../superpowers/plans/2026-10-25-provider-continuity.md) |

## Problem
Changing `/provider` cleared the selected provider's main session and supplied
no conversation history to the new harness. Native CLI sessions cannot be
shared between Claude and Codex. This makes a manual switch after quota
exhaustion lose the current task, despite persisted chat history and files.

## Proposal
Preserve native sessions and supply a bounded, provider-neutral transcript of
this specific conversation on the next provider's turn. Keep switching manual.

## Out of scope
Automatic quota fallback, provider/model selection per thread, conversion of
native tool transcripts, unlimited context replay, and deployment.

## Risks
Recent-history replay is bounded; it cannot reproduce hidden/tool context or
guarantee that a model recalls every old detail. Context must remain isolated
by chat and thread. Clearing must prevent history reappearing on a switch back.

## Acceptance
- Claude → Codex → Claude retains each native session and transfers recent
  messages, replies, and work performed while a different provider was active.
- Main and sibling threads never contribute messages to each other's bridge.
- Restart preserves the bridge and provider setting; failed runs do not consume it.
- Switching waits for an active run and honours cancellation.
- `/clear` affects one conversation across providers without resurrecting it.
- Build, vet, full tests, race tests, independent review and CI pass.
