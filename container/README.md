# gopod agent container

The image gopod spawns per registered chat. See
[../docs/ARCHITECTURE.md §5.2](../docs/ARCHITECTURE.md) and
[../docs/ISOLATION.md](../docs/ISOLATION.md) for the design rationale.

## Build

Build context is the repo root, not `container/`, because stage 1
of the Dockerfile builds the `frf` CLI from the `skills/frf-tui/`
git submodule. Run from repo root:

```sh
# Claude (default)
docker build -t gopod-agent:latest -f container/Dockerfile .

# Codex variant (optional)
docker build -t gopod-agent-codex:latest -f container/Dockerfile.codex .
```

If the submodule was not pulled at clone time, run
`git submodule update --init --recursive` first — otherwise stage 1
fails with `"/skills/frf-tui": not found`.

The resulting image runs as the non-root `node` user inside, but
gopod overrides this at spawn time with `--user <host_uid>:<host_gid>`
so bind-mounted files stay owned by the operator on the host
([D013](../docs/DECISIONS.md) / ISOLATION.md §6.1).

## Contents

| Component | Why |
|---|---|
| `node:22-slim` base | `@anthropic-ai/claude-code` is a Node CLI |
| `@anthropic-ai/claude-code` | the agent gopod drives via `docker exec` |
| `git` | agent workflows and self-modification |
| `ripgrep` | search tool Claude Code shells out to |
| `ca-certificates` | outbound TLS to Anthropic API |
| `curl` | common agent primitive |

Nothing else is baked in on purpose. Per-chat skills live in
`container/skills/` on the host and are bind-mounted at spawn time
([SKILLS.md](../docs/SKILLS.md)).

## `skills/`

This directory holds container skills — drop a `SKILL.md` + optional
scripts here and they appear inside the agent container at
`/home/node/.claude/skills/<name>/`, filtered per chat by the
registered `skills.json`. Empty by default.

## Entrypoint

`CMD ["sleep", "infinity"]`. The container is long-lived per chat;
every agent turn is a fresh `docker exec ... claude ...` invocation
driven by the host Go runner. There is no `entrypoint.sh` — if
bootstrapping grows beyond one line, a real entrypoint lands next to
the Dockerfile.
