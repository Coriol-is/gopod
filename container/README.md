# picoclaw agent container

The image picoclaw spawns per registered chat. See
[../docs/ARCHITECTURE.md §5.2](../docs/ARCHITECTURE.md) and
[../docs/ISOLATION.md](../docs/ISOLATION.md) for the design rationale.

## Build

```sh
docker build -t picoclaw-agent:latest container/
```

The resulting image runs as the non-root `node` user inside, but
picoclaw overrides this at spawn time with `--user <host_uid>:<host_gid>`
so bind-mounted files stay owned by the operator on the host
([D013](../docs/DECISIONS.md) / ISOLATION.md §6.1).

## Contents

| Component | Why |
|---|---|
| `node:22-slim` base | `@anthropic-ai/claude-code` is a Node CLI |
| `@anthropic-ai/claude-code` | the agent picoclaw drives via `docker exec` |
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
