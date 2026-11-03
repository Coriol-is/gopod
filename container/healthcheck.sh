#!/bin/sh
# Offline runtime prerequisites only. Never execute the provider CLI, read auth,
# write probe files, inspect host PID 1 (elevated uses host PID), or call network.
set -eu
fail() { echo 'runtime prerequisites unavailable' >&2; exit 1; }
[ "$#" -eq 2 ] || fail
provider=$1
workspace=$2
[ -d "$workspace" ] && [ -r "$workspace" ] && [ -x "$workspace" ] || fail
[ -x /bin/sh ] && [ -x /bin/sleep ] && [ -x /usr/local/bin/node ] || fail
case "$provider" in
    claude|codex) [ -x "/usr/local/bin/$provider" ] || fail ;;
    supervisor) [ -r /usr/local/lib/gopod-elevated-exec.cjs ] || fail ;;
    *) fail ;;
esac
case "$workspace" in
    /workspace/session) [ -r /usr/local/lib/gopod-elevated-exec.cjs ] || fail ;;
    /workspace/inference) [ -r /etc/gopod-group/ca.crt ] || fail ;;
esac
