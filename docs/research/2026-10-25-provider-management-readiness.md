# Provider management readiness — official interface research

Date: 2026-10-25. Research only. No authenticated provider requests, model
requests, credential extraction or conversation mutations were performed for
this research.

## Decision supported by the evidence

The management layer can inspect supported CLI/account interfaces without
sending a model prompt. That inspection must retain separate dimensions for
runtime readiness, authentication metadata, quota observations, model catalog
visibility and actual inference success. One successful management request does
not prove all of them.

The initial startup implementation being developed alongside this note is
scoped to owner-runtime warmup and a Codex account/limits probe, without
`model/list`. This note is not verification that those changes shipped. Catalog
queries, periodic caching, manual refresh and passive Claude quota capture below
are recommendations or later work, not implemented-feature claims.

## Source and version verification

Official OpenAI documentation was searched and fetched first using OpenAI Docs;
current Claude documentation was retrieved through the read-docs workflow.
Only official pages support the interface claims below. Local verification then
established the installed binaries:

```text
codex --version  -> codex-cli 0.164.0
claude --version -> 2.1.310 (Claude Code)
```

Local `claude auth status --help` confirms `--json` (default) and `--text`.
No real `auth status` command was run during the research.

The exact installed Codex binary generated its JSON schema offline with
`codex app-server generate-json-schema --out <temporary-directory>`, with a new
empty temporary `CODEX_HOME`. Generation exited 0; temporary files were removed.
No authenticated app-server process was started. This version-specific schema
is more precise than examples in the rolling documentation. The ordinary CLI
help/version commands and schema generation do not establish remote account
availability.

## Codex subscription authentication

The [official App Server documentation](https://learn.chatgpt.com/docs/app-server)
documents a JSONL stdio protocol with initialization, account inspection,
ChatGPT rate-limit reads and model catalog discovery. A management-only client
needs no thread or turn. The minimal relevant requests are:

```jsonl
{"id":1,"method":"initialize","params":{"clientInfo":{"name":"gopod_diagnostics","version":"1"}}}
{"method":"initialized","params":{}}
{"id":2,"method":"account/read","params":{"refreshToken":false}}
{"id":3,"method":"account/rateLimits/read"}
```

Wait for successful initialization before sending the acknowledgment and later
requests. Match responses by request ID, tolerate unrelated notifications and
bound protocol line/output sizes. Never invoke login/logout, thread/turn
creation, credit redemption or workspace-owner notification methods from this
probe. `refreshToken:false` avoids explicitly requesting proactive token
refresh; normal CLI authentication/cache handling may still occur. This is a
no-inference/no-conversation-mutation contract, not a promise that starting the
CLI has no incidental local cache writes.

Documented quota observations include usage percentages, window lengths,
Unix-second reset timestamps and separate limit buckets. Preserve bucket
identity and nullable fields instead of flattening all quota into one Boolean.

### Exact 0.164.0 schema findings

The generated `GetAccountRateLimitsResponse` has nullable
`ordinaryUsageAllowed`. Its description says:

> clients must not infer recovery from percentages or reset times.

It also includes `accountId`, `rateLimits`, nullable
`rateLimitsByLimitId`, reset-credit metadata and other optional fields.
`RateLimitSnapshot` has nullable `spendControlReached`, primary/secondary
windows, credits, plan information and limit identifiers.
`RateLimitWindow.usedPercent` is required; `resetsAt` and
`windowDurationMins` are nullable.

Consequences for a consumer:

- Preserve an explicit `ordinaryUsageAllowed:false` as reported, even if a
  displayed percentage or reset timestamp might appear permissive.
- Treat null/missing `ordinaryUsageAllowed` and `spendControlReached` as unknown,
  not recovered or unrestricted.
- An elapsed reset is a reason to refresh an observation, not proof of recovery.
- Keep quota-reading failures distinct from exhausted quota. A 429 on a
  management endpoint can rate-limit the observation itself.
- Do not map `requiresOpenaiAuth:false` to a validated remote account: it
  describes the active provider's authentication requirement.

Account details can contain identifiers or email. Publish only the minimum safe
status; do not log raw JSON or token-bearing errors.

### Model discovery is a separate optional probe

The same official documentation supports:

```jsonl
{"id":4,"method":"model/list","params":{"limit":100,"includeHidden":false}}
```

The exact installed schema confirms `cursor`, `includeHidden` and `limit`.
Follow `nextCursor` within a bounded budget when using it. A catalog entry is
catalog evidence, not a successful inference request. The initial startup
probe does not include model discovery; it must not claim that a selected model
was validated by catalog lookup.

## Claude subscription authentication

[Claude's CLI reference](https://code.claude.com/docs/en/cli-reference) documents
`claude auth status` as JSON output with exit 0 when logged in and 1 otherwise.
The output includes an auth method; local help confirms the JSON option in
2.1.310. A management wrapper can sanitize that result without parsing a
credentials file itself. Configured authentication does not guarantee that a
remote inference request will succeed.

No supported headless JSON subscription-quota command was established by the
retrieved official documentation. Do not invent `claude usage --json` or assume
that `claude -p "/usage"` is a management-only request.

The official [`/usage` guide](https://code.claude.com/docs/en/costs#using-the-usage-command)
describes an interactive usage display and last-known bars when the usage
endpoint cannot be fetched. Its observation endpoint being rate limited does
not mean inference quota itself is exhausted. There is no documented automation
contract here for scraping terminal output into startup readiness.

Official [status-line data](https://code.claude.com/docs/en/statusline#rate-limit-usage)
includes `rate_limits.five_hour` and `seven_day`, with `used_percentage` and
Unix-second `resets_at`. Those fields appear only after the first API response
in the session; windows can independently be absent. This supports future
passive capture during already-authorized real work, not an active zero-cost
cold-start quota probe.

Therefore the initial safe Claude cold-start result is authentication metadata
plus `quota: unknown` when no trusted recent observation exists. Do not send a
dummy model request to fill that gap. Do not extract OAuth tokens or scrape the
undocumented `/api/oauth/usage` endpoint. Passive status-line capture and a
cross-session observation cache remain deferred work, not current behavior.

## API-key cases

Subscription quota interfaces are not general API-key balance interfaces.
For Codex API-key authentication, distinguish `subscription_quota:
not_applicable` from unknown API capacity. A failed ChatGPT quota read is not
evidence of exhausted API credit.

The OpenAI [Models API](https://developers.openai.com/api/reference/resources/models/methods/retrieve)
provides model metadata without generation. The documented
[rate-limit headers](https://developers.openai.com/api/docs/guides/rate-limits)
provide rate-limit observations on API responses. Neither implies that a
selected inference request is guaranteed to succeed.

Anthropic's [Models API](https://platform.claude.com/docs/en/api/models/list)
provides non-generation model catalog access. Its
[Rate Limits API](https://platform.claude.com/docs/en/manage-claude/rate-limits-api)
reads configured organization/workspace ceilings, not current remaining quota.
It needs suitable [Admin API authorization](https://platform.claude.com/docs/en/manage-claude/admin-api#authentication);
an ordinary workspace inference key must not be presumed sufficient. Do not
expand credential privileges merely to power diagnostics. These administrative
APIs are optional future integrations, not initial startup requirements.

Current remaining-token/reset observations may be captured from headers of
already-authorized real API work as described in Anthropic's
[rate-limit documentation](https://platform.claude.com/docs/en/api/rate-limits).
A configured ceiling, historical consumption report and live remaining quota
are different measurements and must not be substituted for one another.

## Recommended observation and lifecycle contract

The following are design recommendations, not implemented features:

```text
runtime_state
credential_scope / auth_state
catalog_state / selected_model_listed
quota_state / windows / ordinary_usage_allowed
checked_at / last_success_at / freshness
source / sanitized_error_class
inference_state
```

Use a service-owned, single-flight probe per effective credential/account scope,
not per Telegram topic. Keep management work out of the conversation turn
queue so a slow observation cannot starve foreground work. Suggested initial
budgets are five seconds for local CLI/handshake and ten seconds total for
external observation. These are local policy choices, not provider guarantees.
Terminate and join the diagnostic process on deadline/shutdown, including
closing protocol transport; bounded output capture and safe error classes must
prevent secrets or arbitrary terminal text from entering reports.

For a future refresh service, consider a five-minute cache, thirty-second manual
cooldown and bounded failure backoff. Invalidate prior observations immediately
on credential/account changes, and invalidate catalog association on
provider/model changes. A startup-only implementation must honestly report its
observation age; it does not provide periodic recovery monitoring.

Show separate facts such as “authentication configured” and “quota last
observed,” retaining an explicit “inference availability unverified” state.
Model membership plus unexhausted quota is still not a successful selected-model
inference. Only actual authorized work provides that final signal.

## Verification needed for implementation

- Exact-schema fixtures for account and quota responses, null/missing fields,
  multiple buckets, exhausted quotas and independent management-endpoint errors.
- No thread/turn/login/logout/credit/email methods in the allowed request set;
  no model invocation in either provider probe.
- Deterministic initialization and response-ID matching; bounded lines/output,
  timeout, cancellation, process cleanup and shutdown joining.
- Last-success retention and credential-scope invalidation; an elapsed reset
  cannot turn denied/unknown into available without new authoritative evidence.
- Safe reports/logs for identifiers, malformed JSON and token-bearing CLI errors.
- Claude auth JSON can be parsed, but missing subscription quota remains unknown;
  no `-p /usage`, OAuth extraction or undocumented usage-endpoint fallback.
- Real startup warmup/management checks must record their own runtime evidence.
  Offline schema verification in this research proves interface shape only.
