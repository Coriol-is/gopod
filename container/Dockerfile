# picoclaw agent container
#
# Minimum viable image for M5e. The container is long-lived per chat
# (see docs/ARCHITECTURE.md §5.2 and docs/ISOLATION.md §7): picoclaw
# spawns it, the entrypoint waits forever, and every agent turn is a
# fresh `docker exec ... claude ...` invocation driven by the host Go
# runner.
#
# Design notes:
#
#   - Base image is `node:22-slim`. node is a hard dependency of
#     `@anthropic-ai/claude-code` — the Claude Code CLI is a Node
#     application; picoclaw itself is Go and lives on the host.
#   - Only four extra binaries go in: `git`, `ripgrep`, `ca-certificates`,
#     and `curl`. `git` is needed by most agent workflows, `ripgrep`
#     is the search tool Claude Code shells out to, `ca-certificates`
#     is required for outbound TLS to the Anthropic API, and `curl` is
#     a common agent primitive.
#   - Non-root. The image runs as the pre-existing `node` user
#     (uid 1000, gid 1000). docs/ISOLATION.md §6.1 requires non-root;
#     picoclaw passes `--user <host_uid>:<host_gid>` at spawn time, so
#     this image's USER is a build-time sanity default, not the final
#     runtime identity.
#   - Working directory is `/workspace/chat`. picoclaw bind-mounts the
#     per-chat workspace there (mounts.go).
#   - No entrypoint.sh hand-off — the entrypoint is `sleep infinity`.
#     There is no state to set up; the bind-mounts carry all the
#     per-chat configuration and there is nothing for a shell script
#     to massage. If bootstrapping gets complicated later, a real
#     entrypoint.sh can land next to this Dockerfile.

FROM node:22-slim

# Install runtime dependencies. `--no-install-recommends` keeps the
# image small; apt cache is cleaned in the same layer so it does not
# leak into the image.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      curl \
      git \
      ripgrep \
 && rm -rf /var/lib/apt/lists/*

# Install the Claude Code CLI globally. Pinning is left to the apt/npm
# default for M5e; a pinned version is a future ADR-worthy decision
# once we know which release stream we track.
RUN npm install -g @anthropic-ai/claude-code \
 && npm cache clean --force

# picoclaw bind-mounts /workspace/chat at spawn time per ISOLATION.md
# §3. The directory needs to exist first — Docker bind-mount targets
# are created on demand but explicit mkdir keeps the layer permissions
# predictable.
RUN mkdir -p /workspace/chat /workspace/memory /workspace/ipc

# Run as the stock non-root `node` user baked into node:22-slim
# (uid 1000). picoclaw overrides this at spawn time via
# --user <host_uid>:<host_gid> so bind-mount writes are owned by the
# host operator rather than container-side uid 1000.
USER node

WORKDIR /workspace/chat

# Long-lived container. There is nothing to do here except keep the
# PID 1 process alive so `docker exec` has somewhere to run. `sleep
# infinity` is the shortest, clearest way to say that.
CMD ["sleep", "infinity"]
