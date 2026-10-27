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

## CLI versions

The harness CLIs are pinned by build arg so the version baked into an
image is reproducible and visible in git:

| Image | Arg | Default |
|---|---|---|
| `gopod-agent` | `CLAUDE_CODE_VERSION` | `2.1.310` |
| `gopod-agent-codex` | `CODEX_VERSION` | `0.164.0` |

Bump = edit the default (one-line commit) or pass
`--build-arg CLAUDE_CODE_VERSION=x.y.z`. Nothing rebuilds agent images
automatically; `docker compose build` only rebuilds the gopod binary.
After a bump, rebuild the images on every host that runs gopod.

Check what a built image carries:

```sh
docker run --rm --entrypoint claude gopod-agent:latest --version
docker run --rm --entrypoint codex  gopod-agent-codex:latest --version
```

gopod also logs the version once per container spawn
(`agent container ready ... cli_version=...`).

The Docker integration suite can assert the expected versions:

```sh
GOPOD_TEST_CLAUDE_VERSION=2.1.310 GOPOD_TEST_CODEX_VERSION=0.164.0 \
  go test -tags docker_integration -run 'AgentImage|CodexImage' ./internal/runner/
```

`GOPOD_TEST_AGENT_IMAGE` / `GOPOD_TEST_CODEX_IMAGE` point it at
differently tagged images. Tests skip when an image is not built.

### Qualification on 2026-10-25

The defaults follow the normal npm `latest` releases verified that day:
[Claude Code 2.1.310](https://github.com/anthropics/claude-code/releases/tag/v2.1.310)
and [Codex CLI 0.164.0](https://learn.chatgpt.com/docs/changelog).
Codex's `0.165.0-alpha.1` was excluded. Anthropic's delayed `stable` channel
pointed to `2.1.308`; this pin deliberately uses the newer non-prerelease
`latest`, not that delayed channel. See [Claude release channels](https://code.claude.com/docs/en/setup#configure-release-channel).

Both complete images were built and tested on local Linux ARM64 Docker.
The agent-image integration tests passed (auth check, hardened container args,
CLI versions). Read-only, network-disabled containers under uid 1000 also
passed version/help checks for the existing fresh/resume/session/stream/model
and device-login flags. Claude accepted the runner's
`NODE_OPTIONS=--max-old-space-size=768` environment for these probes, but its
new native binary is **not qualified as obeying a V8 heap limit**. Do not treat
`GOPOD_AGENT_NODE_HEAP_MB` as a Claude RAM cap for this version. Admission control
still applies; hard memory limits require host cgroup enforcement.

No authenticated model turn, production Pi rebuild or rollout was performed
by this qualification. Versions in a running deployment change only after its
agent images are rebuilt and its agent containers are recreated. Historical
stream recordings retain their original version provenance.

## Contents

| Component | Why |
|---|---|
| `node:22-slim` base | Node 22+ for the Claude npm installer and agent scripts; Claude itself is native |
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

## Personal and group build sets (CP-020)

`container/personal/Dockerfile{,.codex}` preserves the existing personal contents
and tags. `container/group/Dockerfile{,.codex}` builds separate trusted inference
images, with pinned CLIs and CA roots but no FreeFeed CLI, curl, git, ripgrep,
private skills or personal configuration. Neither group image inherits a personal
final image. Build all four from the repository root with:

```sh
./container/build-profiles.sh
```

Group tags are `gopod-agent-group:latest` and `gopod-agent-group-codex:latest`.
These names do not authorize group admission: the controller requires qualified
namespace policy, exact image/client identities, protected state/policy mounts,
scoped external MCP tools and enforced inference-only network routes.

The trusted native CLI sees only its own fresh session state and a synthetic
`/workspace/inference` directory. Actual group files are available only to the
separate credential-free tool sandbox. Fixed policy files are controller-owned
read-only binds; upstream native auth is a separate read-only file grant, never
an inherited personal HOME or a fallback API key. No image includes credentials.
The proxy CA is mounted at `/etc/gopod-group/ca.crt`; proxy credentials and the
namespace-bound MCP capability are minted by the host, never chosen by a prompt.

Pinned-CLI offline qualification uses synthetic localhost model/MCP servers and
fresh temporary HOME/CODEX_HOME; no real authentication or inference is used:

```sh
GOPOD_GROUP_NATIVE_TEST=1 go test ./internal/runner -run '^TestGroupNativeInventory' -count=1
```

This checks actual new/resume tool inventories, successful approved MCP calls and
rejection of model-requested native Bash/exec_command. It does not prove the live
subscription, image network policy or route allowlist; those are separate runtime
acceptance gates. Official controls: [Claude CLI](https://code.claude.com/docs/en/cli-reference),
[Claude MCP/connector settings](https://code.claude.com/docs/en/mcp),
[Codex MCP settings](https://learn.chatgpt.com/docs/extend/mcp) and
[Codex configuration](https://learn.chatgpt.com/docs/config-file/config-reference).

Group Codex uses the documented native-auth custom provider ID
`gopod_group_native` to select HTTPS/SSE while retaining native ChatGPT login.
Production config supplies no base URL or API key; see the
[pinned source and offline qualification](../docs/research/2026-10-26-codex-group-native-sse.md).
The separate synthetic ChatGPT-auth Docker fixture runs on an internal network:

```sh
python3 experiments/subscription-isolation/codex_sse_auth_probe.py --cli-image gopod-agent-group-codex:latest
```

Profile image npm installs disable package lifecycle scripts. Claude 2.1.310 ships
an executable placeholder in the wrapper package, so the reviewed Docker build
links `claude` directly to the installed pinned Linux optional-package binary,
checks the supported architecture and verifies its version. No upstream installer
script is run manually. Missing native packages or version mismatches fail build.

### Codex catalog policy (group-native-v2)

Rebuild both group native images when the shared policy label changes; personal
images do not need rebuilding for a group-only policy update. Codex requires the
controller's readonly sanitized model catalog and an admitted model on every run.
Feature flags alone do not disable metadata-driven `apply_patch`/code/agent tools.
See [catalog provenance and preparation](group/policy/README.md) and
[host configuration](../docs/ISOLATION.md#9-configuration). The public snapshot is
not baked into a native HOME and does not inherit owner instructions or caches.
