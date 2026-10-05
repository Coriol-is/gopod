# CP-005 — Shared tools base image for agent containers

| | |
|---|---|
| Status | proposed |
| Date | 2026-10-05 |
| Origin | feature survey of `rzaytsev/codex-agent` (2026-10-05) and the 2026-10-03 public-release audit |
| Size | M |
| Touches | `container/Dockerfile.tools` (new), `container/Dockerfile`, `container/Dockerfile.codex`, `container/README.md`, Pi build procedure |
| Depends on | — |
| Spec | `—` |

## Problem
Both agent images are `node:22-slim` plus one CLI. Useful tools (PDF text extraction, document conversion, a Python runtime for the agent's own scripts) are missing, so PDF input is unsupported and agents shell out to nothing. Adding tools to both Dockerfiles duplicates layers and couples tool bumps to CLI pin bumps. Running the CLIs "inside" a separate tools container is not possible without giving agent containers a Docker socket, and a tools sidecar over MCP adds a network hop and a second auth perimeter per chat.

## Proposal
A layered image set: `gopod-tools:latest` (node:22-slim + git, ripgrep, curl, `poppler-utils` for `pdftotext`, `pandoc`, `python3` + `uv`, `ffmpeg` if voice work needs it) built once; `gopod-agent` and `gopod-agent-codex` become `FROM gopod-tools` plus the pinned CLI. One tools layer shared by both, CLI bumps do not rebuild tools, tool bumps do not touch CLI pins. Heavy items (LibreOffice, Chromium, local whisper) stay out until a concrete use case exists.

## Out of scope
A sidecar tools container. MCP tool servers. Per-chat tool allowlists.

## Risks
Image size grows once (expected a few hundred MB); the Pi build time grows; `container/README.md` build order must say tools first.

## Acceptance
- `docker build -f container/Dockerfile.tools` then both agent builds succeed on arm64 and amd64.
- `pdftotext --version`, `pandoc --version`, `uv --version` work inside a spawned agent container as the non-root user.
- Integration test asserts the tool set; README documents the build order and the bump procedure.
