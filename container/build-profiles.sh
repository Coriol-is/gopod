#!/bin/sh
# Run from the repository root. Personal tags retain existing runtime defaults.
set -eu
docker build -f container/personal/Dockerfile -t gopod-agent:latest .
docker build -f container/personal/Dockerfile.codex -t gopod-agent-codex:latest .
docker build -f container/group/Dockerfile -t gopod-agent-group:latest .
docker build -f container/group/Dockerfile.codex -t gopod-agent-group-codex:latest .
docker build -f container/group/Dockerfile.tools -t gopod-group-tools:latest .
