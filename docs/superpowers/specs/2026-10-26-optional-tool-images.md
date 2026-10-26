# Lightweight bases and optional tool runtimes

Date: 2026-10-26. CP-005. Status: **proposed design**, not implemented or enabled.
Planning wave: 4; size estimate: L.

[Proposal](../../proposals/CP-005-tools-base-image.md) ·
[Shared contracts](2026-10-26-codex-agent-adoption-common-contract.md) ·
[Adoption map](../../proposals/2026-10-26-codex-agent-adoption.md).

## User outcome and current gap

Installing Chromium, LibreOffice, OCR and speech stacks in every CLI image grows Pi disk/build
costs and broadens tool grants even when a task needs none of them.

## Dependencies and boundaries

CP-011 runtime contract; CP-014 selected media formats; CP-020 group boundary; CP-003 exports.
The shared contracts are mandatory. Conditional group/visibility dependencies do
not block a correctly bounded personal-only slice; they block its group rollout.

## Required behavior

- Retain two image/profile sets, personal and group, each with Claude/Codex variants. Preserve
  current personal default contents/behavior in the initial migration. Share only nonpersonal
  base stages; group final images never derive from the personal final image.
- Keep a small qualified common toolset. Add optional pinned packs, initially documents (PDF
  conversion/rendering/office), OCR/image, browser and media/speech as separate candidates. A
  pack is installed/built/cached by the controller, not apt/npm commands issued by an
  untrusted agent.
- A job requests a named capability, never a Docker image, host path, flag list or socket.
  Trusted namespace-bound runtime operations select the approved digest and mounts. No Docker
  socket, personal auth/home/env or cross-chat workspace is exposed to a tool worker.
- Mount bounded immutable inputs and a dedicated output area; export results through CP-003.
  Network is disabled by default; browser/public-fetch exceptions need explicit destination
  policy, not access to host/LAN/account services.
- Proposed Pi qualification starting point: one heavy job at a time, explicit
  CPU/memory/disk/wall limits and cold-start/cache budgets. Actual values require measurement
  on the target Pi; do not promise that 512 MiB suits every LibreOffice/browser workload.
- Missing packs, unsupported architecture or insufficient resources fail with a useful status;
  no fallback to the personal image or arbitrary host execution. Image-only presence is not
  evidence of extraction/render quality.

## Acceptance

- arm64/amd64 builds and pinned capability manifest pass known synthetic format fixtures.
- Untrusted image/flags/env/mount requests are rejected; worker cannot see personal credentials
  or Docker socket.
- Cold and warm Pi runs record memory, disk, latency and foreground responsiveness before a pack
  is enabled.
- Corrupt/oversized input, OOM and timeout yield bounded failures; output remains source-scoped
  and immutable.

## Migration, retention and rollback

Add packs without removing existing personal tools or auto-installing them at boot. Preserve
image digests/rollback manifests; prune only controller-owned unused cache entries under an
explicit retention policy.

## Risks and decisions before implementation

Heavy images can exhaust Pi storage and memory. Tool/runtime isolation, codec safety and
deterministic versions need independent qualification.
Defaults/limits above are proposed product policy, not existing configuration.
Before implementation, choose exact schema/API changes, implementation slices,
resource/retention values and any deployment/account consent needed. Record those
choices in an executable plan; unresolved privacy/effect questions fail closed.

## Out of scope

Enabling office/browser/video tools immediately, a Docker socket inside an agent, automatic
arbitrary package installation or copying the donor's whole runtime.

## Source evidence

Behavioral reference: `rzaytsev/codex-agent@b231ed572c6b4acaff057e715570b867aa49a75b`:
`Dockerfile; src/python.js; src/agent.js; docs/shared-skills.md`. Source/tests indicate implementation intent and mechanics;
this document does not certify donor deployment, native-account isolation or
our own live acceptance. Implementation is adapted to Go and both providers.
