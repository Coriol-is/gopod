# Agent provider registry and routing

**Date:** 2026-10-25  
**Status:** Design saved; implementation deferred  
**Proposal:** CP-012  
**Depends on:** CP-011; D028, D030, D031; proposed D032

## Goal
Make additional agents an explicit registration and qualification task. Separate
agent identity from execution backend identity. Preserve current Claude/Codex
behaviour first. No new provider, login, credential or paid request is enabled
by this document.

## Verified baseline
`AgentProvider` already owns CLI commands, auth/session handling and optional
structured streaming. Main wiring constructs a Claude/Codex map. The provider
command takes an explicit names slice. Quota UI/validation explicitly permits
only Claude ↔ Codex. `Image()` embeds a Docker packaging assumption.
Those sites must derive from a registry before another provider can be considered
supported end to end; implementing RunCmd alone is insufficient.

## Descriptor and responsibilities
A statically registered descriptor has a stable id, display name, provider
implementation, model-choice metadata, supported execution artifacts/runtime
capabilities, auth capabilities and quota classification policy. Registry ids
are validated; duplicate/unknown ids fail clearly. Display labels never become
shell argv, filesystem paths or authorization identifiers.

Credentials are resolved at execution/login time, not stored in a descriptor.
Do not scan credential stores or invoke a model while listing providers/models.
Docker image names are one artifact mapping, not the agent's universal identity.
An incompatible provider/runtime combination is rejected before replacing the
current working sandbox or consuming a queue item.

## Selection and persistence
- Provider remains **per chat**. Menus and confirmations explicitly say this.
- Native sessions stay per `(chat folder, thread, provider, execution domain)`.
  Domain means the durable backend + persistent session-storage namespace defined
  in CP-011, stable across Docker container recreation and provider roundtrips.
  Allocation/container/run ids are ephemeral and do not change this lookup key.
- Main is thread 0, never a wildcard over sibling transcripts.
- A switch waits for active work, compares any expected provider atomically,
  validates compatibility, then persists selection/model reset and replaces
  execution resources with checked errors.
- On restart, unknown/removed provider ids are reported as unavailable. Do not
  silently route history or credentials to a different provider.
- Existing identities and choices migrate without deleting Claude/Codex sessions.
  Runtime identity adoption is a one-time explicit migration, with tests.
- D030 provider-neutral bounded history crosses harnesses in the same conversation;
  raw native session files are not translated. Workspace availability is checked
  independently, especially if a runtime change is involved.

## Capabilities
Declare structured streaming, interactive auth, explicit session resume, fresh
system turns, cancellation support, attachments and model override behaviour.
An optional feature has a tested fallback or a clear refusal; unsupported
features must not silently discard input. Models are provider-specific. Retain
current model reset semantics when changing harnesses.

Keep authentication errors, provider allowance exhaustion, transport throttling,
agent failure and reply-delivery failure separate. A generic HTTP 429 is not
proof that an account quota was exhausted. Classification uses trusted CLI
metadata/stderr or the main result channel, never tool transcript text or a
model-selected provider id. User-facing errors contain no raw credentials/paths.

## Confirmed quota alternatives
D031 remains the default: no unattended switch, quota polling or automatic return.
Candidates come from an explicit operator allowlist/configured order and runtime
compatibility; registering a provider alone never makes it a fallback candidate.
The originating failed provider identity is retained even if selection changes.

With multiple candidates, show named alternatives and require an explicit
switch-and-continue confirmation for the selected target. Validate the same
original chat/folder/thread and expected selection on press. Do not follow a
cycle through candidates when another quota error occurs. Preserve one durable
retry identity per original confirmed attempt and truthful switch/queue notices.
Login readiness/costs may be stated only from verified metadata; do not claim
that a listed provider is authenticated or free. No login or billable model
probe happens just to render a choice.

## Delivery sequence
1. Central registry for current Claude/Codex; remove duplicate names/maps and
   preserve menus, current-provider markers and existing confirmation semantics.
2. Explicit provider/runtime capability and artifact validation against CP-011.
3. Configured quota candidate selection using D031 single-use buttons and durable
   deduplication, with compatibility/staleness/permission tests.
4. A separately approved provider addition supplies fixtures and passes live
   contract acceptance, including auth, real resume and Stop. No provider chosen here.

## Acceptance
Verify registry duplication/unknown-id rejection; menu/help capability filtering;
backward-compatible persistence; main/topic/chat isolation; switch-back and
restart; wrong-domain native id rejection; clear across all provider mappings
in one conversation; no fallback before consent; stale and foreign callbacks;
double tap; two exhausted candidates; unauthenticated target; failed resource
replacement or queue insertion; and secrets absent from metadata/logs/UI.
The third-provider test adapter must exercise registration end to end, not
just unit-test a new RunCmd implementation. Fake CLI tests are insufficient to
claim support for a production agent.
