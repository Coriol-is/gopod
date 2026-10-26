# Group inference egress capability

CP-020 foundation, 2026-10-26. This package does not enable a group profile,
provision credentials or contact a real provider during its tests.

`New(Config)` returns an `http.Handler` that only accepts authenticated HTTPS
CONNECT for explicit DNS host/443 grants. It terminates TLS with a per-lease CA,
parses the inner HTTP request, and forwards only an exact host/method/path/query
route. It never provides a raw TCP tunnel. Each tunnel handles one request and
closes; HTTP/2, WebSockets, upgrades and redirects are unavailable. An authenticated
WebSocket handshake on an exact granted Responses inference route receives 426
without upstream forwarding, allowing the pinned Codex native HTTPS fallback. Streaming
HTTPS/SSE is supported, with request/response/duration/connection/header bounds.
An interrupted client or `Proxy.Close()` cancels upstream work.

The native CLI uses `http://group:<opaque-token>@relay:port` as HTTPS proxy and
trusts `CertificatePEM()` via its managed CA setting. The private CA key belongs
only to the controller/relay, never to native CLI or tool workspaces.
`GenerateCA()` permits controller-owned 0600 per-lease files; optional supplied
CA fields let a relay restart without changing its mounted public trust anchor.
CA validity is seven days; a lease must rotate before expiry, never weaken TLS.

The caller must configure an HTTP server with bounded outer header parsing,
header timeout and safe errors; `Proxy.Close()` must accompany shutdown because
Go HTTP server shutdown does not own hijacked connections. The proxy itself has
no logging of headers, bodies, upstream errors, credentials or response text.

## Enforcement outside environment variables

Setting proxy environment variables alone is not enforcement. The CLI container
must have only the isolated internal network, no host gateway, general host
ports or external default route. A separate relay attaches to that internal
network plus the external network. It exposes only this proxy and a separately
capability-bound fixed MCP reverse route. The relay has no personal workspace,
CLI credentials, Docker socket or account integrations. Tools execute elsewhere.
An inaccessible relay or invalid/missing policy must make group execution fail.

The upstream transport ignores process proxy variables. Every DNS answer must
be a public address; private, loopback, metadata, link-local, shared, transition
and special-use ranges are rejected. The transport connects to the validated IP
while retaining original-host TLS verification. DNS results cannot change between
validation and connection. No arbitrary target port or hostname wildcard exists.

## Request authority

`Routes` are trusted explicit grants, not provider-name defaults. No auth-refresh,
login, account, models or metadata route is inferred. Routes remain denied until
qualified for the pinned CLI. ExactJSON supports a reviewed refresh envelope with
an explicit top-level field set, but no refresh route is installed automatically.
Missing/expired credentials are not permission to bypass the proxy or switch billing.
Production readonly-shared native profiles permit only pinned inference and
explicit model-catalog routes, never OAuth/token refresh or account routes.
Refreshing a readonly reference could rotate the owner's refresh token without
persisting its replacement; expiry therefore fails closed until personal native
management refreshes the source. Production `RequireSubscriptionBearer` requires
one Bearer Authorization and rejects API-key headers before upstream forwarding;
provider status and endpoint restrictions remain necessary to establish native
subscription use, since a Bearer scheme alone does not prove an account type.

AnthropicMessages and Responses policies validate duplicate-free bounded JSON,
known inference fields and explicit local tool names. Hosted MCP, connectors,
web search, computer/browser/custom tools, remote account file/history references
and unknown future inference fields are denied. Responses namespace tools must
contain only allowlisted fully qualified local function names. Production must
set `RequiredTools` to both scoped `mcp__group__group_exec` and
`mcp__group__group_request`: every inference request must advertise both tools,
non-deferred and enabled. Flat qualified names and namespace function names map
to the same identity. Missing MCP, deferred tools and `tool_choice=none` deny
inference before upstream authentication is forwarded. Nil RequiredTools remains
a foundation-only mode and must not qualify a production group runtime. Metadata/text is
not host verification or authority to change these grants. A tool schema/body
mismatch fails closed and must be requalified; do not broaden grants from logs.

The precise body/route envelope is part of a runtime policy fingerprint. Current
pinned clients can change these envelopes; this package does not claim live
subscription or account-service qualification. The sole production endpoint
assertion currently sourced is Codex's ChatGPT inference URL:
`https://chatgpt.com/backend-api/codex/responses`. It is not automatically enabled.
No real account or model request was made in this implementation task.

## Evidence and official sources

- OpenAI explains the ChatGPT-authenticated Responses endpoint in
  [Unrolling the Codex agent loop](https://openai.com/index/unrolling-the-codex-agent-loop/).
- Codex's [official network proxy implementation notes](https://github.com/openai/codex/blob/main/codex-rs/network-proxy/README.md)
  distinguish CONNECT tunneling from TLS termination needed for method/path policy.
- OpenAI's [agent approvals and security](https://learn.chatgpt.com/docs/agent-approvals-security)
  explicitly says command proxy settings do not cover model/auth, apps, MCP or other
  service traffic. Hence enforcement here is a Docker boundary, not a CLI setting.
- Anthropic [server-tool documentation](https://platform.claude.com/docs/en/agents-and-tools/tool-use/server-tools)
  shows server tools run remotely. Filtering local shell tools alone is insufficient.

Sources inspected 2026-10-26. Offline tests use a local TLS proxy and synthetic
upstream RoundTripper, exercising allowed inference, request policy, capability,
DNS/address classification, headers/JSON/size limits, cancellation, CA reuse and
close cleanup. They do not prove Docker topology or authenticated native CLI
compatibility. Those are integration/release gates owned by the Runner rollout.
