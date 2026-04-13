# gopod — Secret Gateway

> Companion docs: [ARCHITECTURE.md](ARCHITECTURE.md) · [ISOLATION.md](ISOLATION.md) · [DECISIONS.md](DECISIONS.md)
> Status & next steps: [HANDOFF.md](HANDOFF.md) · [../ROADMAP.md](../ROADMAP.md)
> Reference implementation: `/Users/<user>/_code/gh-public/onecli`

An HTTP CONNECT proxy embedded in gopod that intercepts agent HTTPS
requests, resolves credentials from HashiCorp Vault, and injects them
into outbound headers. Agents never see real keys.

Supersedes [D008](DECISIONS.md) ("No OneCLI; secrets via env vars").

---

## 1. Problem

Today gopod injects API keys (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`,
etc.) as environment variables into each agent container via Docker's
`Env` field ([D012](DECISIONS.md)). This works for a single user but
has weaknesses:

1. **Secrets in container memory.** Any process inside the container
   can read `/proc/1/environ` and exfiltrate every injected key.
2. **No per-service scoping.** An agent that only needs Anthropic
   access also has the OpenAI key, Slack token, etc.
3. **No rotation without restart.** Changing a key means restarting
   the container (and losing session state).
4. **No audit trail.** gopod has no record of which key was used
   when, by which agent.
5. **No external vault.** Secrets live in `.env` on disk or in
   systemd `EnvironmentFile`. No integration with a secret manager.

OneCLI (`/Users/<user>/_code/gh-public/onecli`) solves this
with a Rust+Next.js MITM proxy backed by PostgreSQL and Bitwarden.
gopod adopts the same architecture, simplified for Go + Vault.

---

## 2. Architecture overview

```
┌──────────────────────────────────────┐
│          gopod (Go, host)            │
│                                      │
│  ┌──────────────────────────────┐    │
│  │  internal/gateway            │    │
│  │  HTTP CONNECT proxy          │    │
│  │  :10255 (localhost or 0.0.0.0)│   │
│  │                              │    │
│  │  ┌─────────┐  ┌──────────┐  │    │
│  │  │  MITM   │  │  inject  │  │    │
│  │  │  TLS    │→ │  headers │  │    │
│  │  └─────────┘  └──────────┘  │    │
│  │       ↑            ↑        │    │
│  │  ┌─��───────┐  ┌──────────┐  │    │
│  │  │   ca    │  │  policy  │  │    │
│  │  │  certs  │  │  engine  │  │    │
│  │  └─────────┘  └──────────┘  │    │
│  └──────────────────────────────┘    │
│                  ↕                   │
│  ┌──────────────────────────────┐    │
│  │  internal/vault              │    ���
│  │  HashiCorp Vault client      │    │
│  │  KV v2 + Transit (encrypt)   │    │
│  └──────────────────────────────┘    │
└──────────────────────────────────────┘
         ↑                    ↑
    Agent containers     HashiCorp Vault
    (HTTP_PROXY →        (external, any
     gateway)             deployment)
```

### Data flow (happy path)

```
1. Agent container makes HTTPS request:
   curl -x http://x:${AGENT_TOKEN}@host.docker.internal:10255 \
     https://api.anthropic.com/v1/messages

2. Gateway extracts AGENT_TOKEN from Proxy-Authorization header

3. Resolve policy:
   a. Look up agent token → (chat_folder, provider, scoped_paths)
   b. Query Vault KV: secret/data/gopod/<chat_folder>/anthropic
      or fallback: secret/data/gopod/shared/anthropic
   c. Build injection rules: [{host: "api.anthropic.com",
      header: "x-api-key", value: <decrypted>}]

4. MITM: terminate agent TLS, generate leaf cert signed by gopod CA

5. Inject: set x-api-key header on the decrypted request

6. Forward to api.anthropic.com over fresh TLS

7. Stream response back to agent (no modification)
```

---

## 3. Components

### 3.1 `internal/gateway` — HTTP CONNECT proxy

**Responsibilities:**
- Listen on `GOPOD_GATEWAY_ADDR` (default `127.0.0.1:10255`)
- Accept HTTP/1.1 CONNECT requests
- Authenticate agent via Proxy-Authorization Basic header
- Decide: MITM (if injection rules exist) or plain tunnel (passthrough)
- In MITM mode: terminate TLS, intercept HTTP requests, apply
  injection rules, forward upstream
- Log every proxied request (host, path, agent, injected=yes/no)

**Key types:**

```go
// Gateway is the HTTP CONNECT proxy server.
type Gateway struct {
    ln       net.Listener
    ca       *CA
    vault    vault.Client
    policy   *PolicyEngine
    log      *slog.Logger
}

// InjectionRule describes a header to inject for a host+path match.
type InjectionRule struct {
    HostPattern string // "api.anthropic.com" or "*.openai.com"
    PathPattern string // "/v1/*" or "*"
    Action      Action // SetHeader | ReplaceHeader | RemoveHeader
    HeaderName  string // "x-api-key", "Authorization"
    ValueFormat string // "%s" or "Bearer %s"
}

type Action int
const (
    SetHeader     Action = iota // add/overwrite header
    ReplaceHeader               // overwrite only if present
    RemoveHeader                // strip header (cleanup)
)
```

**No web dashboard.** gopod is a personal bot — secret management
happens via Telegram commands (`/secrets`) and Vault's own UI/CLI.

### 3.2 `internal/gateway/ca` — Certificate Authority

**Responsibilities:**
- Generate a self-signed root CA on first run (stored in
  `${DATA_DIR}/gateway/ca.crt` + `ca.key`)
- Generate per-host leaf certificates on demand (cached in memory,
  TTL 24h)
- Root CA cert is mounted into agent containers as a trusted cert
  (`/usr/local/share/ca-certificates/gopod-ca.crt` + `update-ca-certificates`)

**Implementation:** `crypto/x509` + `crypto/ecdsa` (P-256). No CGO.

### 3.3 `internal/vault` �� HashiCorp Vault client

**Responsibilities:**
- Connect to Vault at `VAULT_ADDR` with `VAULT_TOKEN` or AppRole
- Read secrets from KV v2 engine
- Optional: use Transit engine to decrypt secrets stored in SQLite
  (hybrid mode — see §6)
- Cache secrets in memory with configurable TTL (default 5 min)
- Invalidate cache on `/secrets` mutations

**Path convention:**

```
secret/data/gopod/
├── shared/              # secrets available to all chats
│   ├── anthropic        # {api_key: "sk-ant-..."}
│   ├── openai           # {api_key: "sk-..."}
│   └── github           # {token: "ghp_..."}
├── <chat_folder>/       # per-chat overrides (take precedence)
│   └── anthropic        # {api_key: "sk-ant-DIFFERENT-..."}
└── _meta/
    └── injection-rules  # host→header mapping config
```

**Secret data format (KV v2 value):**

```json
{
  "api_key": "sk-ant-...",
  "host_pattern": "api.anthropic.com",
  "path_pattern": "/v1/*",
  "header_name": "x-api-key",
  "value_format": "%s"
}
```

### 3.4 `internal/gateway/policy` — Policy engine

**Responsibilities:**
- Resolve (agent_token, hostname) → list of InjectionRules
- Enforce per-chat secret scoping:
  - Chat has access to `shared/*` secrets + its own `<folder>/*`
  - Per-chat overrides shadow shared secrets for the same host
- Rate limiting (optional, per agent+host, token bucket)
- Block rules (deny specific hosts/paths per agent)

**Resolution order:**
1. Per-chat secret for this host → use it
2. Shared secret for this host → use it
3. No secret → plain tunnel (no MITM, no injection)

### 3.5 Agent token generation

Each registered chat gets an agent token (random 32-byte hex,
prefix `gpt_`). Stored in `registered_chats.agent_token` column.
Generated at registration time, rotatable via `/secrets rotate-token`.

Token is passed to the container as the sole secret env var:
`GOPOD_AGENT_TOKEN=gpt_...`. The container's HTTP_PROXY is set to
`http://x:${GOPOD_AGENT_TOKEN}@host.docker.internal:10255`.

**This replaces all other secret env vars.** No more
`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, etc. in the container
environment.

---

## 4. Container changes

### 4.1 Env vars (before → after)

| Before | After |
|--------|-------|
| `ANTHROPIC_API_KEY=sk-ant-...` | (removed) |
| `OPENAI_API_KEY=sk-...` | (removed) |
| (any secret env vars) | (removed) |
| — | `GOPOD_AGENT_TOKEN=gpt_...` |
| — | `HTTP_PROXY=http://x:${GOPOD_AGENT_TOKEN}@host.docker.internal:10255` |
| — | `HTTPS_PROXY=http://x:${GOPOD_AGENT_TOKEN}@host.docker.internal:10255` |
| — | `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/gopod-ca.crt` |

### 4.2 Mount additions

| Source | Target | Mode |
|--------|--------|------|
| `${DATA_DIR}/gateway/ca.crt` | `/usr/local/share/ca-certificates/gopod-ca.crt` | RO |

### 4.3 Dockerfile changes

Add `update-ca-certificates` to the agent image entrypoint (or trust
the cert via `NODE_EXTRA_CA_CERTS` for Node.js / `SSL_CERT_FILE` for
Go-based agents).

---

## 5. Control plane commands

All via `internal/control` Router, per [D011](DECISIONS.md).

| Command | Perm | Description |
|---------|------|-------------|
| `/secrets list` | OwnerOnly | List Vault paths with host patterns (no values) |
| `/secrets add <name> <host>` | OwnerOnly | Interactive: prompts for value, writes to Vault |
| `/secrets remove <name>` | OwnerOnly | Remove from Vault |
| `/secrets rotate-token [chat]` | OwnerOnly | Regenerate agent token for a chat |
| `/secrets test <host>` | OwnerOnly | Test: make a proxied request, report injection result |
| `/secrets status` | OwnerOnly | Vault connection status + cache stats |

---

## 6. Deployment modes

### 6.1 Full Vault (recommended)

External HashiCorp Vault (dev server, Docker, HCP Cloud).
All secrets in Vault KV v2. gopod connects via `VAULT_ADDR` +
`VAULT_TOKEN` (or AppRole credentials).

```
VAULT_ADDR=http://127.0.0.1:8200
VAULT_TOKEN=hvs.xxx
# or AppRole:
VAULT_ROLE_ID=xxx
VAULT_SECRET_ID=xxx
```

### 6.2 SQLite + Transit (lightweight)

Secrets encrypted in gopod's SQLite via Vault Transit engine.
Vault only provides encryption/decryption — no KV storage needed.
Good for setups where Vault is remote and you want local caching.

```
GOPOD_SECRET_BACKEND=transit
VAULT_ADDR=http://vault:8200
VAULT_TRANSIT_KEY=gopod
```

### 6.3 SQLite + local key (dev-only fallback)

Secrets AES-256-GCM encrypted in SQLite with a local key.
No Vault dependency. Acceptable only for development.

```
GOPOD_SECRET_BACKEND=local
GOPOD_SECRET_KEY=base64-encoded-32-byte-key
```

### 6.4 Env var passthrough (backwards compat)

Gateway disabled. Secrets injected as env vars like today.
Activated when `GOPOD_GATEWAY_ADDR` is not set and no Vault is
configured. Existing D008/D012 behavior, no code changes.

---

## 7. Schema additions

```sql
-- Per-chat agent tokens for gateway authentication.
-- Added to existing registered_chats table.
ALTER TABLE registered_chats ADD COLUMN agent_token TEXT;

-- Injection rules (for SQLite-backed modes 6.2/6.3).
CREATE TABLE IF NOT EXISTS secrets (
    id           INTEGER PRIMARY KEY,
    name         TEXT NOT NULL,             -- "anthropic", "openai"
    chat_folder  TEXT,                      -- NULL = shared
    host_pattern TEXT NOT NULL,             -- "api.anthropic.com"
    path_pattern TEXT NOT NULL DEFAULT '*', -- "/v1/*"
    header_name  TEXT NOT NULL,             -- "x-api-key"
    value_format TEXT NOT NULL DEFAULT '%s',-- "Bearer %s"
    encrypted    BLOB NOT NULL,            -- AES-256-GCM or Transit ciphertext
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(name, chat_folder)
);

-- Audit log for proxied requests.
CREATE TABLE IF NOT EXISTS gateway_log (
    id         INTEGER PRIMARY KEY,
    ts         TEXT NOT NULL DEFAULT (datetime('now')),
    chat_folder TEXT NOT NULL,
    host       TEXT NOT NULL,
    path       TEXT NOT NULL,
    method     TEXT NOT NULL,
    secret_name TEXT,          -- NULL if plain tunnel
    status     INTEGER,       -- upstream HTTP status
    latency_ms INTEGER
);
```

---

## 8. AgentProvider changes

The `AgentProvider` interface gets two new methods:

```go
// ProxyEnvVars returns env vars the container needs for proxy setup.
// Called by docker_args.go instead of RequiredEnvVars when gateway is active.
ProxyEnvVars(agentToken string, gatewayAddr string) []string

// UsesProxy reports whether this provider's CLI respects HTTP_PROXY.
// If false, the gateway injects credentials via RequiredEnvVars fallback.
UsesProxy() bool
```

Claude Code and Codex both respect `HTTP_PROXY` / `HTTPS_PROXY`
(they use Node.js `undici` / Go `net/http` which honor these).

---

## 9. Security model

| Property | Guarantee |
|----------|-----------|
| Agent never sees real keys | Keys injected at proxy layer, not in container env |
| Per-service scoping | Injection rules match by host+path; agent only gets keys for services it actually calls |
| Per-chat isolation | Agent token scopes to chat_folder; cross-chat requests get different (or no) keys |
| Rotation without restart | Vault secret update + cache invalidation; no container restart needed |
| Audit trail | `gateway_log` table records every proxied request with secret_name |
| TLS integrity | MITM only for hosts with injection rules; all others pass through as plain tunnel |
| Vault-backed | Real keys live in Vault, not on disk (except dev mode) |

---

## 10. What this is NOT

- **Not a general-purpose proxy.** Only gopod agent containers use it.
- **Not a web dashboard.** Secret management is via Vault UI/CLI +
  Telegram `/secrets` commands.
- **Not multi-tenant.** Single gopod instance, single Vault namespace.
  Per-chat scoping is the isolation boundary, not accounts.
- **Not OneCLI.** No Bitwarden, no PostgreSQL, no Rust, no OAuth
  app connections. The architecture is inspired by OneCLI but the
  implementation is purpose-built for gopod.

---

## 11. Implementation plan

See [ROADMAP.md](../ROADMAP.md) Phase 6 (G1–G8) for milestone breakdown.

Summary:

| Step | What | Depends on |
|------|------|------------|
| G1 | `internal/vault` — Vault client (KV v2 read/write, AppRole auth, caching) | — |
| G2 | `internal/gateway/ca` — CA cert generation + per-host leaf certs | — |
| G3 | `internal/gateway` — HTTP CONNECT proxy scaffold (listen, auth, plain tunnel) | G1 |
| G4 | `internal/gateway` — MITM TLS interception + header injection | G2, G3 |
| G5 | Container wiring: agent_token column, proxy env vars, CA cert mount, Dockerfile update | G4 |
| G6 | `internal/gateway/policy` — scoped resolution (per-chat + shared), caching | G4 |
| G7 | Control plane: `/secrets` commands via Router | G5, G6 |
| G8 | SQLite fallback backends (Transit + local key) | G6 |

G1 and G2 are independent and can be built in parallel.
