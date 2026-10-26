# Group Codex: native ChatGPT authentication over HTTPS/SSE

Date: 2026-10-26. Scope: CP-020 trusted group inference runtime. No real
credentials, subscription quota or live inference were used for this qualification.

## Supported configuration

Use a nonreserved native provider ID to select transport without overriding a
built-in provider or translating OAuth tokens into an application-owned API call:

```toml
model_provider = "gopod_group_native"

[model_providers.gopod_group_native]
name = "Group native ChatGPT"
requires_openai_auth = true
wire_api = "responses"
supports_websockets = false
```

Production deliberately omits `base_url`, `env_key`, `experimental_bearer_token`
and token helper commands. Native Codex parses its explicitly granted auth file.
No API key, alternate paid project, credential extraction or personal session
configuration is supplied. The logical gopod provider remains `codex`; this native
transport ID is fixed host policy, not a group-selected endpoint.

[Official authentication docs](https://learn.chatgpt.com/docs/auth#alternative-model-providers)
explicitly support ChatGPT authentication for custom providers using
`requires_openai_auth = true`.
[Configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference)
documents custom provider IDs and `supports_websockets`.

Pinned source: `rust-v0.164.0`, peeled commit
`6aaac694803004a63c6f393d153c96d22521a6b9`:

- [Native API provider resolution](https://github.com/openai/codex/blob/6aaac694803004a63c6f393d153c96d22521a6b9/codex-rs/model-provider-info/src/lib.rs#L429-L449)
  selects its normal `https://chatgpt.com/backend-api/codex` default when the
  authentication mode is ChatGPT and no explicit base URL is configured.
- [Native authentication path](https://github.com/openai/codex/blob/6aaac694803004a63c6f393d153c96d22521a6b9/codex-rs/model-provider/src/provider.rs#L335-L341)
  uses first-party auth when `requires_openai_auth` is true and alternate auth
  mechanisms are absent.
- [WebSocket capability check](https://github.com/openai/codex/blob/6aaac694803004a63c6f393d153c96d22521a6b9/codex-rs/core/src/client.rs#L1143-L1154)
  respects the provider's `supports_websockets` field.
- [Fallback branch](https://github.com/openai/codex/blob/6aaac694803004a63c6f393d153c96d22521a6b9/codex-rs/core/src/client.rs#L2083-L2091)
  switches to HTTP for a WebSocket handshake response of HTTP 426. Proxy refusal
  may retain that defense, but this managed provider avoids WebSocket attempts.

The built-in ID `openai` is reserved: a pinned native test rejected
`[model_providers.openai]` with a configuration error. Removed WebSocket feature
flags are not a supported transport control and are not used here.

## Reproducible synthetic qualification

Preload trusted `node:22-slim` and the pinned group Codex image, then run:

```sh
python3 experiments/subscription-isolation/codex_sse_auth_probe.py \
  --cli-image gopod-agent-group-codex:latest
```

The script verifies CLI 0.164.0, creates a disposable Docker **internal** network,
starts a local mock backend and constructs a fresh synthetic ChatGPT auth file.
The fixture overrides base URLs to that mock only. No real account file is read,
copied or used; no container has an external network route. Every image must
already exist (`--pull=never`). Containers and network are removed in `finally`.

Observed result with the pinned Codex image:

- Native exit code 0; one HTTP/SSE `/backend-api/codex/responses` request.
- Native authorization and ChatGPT account headers present; values never logged.
- Zero WebSocket attempts; no API key supplied.
- Account-check, user-settings and analytics paths were explicitly denied by the
  mock (403); inference still completed. Production does not grant those routes.
- Normal inference request keys: `client_metadata`, `include`, `input`, `model`,
  `parallel_tool_calls`, `prompt_cache_key`, `reasoning`, `store`, `stream`,
  `tool_choice`, `tools`.

Separate production-wrapper Go qualification uses synthetic local model/MCP
servers and proves explicit new/resume sessions, the two scoped tools and
rejection of provider-requested native Bash/exec_command for both pinned CLIs.
The group request envelope must allow the normal `client_metadata` field while
continuing to reject hosted tools/connectors and remote account/history references.

These tests prove native transport/auth-path compatibility with synthetic input.
They do not prove actual account entitlement, remaining quota, live model behavior,
MITM CA trust, production route grants or complete group isolation. Those remain
separate controlled runtime acceptance checks. Missing auth or failed transport
must remain unavailable rather than selecting a personal runtime or API key.

## Local management and automatic continuation

The pinned Codex source (`rust-v0.164.0`, commit
`6aaac694803004a63c6f393d153c96d22521a6b9`) dispatches `login status` directly
to `run_login_status` in `codex-rs/cli/src/login.rs`. For ChatGPT auth it loads
local configuration/auth and reports the auth mode; it does not construct a
model, session, tool or MCP client. `--strict-config` is explicitly unsupported
for this subcommand, so the group wrapper uses its fixed read-only group config,
synthetic empty cwd and group-only auth HOME without inventing flags. Claude
`auth status --json` receives the same immutable restricted/settings/MCP policy
as inference. The opt-in `TestGroupNativeAuthStatusOfflineDocker` exercises both
with synthetic credentials under `--network=none`; no account is contacted.

Codex's stable `goals` feature defaults to enabled and includes persisted goals
and automatic continuation (`codex-rs/features/src/lib.rs`, `Feature::Goals`).
The group profile explicitly disables it; the pinned new/resume inventory must
not advertise `get_goal`, `create_goal` or `update_goal`. These tools are not
added to the proxy allowlist. See the official
[feature configuration](https://learn.chatgpt.com/docs/config-file/config-basic).

Known authentication patterns in group text/captions are rejected before raw
history or command dispatch and again by local memory admission. This is a
bounded known-secret guard, not a universal secret/PII classifier or attachment
content scanner. Publicly submitted attachment contents retain their existing
shared-workspace semantics.
