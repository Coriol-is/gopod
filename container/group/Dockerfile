# Trusted Claude inference runtime only. Real group files/tools execute elsewhere.
# Never derive this image from the personal final image or copy private tools.
FROM node:22-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
ARG CLAUDE_CODE_VERSION=2.1.310
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} \
 && npm cache clean --force
RUN mkdir -p /workspace/inference /etc/gopod-group /home/node/.claude /home/node/.codex \
 && chown -R node:node /home/node/.claude /home/node/.codex
LABEL org.gopod.profile="group" org.gopod.provider="claude" org.gopod.native-policy="group-native-v1"
ENV ENABLE_CLAUDEAI_MCP_SERVERS=false \
 ENABLE_TOOL_SEARCH=false \
 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
 DISABLE_AUTOUPDATER=1 \
 NODE_EXTRA_CA_CERTS=/etc/gopod-group/ca.crt \
 SSL_CERT_FILE=/etc/gopod-group/ca.crt
USER node
WORKDIR /workspace/inference
CMD ["sleep", "infinity"]
