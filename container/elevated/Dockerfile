# Elevated host tools: root authority is granted only by the separate runner profile.
# Docker repository setup follows https://docs.docker.com/engine/install/debian/.
# Node 22 slim; immutable multi-architecture index.
FROM node@sha256:4b150b48b4116e2a22f2c39cc27025fbf4f153e2078fd3b14d159fdb5c7279da
ARG DOCKER_CLI_MAJOR=29
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl dnsutils git \
      iproute2 iputils-ping jq less lsof procps ripgrep rsync sqlite3 util-linux vim-tiny \
 && install -m 0755 -d /etc/apt/keyrings \
 && curl --proto '=https' --proto-redir '=https' -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc \
 && chmod a+r /etc/apt/keyrings/docker.asc \
 && . /etc/os-release \
 && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian ${VERSION_CODENAME} stable" > /etc/apt/sources.list.d/docker.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends "docker-ce-cli=5:${DOCKER_CLI_MAJOR}.*" docker-compose-plugin \
 && docker --version \
 && docker compose version \
 && rm -rf /var/lib/apt/lists/*

ARG CLAUDE_CODE_VERSION=2.1.310
RUN npm install -g --ignore-scripts "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && native_arch="$(node -p process.arch)" \
 && case "$native_arch" in arm64|x64) ;; *) exit 1 ;; esac \
 && native_binary="/usr/local/lib/node_modules/@anthropic-ai/claude-code/node_modules/@anthropic-ai/claude-code-linux-${native_arch}/claude" \
 && test -x "$native_binary" \
 && ln -sf "$native_binary" /usr/local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe \
 && test "$(claude --version)" = "${CLAUDE_CODE_VERSION} (Claude Code)" \
 && npm cache clean --force


ENV HOME=/home/node
RUN mkdir -p /workspace/session /home/node/.claude /home/node/.codex
# CP-038 intentionally requires host-root authority in this dedicated image.
USER 0:0
WORKDIR /workspace/session
CMD ["sleep", "infinity"]
