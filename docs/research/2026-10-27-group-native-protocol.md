# Group native protocol interoperability

Date: 2026-10-27. Scope: CP-020 trusted group inference relay only.
Status: protocol investigation and local regression evidence; this note does
not certify successful exact-Pi model inference, deployment, or Telegram UAT.

## Claude optional diagnostics

An isolated native qualification probe reached the group relay but received its
local deny response. A temporary diagnostic overlay identified the additional
top-level field as `diagnostics`; no credential or message payload is retained
in this note. Auth-only success had not proved inference-envelope compatibility.

The official [Messages API reference](https://platform.claude.com/docs/en/api/messages/create)
(accessed 2026-10-27) documents an optional diagnostics object containing
`previous_message_id`, a nullable string up to 256 characters. It compares the
previous request's prompt fingerprint with the current request and can report
prompt-cache divergence. It is optional generation diagnostics, not required
conversation history.

Group policy accepts only null or an object with that single optional nullable
string (implementation conservatively bounds UTF-8 bytes to 256), then **strips
the entire diagnostics field before forwarding**. Unknown keys, other types,
excess length, or the field on Responses are denied. This prevents forwarding
an account-scoped comparison identifier or introducing a cache-metadata oracle.
Normal generation fields remain subject to the existing policy. Personal native
execution does not use this group proxy and is unchanged.

[Implementation](../../internal/isolationproxy/diagnostics.go) and
[upstream sanitization/schema tests](../../internal/isolationproxy/diagnostics_test.go)
cover removal before transport, generation-envelope preservation, and invalid
schemas. No raw auth output, request bodies, or comparison identifiers are logged.

## Codex Responses Lite and local discovery

The [pinned SSE and native protocol investigation](2026-10-26-codex-group-native-sse.md#responses-lite-model-metadata-and-explicit-tool-declarations)
records source qualification for Codex 0.164.0 at commit
`6aaac694803004a63c6f393d153c96d22521a6b9`, including exact source locations,
Responses Lite and client tool-search behavior. The full inventory can live in
one initial developer `additional_tools` item rather than top-level `tools`.
Only the reviewed wrapper shape, placement and tool identities are accepted;
remote context and provider-hosted tools remain excluded. Incremental tool
world-state is disabled for the group profile.

Catalog models supporting search can defer MCP declarations. Pinned client
search uses its existing local registry and BM25; it does not create arbitrary
runtime bindings. Offline new/resume fixtures cover discovery, scoped MCP calls,
and denial of provider-requested native execution. Client-search compatibility
requires its explicit reviewed mode, exact client descriptor, scoped immutable
MCP configuration, and validation of returned tool declarations. These offline
fixtures are separate from exact-device live model canaries.

## MCP concurrency and safe observability

Native startup can concurrently initialize, list tools and ping. The scoped MCP
endpoint now serializes these RPCs instead of rejecting ordinary discovery
contention. Waiters do not hold the registry lock; lease revocation cancels
waiting requests, closes admission, and retains unsettled handlers in the drain
registry until their actual exit. Race regressions cover discovery settlement,
revoked/canceled waiters and timed-out drains.

Relay diagnostics emit fixed classes only: CONNECT authentication/authority/host
or lease denial; route/body validation classes; tools presence, known inventory
and deferred-loading booleans; forwarding, network failure and bounded upstream
status classes. They never emit supplied host/path/header/body/key/tool names or
raw errors. This distinguishes local policy rejection from upstream failure
without collecting private request data. Successful auth, discovery, and local
regressions must not be reported as successful live inference or a completed
production release.
