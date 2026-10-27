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

## Responses Lite model metadata and explicit tool declarations

Pinned source qualification on 2026-10-27 found a separate wire-format boundary:
actual model-catalog metadata can set `ModelInfo.use_responses_lite = true`
(`codex-rs/protocol/src/openai_models.rs`). A synthetic `gpt-5` fallback without
catalog metadata does not exercise this branch. CLI version/auth compatibility
alone therefore does not qualify every selected model's request envelope.

At the same pinned commit `6aaac694803004a63c6f393d153c96d22521a6b9`:

- `core/src/client.rs:1015-1041` always omits top-level `tools` for Responses
  Lite. It places full tool declarations in the **first** input item, before
  base instructions and conversation history.
- `protocol/src/models.rs:1053` defines that item as
  `{"type":"additional_tools","id":"at_<UUID>","role":"developer","tools":[...]}`;
  `id` is optional in the protocol, but native client code generates it.
- `tools/src/tool_spec.rs:95` preserves MCP namespaces and places ordinary
  builtin functions under a `functions` namespace. This grouping is a wire
  format, not a new permission or a grant for arbitrary function names.
- `core/src/session/step_context.rs:52` enables incremental declarations when
  both Responses Lite and stable `features.incremental_tools` are enabled.
  `session/turn.rs:1681` then empties the prompt tool list and
  `session/world_state.rs:168` carries incremental declarations in world state.
  The group profile explicitly sets `incremental_tools = false`. This makes
  the ordinary client prefix contain the full current tool inventory on every
  request, including resumed turns; it **does not disable Responses Lite**.
- `session/startup_prewarm.rs:243` returns after auth-only prewarm when WebSockets
  are disabled. A missing-tool Responses Lite request should not be presumed
  to be a harmless auxiliary/prewarm request.

A temporary offline Go overlay with a synthetic explicit catalog model
(`use_responses_lite=true`, `supports_search_tool=false`) confirmed native
new/resume tool use and hostile native-tool denial. Every model request omitted
its top-level tools and contained exactly one full `additional_tools` item.
No real account/model was used. The prior synthetic-schema overlay also passed
both native CLIs with the exact production `group_request` generic-object schema;
that schema does not explain an absent inventory.

The proxy must independently validate any supported `additional_tools` wrapper,
its placement and keys, and every declaration against the same exact allowed
and required identities. It must not interpret arbitrary nested data as a grant
or accept incremental world-state declarations through this compatibility path.

No supported per-MCP eager-loading switch was found in the pinned
`config/src/mcp_types.rs`: tool settings contain approval mode/output budget,
while server settings contain enabled/disabled tool lists and exposure omission,
not a direct/deferred override. `features/src/lib.rs:273-276` explicitly marks
`tool_search` and `tool_search_always_defer_mcp_tools` removed compatibility
flags. `core/src/mcp_tool_exposure.rs:158` defers MCP tools whenever model tool
search is available. Do not invent a flag or weaken required-tool checks. Models
with tool-search metadata require separate qualification of a strictly scoped
search implementation, or remain unavailable. `model_catalog_json` is a real
configuration option, but rewriting actual model metadata to pretend that the
model uses another format is not the chosen compatibility mechanism.

### Qualified client tool-search behavior (offline)

A synthetic explicit catalog with `supports_search_tool=true` reproduces the
next native envelope: `additional_tools.tools` initially contains safe builtin
functions and the descriptor below, **without** deferred MCP declarations:

```json
{"type":"tool_search","execution":"client","description":"<native-generated description>","parameters":{"type":"object","properties":{"limit":{"type":"number","description":"Maximum number of tools to return. Defaults to 8."},"query":{"type":"string","description":"Search query for deferred tools."}},"required":["query"],"additionalProperties":false}}
```

`core/src/tools/handlers/tool_search_spec.rs` builds this fixed client descriptor;
`tools/src/tool_spec.rs` serializes it. `ToolSearchHandlerCache::get_or_build` in
`core/src/tools/handlers/tool_search.rs` collects **only existing deferred registry
entries**. Search uses local BM25 (`bm25::Embedder`/Scorer), not an external
embedding/model endpoint. `ToolSearchHandler::search` returns catalog entries as
loadable definitions. `core/src/tools/router.rs:275` executes a search call only
when `execution == "client"`; it does not execute provider-hosted search.
Unregistered function calls fail in the registry dispatch path instead of
creating new runtime bindings (`core/src/tools/registry.rs`).

The persisted native fixture now covers search → scoped MCP execution on both
new and resumed sessions, validates every returned search declaration belongs
to `mcp__group.group_exec` or `mcp__group.group_request`, and proves a malicious
provider-requested `exec_command` cannot execute with search enabled. All eight
normal/RLite/search/hostile scenarios passed with pinned local CLIs, synthetic
catalogs and local mock endpoints; no account or live model was used.

A compatibility policy must distinguish declaration availability from registry
authority: before initial discovery the scoped pair is established by the
immutable strict MCP server configuration and its exact `enabled_tools`, not by
a visible request declaration. An explicit client-search mode can allow only the
pinned descriptor and audited builtin functions, with recursive identity checks
on `tool_search_output.tools` and rejection of server execution, foreign
namespaces and unknown declarations. Merely treating a `tool_search` descriptor
as if it declared both scoped functions would misrepresent the wire evidence.
These source/fixture results do not authorize a generic hosted-search grant or
prove live transport/account acceptance.

## Group native policy v2: mandatory sanitized static catalog

A source audit found that `features.shell_tool=false` does **not** prevent native
`apply_patch` registration. Pinned `core/src/tools/spec_plan.rs:1409` registers
`ApplyPatchHandler` whenever `model_info.apply_patch_tool_type` is present,
independently of shell features. Its default exposure is direct. Code Mode Only
can hide this declaration while advertising native `exec`/`wait` wrappers;
those wrappers remain forbidden by the proxy. The old minimal synthetic model
catalog had omitted these authority-bearing fields and missed the registration.
An offline unsafe-catalog regression reproduced the unexpected declaration.
A synthetic deletion attempt did not demonstrate auth-content disclosure; no
real account file was accessed. Read-only execution alone is not a proof that
no native filesystem reads can occur.

Policy v2 therefore requires controller-sanitized catalog bytes and an exact
admitted model for every Codex runtime. The provider rejects missing, uncapped
or model-mismatched catalogs, emits a fixed read-only
`/etc/gopod-group/models.json`, and references it through the supported
`model_catalog_json` setting. Each new, resumed and fresh command explicitly pins
`--model`; later runner model arguments cannot replace that admission decision.
The host rejects unknown slugs before invoking the native CLI. It does not use
native prefix matching or unknown-model fallback to widen the selection.

The sanitizer reduces metadata capabilities while retaining model identity,
context limits, reasoning, Responses Lite and qualified client-search metadata:

- `apply_patch_tool_type=null`, `tool_mode="direct"`, `shell_type="disabled"`;
- `experimental_supported_tools=[]`, `multi_agent_version="disabled"`;
- `node_repl_disabled=true`, `auto_review_model_override=null`;
- source skills/plugins/apps instruction toggles disabled;
- fixed neutral shared-group `base_instructions`, with `model_messages=null`.

The pinned legacy-base adapter (`protocol/src/openai_models.rs:917`) promotes the
fixed base into a fresh instruction template when `model_messages` is null.
Source/private instruction overrides are not inherited. Native configuration
also sets `[agents] enabled=false` and explicitly disables `sleep_tool`,
`code_mode` and `code_mode_only`: false multi-agent feature flags alone do not
prevent model metadata from selecting agent V2. Unknown source fields are
removed; the already-sanitized format is validated strictly.

Fourteen pinned offline native scenarios pass after sanitizing a deliberately
unsafe source model (freeform patch, Code Mode Only, agent V2 and experimental
clock/test/async tools). They cover ordinary/Responses Lite/client-search
inventories, new and resumed sessions, provider-requested exec/read calls and
custom patch attempts to delete a synthetic auth canary or create a native file.
The canary remains unchanged and absent from provider request context. Only the
scoped MCP pair and audited local control tools are advertised; no native
filesystem authority is added to the proxy allowlist.

### Public deployment catalog source and default selection

The versioned primary source is the Codex repository's
[`models-manager/models.json` at commit 6aaac694](https://github.com/openai/codex/blob/6aaac694803004a63c6f393d153c96d22521a6b9/codex-rs/models-manager/models.json).
It requires no account or credential export. Its source SHA256 is
`943ca7fe1d19ed019054158303f0dbbd80b3bef43b3aa425a71aa7cb3fb52c2b`.
The catalog preparer successfully sanitizes all 11 models. The actual pinned
Codex CLI loads the sanitized public catalog successfully in a fresh auth-less
HOME (`login status` reaches the expected not-logged-in result, not a catalog
parse error); no inference/network request is made by this check.

| Slug | Priority | Picker visibility |
| --- | ---: | --- |
| gpt-6.1-sol | 1 | list |
| gpt-6-astra | 2 | list |
| gpt-6-sol | 3 | list |
| gpt-6-luna | 4 | list |
| gpt-5.6-sol | 5 | list |
| gpt-5.6-terra | 8 | list |
| gpt-5.6-luna | 9 | list |
| gpt-daybreak-blue-latest | 11 | hide |
| gpt-daybreak-red-latest | 12 | hide |
| gpt-5.5 | 13 | list |
| codex-auto-review | 43 | hide |

Pinned `ModelsManager::build_available_models` sorts by ascending priority;
`ModelPreset::mark_default_by_picker_visibility` chooses the first visible model,
falling back to the first only if none are visible. Thus the **public pinned
snapshot's** default candidate is `gpt-6.1-sol`. This is not proof of the current
owner account's remote catalog, entitlement or selected/default model. The
catalog preparer and group manager require an explicit default; importing this
snapshot does not authorize silently changing an existing selection.

### Guardian and model-driven registry audit

`ModelInfo.guardian` is approval/classification coverage, not a tool registration
selector. Nevertheless pinned `config/src/guardian.rs` explicitly lets a non-null
catalog policy bypass the legacy `guardianv2=false` scoring gate. An adaptive
policy can prepare classifier state. Current default user review prevents its
network prewarm/scoring, but policy v2 does not depend on that default: sanitized
`guardian=null`, fixed `approvals_reviewer="user"`, and supported
`features.guardian_approval=false`/`features.guardianv2=false` close this path.
The Guardian extension's thread-start handler exits when GuardianApproval is off.
`guardian_source` is not a ModelInfo field in this pin and is dropped as unknown.

The complete model-driven registry branches in `core/src/tools/spec_plan.rs`
are covered by the following controls: shell type plus ShellTool feature;
apply-patch metadata removed; tool mode forced direct; multi-agent metadata
disabled plus agents.enabled=false; experimental clock/test/async tool names
removed; image modalities cannot enable image tools while their features are
off; search and Responses Lite retain their separately audited scoped behavior.
`node_repl_disabled=true` reduces extension availability;
`node_repl_auto_review_required` is review metadata, not a registry constructor.
The hostile source fixture sets adaptive Guardian coverage, node review flags
and a private reviewer-model override as well as native patch/code/agent tools;
the sanitized native runtime retains only the qualified scoped inventory.

The regenerated sanitized public artifact SHA256 is
`7718d30f843b20ae77c5209208ac77eff668bee5afefc02d9ae65b3934ee0091`.

### All-public-model native qualification

The sanitized public artifact's entire 11-model set was also exercised through
actual Codex 0.164.0 against local mocks: 55 cases passed in the initial read-only
qualification, including hidden daybreak aliases and `codex-auto-review`.
No retained model-specific metadata enabled an unexpected native tool or changed
the admitted outbound model. Every model covered scoped MCP execution and
hostile exec/patch-delete/patch-write/read-file attempts on both new and resumed
sessions. The synthetic auth canary remained unchanged and absent from provider
context; native bypass files were not created. Responses Lite and ordinary
Responses both audit client-search discoveries against the same scoped pair.

This qualification is now permanent and opt-in as
`TestGroupNativeInventoryPublicCatalog`, reusing the original fixture helper.
The CI native-inventory name filter includes its 55 cases plus the 14 hostile
synthetic-metadata cases. No account credential, Telegram or live model request
is part of the qualification. Passing metadata/CLI checks does not prove backend
entitlement, quota or live inference availability for any model.
