# CP-005 — Lightweight bases and optional tool runtimes

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-05; revised 2026-10-26 |
| Origin | Earlier codex-agent survey; owner requested optional tool images for Pi |
| Size | L |
| Planning wave | 4 deferred; candidate, not an implementation queue change |
| Touches | profile/provider builds, controlled tool workers, cache/resources |
| Depends on | CP-011, CP-014, CP-020; CP-003 for exports |
| Spec | [Design](../superpowers/specs/2026-10-26-optional-tool-images.md) |

## Problem

Installing Chromium, LibreOffice, OCR and speech stacks in every CLI image grows Pi disk/build
costs and broadens tool grants even when a task needs none of them.

## Proposal

Keep a lightweight qualified shared base where useful, preserve the current
personal defaults, and offer named optional tool packs through controller-owned
isolated workers. The model chooses a capability, not a Docker image/socket,
host path or flags. Private auth stays outside tool execution. Follow the
[adoption map](2026-10-26-codex-agent-adoption.md); no pack is enabled by this proposal.

## Design evolution

The 2026-10-05 draft excluded tools sidecars/MCP and proposed adding extractors to
both CLI images. The 2026-10-26 owner-requested design supersedes that exclusion:
separate optional controller-brokered tool runtimes are now candidates. Two
personal/group Dockerfile sets and independent provider selection remain intact.
This is a proposed design change, not evidence of deployed isolation or a new
heavy default image.

## Out of scope

Enabling office/browser/video tools immediately, a Docker socket inside an agent, automatic
arbitrary package installation or copying the donor's whole runtime.

## Risks

Heavy images can exhaust Pi storage and memory. Tool/runtime isolation, codec safety and
deterministic versions need independent qualification.

## Acceptance

- arm64/amd64 builds and pinned capability manifest pass known synthetic format fixtures.
- Untrusted image/flags/env/mount requests are rejected; worker cannot see personal credentials
  or Docker socket.
- Cold and warm Pi runs record memory, disk, latency and foreground responsiveness before a pack
  is enabled.
- Corrupt/oversized input, OOM and timeout yield bounded failures; output remains source-scoped
  and immutable.
