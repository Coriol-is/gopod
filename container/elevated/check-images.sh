#!/bin/sh
# Local, credential-free smoke checks. No socket/host mounts or privileged mode.
set -eu
claude_image=${1:-gopod-agent-elevated:latest}
codex_image=${2:-gopod-agent-elevated-codex:latest}
for image in "$claude_image" "$codex_image"; do
  docker image inspect "$image" --format '{{.Id}} {{.Os}}/{{.Architecture}}'
  docker run --rm --network none --entrypoint sh "$image" -ec '
    test "$(id -u)" = 0
    test "$HOME" = /home/node
    test "$PWD" = /workspace/session
    for tool in docker nsenter ps ip ss ping dig lsof rsync sqlite3 jq less vi git rg curl; do
      command -v "$tool" >/dev/null
    done
    docker --version
    docker compose version
  '
done
docker run --rm --network none --entrypoint claude "$claude_image" --version
docker run --rm --network none --entrypoint codex "$codex_image" --version
