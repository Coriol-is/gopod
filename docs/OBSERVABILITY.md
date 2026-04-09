# picoclaw — Observability

> Companion docs: [ARCHITECTURE.md](ARCHITECTURE.md) · [CONTROL.md](CONTROL.md) · [DECISIONS.md](DECISIONS.md)
> Decision: [D014](DECISIONS.md)

picoclaw needs three observability surfaces: structured logs, metrics, and
distributed traces. All three are **opt-in via environment variables** and
**off by default** so a fresh install does not open any new ports or send
data anywhere unexpected.

| Signal | Backend | Default | How to enable |
|---|---|---|---|
| **Logs** | SQLite (`logs` table) + stderr | **on** | always on; [CONTROL.md §9](CONTROL.md) |
| **Metrics** | Prometheus pull (`/metrics` HTTP endpoint) | **off** | `PICOCLAW_METRICS_ADDR=127.0.0.1:9090` |
| **Traces** | OpenTelemetry OTLP push | **off** | `PICOCLAW_OTLP_ENDPOINT=...` |

Logs are covered by [CONTROL.md §9](CONTROL.md) (the `internal/log` SQLite
slog handler, the `/logs` control command, the daily retention task). This
document covers metrics and traces.

---

## 1. Why opt-in by default

A personal assistant on a personal machine should not, on first run:

- Open any TCP port the user did not ask for
- Send telemetry to any third party
- Allocate Prometheus memory for users who don't scrape

OpenTelemetry and Prometheus are excellent when configured, but their default
posture in a personal-use binary should be **silent**. Both subsystems
initialize as no-ops if their env vars are unset. Setting an env var is the
single switch.

This also means picoclaw does not depend on a running collector or
Prometheus instance to function. If `PICOCLAW_OTLP_ENDPOINT` points to an
unreachable collector, traces silently drop after the configured retry
budget; the data plane is unaffected.

---

## 2. Metrics — Prometheus

### 2.1 Library

[`github.com/prometheus/client_golang`](https://github.com/prometheus/client_golang).
Standard, stable, ~zero overhead when not scraped.

### 2.2 HTTP listener

When `PICOCLAW_METRICS_ADDR` is non-empty, picoclaw starts a tiny HTTP
server on that address that serves exactly two endpoints:

- `GET ${PICOCLAW_METRICS_PATH}` (default `/metrics`) — Prometheus exposition
- `GET /healthz` — returns `200 OK` with body `ok`, for liveness probes

The listener has **no other handlers**. It is not the control plane (the
control plane has no HTTP frontend per [D011](DECISIONS.md)) and it has no
authentication. Bind to `127.0.0.1` for loopback-only scraping; if you want
remote scraping, put a reverse proxy in front of it.

The listener runs in its own goroutine, has its own `http.Server`, and is
shut down on SIGTERM with a 5-second grace period.

### 2.3 Metric catalog

Names follow Prometheus conventions: `picoclaw_<subsystem>_<name>_<unit>`.
All metrics are registered with the default registry plus a process
collector for Go runtime stats.

#### Counters

| Name | Labels | Increment when |
|---|---|---|
| `picoclaw_messages_received_total` | `chat`, `direction` (`in` only here) | Telegram update stored in `messages` |
| `picoclaw_messages_sent_total` | `chat` | `telegram.Send` succeeds |
| `picoclaw_agent_invocations_total` | `chat`, `mode`, `provider`, `model`, `result` (`success`/`error`) | `runner.Run` returns |
| `picoclaw_agent_errors_total` | `chat`, `kind` (`spawn`/`exec`/`timeout`/`output_parse`/`provider`/`other`) | Any agent error |
| `picoclaw_memory_ops_total` | `chat`, `op` (`add`/`search`/`get`/`list`/`update`/`delete`) | Memory tool handler runs |
| `picoclaw_scheduled_task_runs_total` | `chat`, `status` (`success`/`error`/`skipped`) | Scheduler executes a task |
| `picoclaw_skill_invocations_total` | `skill`, `tool`, `result` | An MCP tool from a skill is called |
| `picoclaw_control_commands_total` | `name`, `perm`, `result` | `Router.Dispatch` returns |
| `picoclaw_container_starts_total` | `chat` | Container spawn |
| `picoclaw_container_crashes_total` | `chat` | Container exited non-zero |
| `picoclaw_container_idle_kills_total` | `chat` | Idle watcher killed a container |
| `picoclaw_container_leftover_cleaned_total` | (none) | Boot-time cleanup count |
| `picoclaw_llm_tokens_total` | `chat`, `provider`, `model`, `kind` (`input`/`output`) | After every LLM call (when usage is reported) |

#### Gauges

| Name | Labels | What |
|---|---|---|
| `picoclaw_active_containers` | (none) | Currently running picoclaw containers |
| `picoclaw_queue_pending` | (none) | Total messages awaiting processing across all chats |
| `picoclaw_queue_pending_per_chat` | `chat` | Per-chat backlog |
| `picoclaw_queue_active_chats` | (none) | Chats currently holding a worker slot |
| `picoclaw_memory_items` | `chat` | Row count in `memories` for this chat |
| `picoclaw_uptime_seconds` | (none) | `time.Since(startTime)` |
| `picoclaw_build_info` | `version`, `sha`, `go_version` | Constant 1 — labelset carries the build info |

#### Histograms

Default buckets: `0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300`
seconds. Suitable for everything from a single store query to a 5-minute
agent run.

| Name | Labels | Observes |
|---|---|---|
| `picoclaw_agent_duration_seconds` | `chat`, `mode`, `provider`, `model` | Wall time of one `runner.Run` |
| `picoclaw_message_to_reply_seconds` | `chat` | End-to-end: Telegram in → Telegram out |
| `picoclaw_embedding_duration_seconds` | `provider`, `model`, `batch_size` | One embedder call |
| `picoclaw_memory_search_duration_seconds` | `chat` | One hybrid (vec0+fts5) search |
| `picoclaw_telegram_send_duration_seconds` | `chat` | One `bot.SendMessage` |
| `picoclaw_docker_exec_duration_seconds` | `chat` | One `ContainerExecAttach` cycle |
| `picoclaw_skill_tool_duration_seconds` | `skill`, `tool` | One MCP tool round-trip |
| `picoclaw_control_dispatch_duration_seconds` | `name` | One `Router.Dispatch` |

### 2.4 Cardinality discipline

`chat` is the only high-cardinality label. For a personal assistant with
≤50 chats this is fine. For users with many chats, set
`PICOCLAW_METRICS_DROP_CHAT_LABEL=1` to omit the `chat` label from all
metrics — gives aggregate-only data but bounds cardinality.

`provider` and `model` come from a closed set (4 providers, ~10 models),
not user input.

`tool` and `skill` come from manifest files, also bounded.

We never label by user-supplied data (message content, sender username,
custom prompt text). Slog is the place for that.

### 2.5 Configuration

```
PICOCLAW_METRICS_ADDR=                          # empty = disabled. e.g. 127.0.0.1:9090
PICOCLAW_METRICS_PATH=/metrics                  # default
PICOCLAW_METRICS_DROP_CHAT_LABEL=0              # set to 1 to bound cardinality
```

---

## 3. Traces — OpenTelemetry

### 3.1 Library

- [`go.opentelemetry.io/otel`](https://pkg.go.dev/go.opentelemetry.io/otel)
- [`go.opentelemetry.io/otel/sdk/trace`](https://pkg.go.dev/go.opentelemetry.io/otel/sdk/trace)
- [`go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc`](https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc)
  (default; gRPC exporter)
- [`go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp`](https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp)
  (alternative; HTTP exporter)

Pick gRPC for local collectors (faster), HTTP for SaaS endpoints behind
HTTPS load balancers (Honeycomb, Datadog, Tempo, Grafana Cloud).

### 3.2 Initialization

```go
// internal/observability/tracing.go
func InitTracing(cfg Config) (shutdown func(context.Context) error, err error) {
    if cfg.OTLPEndpoint == "" {
        // No-op tracer provider; spans created elsewhere become no-ops.
        otel.SetTracerProvider(noop.NewTracerProvider())
        return func(context.Context) error { return nil }, nil
    }

    exporter, err := buildExporter(cfg)   // gRPC or HTTP based on URL scheme
    if err != nil { return nil, err }

    res, _ := resource.New(ctx,
        resource.WithAttributes(
            semconv.ServiceName("picoclaw"),
            semconv.ServiceVersion(cfg.Version),
            attribute.String("picoclaw.owner_chat_redacted", "yes"),
        ))

    tp := trace.NewTracerProvider(
        trace.WithBatcher(exporter),
        trace.WithResource(res),
        trace.WithSampler(trace.TraceIDRatioBased(cfg.SampleRate)),
    )
    otel.SetTracerProvider(tp)
    otel.SetTextMapPropagator(propagation.TraceContext{})
    return tp.Shutdown, nil
}
```

When `PICOCLAW_OTLP_ENDPOINT` is empty the tracer provider is the no-op
implementation — every `tracer.Start` call is free, every span attribute
write is dropped, every exporter call is skipped. This is the default state.

### 3.3 Span hierarchy

picoclaw produces three trace flows:

#### Inbound message flow

```
picoclaw.message.process              (root)
├── store.save_message
├── trigger.match
├── queue.enqueue
└── queue.work                         (started in worker goroutine, child of root via context propagation)
    └── runner.run
        ├── store.get_messages_since
        ├── prompt.format
        ├── skills.prepare_for_chat
        │   ├── skills.container_filter
        │   ├── skills.mcp_spawn         (one per per-chat MCP skill)
        │   └── skills.tools_register
        ├── memory.tools_for_chat
        ├── runner.ensure_container
        ├── runner.docker_exec           (the heavy span — usually >90% of latency)
        │   ├── llm.call                 (Mode B only — direct API; one per LLM round trip)
        │   └── memory.search            (when the agent invokes memory_search; child via in-process context)
        └── telegram.send
```

#### Scheduled task flow

```
picoclaw.task.run                      (root, started by scheduler)
└── runner.run                         (same subtree as above)
```

#### Control command flow

```
picoclaw.control.dispatch              (root)
├── control.auth_check
└── control.handler.<name>             (e.g. control.handler.chats.list)
```

### 3.4 Standard attributes

Every span sets `picoclaw.chat=<folder>` if applicable. Other standard keys:

| Key | Where |
|---|---|
| `picoclaw.chat` | Anything chat-scoped |
| `picoclaw.mode` | `runner.run` (`claude_container` or `direct_api`) |
| `picoclaw.provider` | `runner.run`, `llm.call`, `memory.search` (embedder) |
| `picoclaw.model` | `runner.run`, `llm.call` |
| `picoclaw.tokens.input` | `llm.call` |
| `picoclaw.tokens.output` | `llm.call` |
| `picoclaw.tools.allowed` | `runner.run` (count of tools registered) |
| `picoclaw.skill` | `skills.*` and `skill_tool.*` |
| `picoclaw.command` | `control.*` |
| `picoclaw.command.perm` | `control.auth_check` |

### 3.5 Trace context propagation across containers

picoclaw passes the W3C `traceparent` (and `tracestate`) into the agent
container as environment variables on each `docker exec`:

```
TRACEPARENT=00-<trace_id>-<span_id>-01
TRACESTATE=...
```

The current Claude Code CLI does **not** consume these — that's fine. They
are present so:

1. MCP skill processes spawned by picoclaw can pick them up via OTel's
   environment-based propagator
2. Future Claude Code versions or agent harnesses with OTel support can
   join the trace automatically
3. Container output can be correlated with the parent span manually if you
   look at exported traces in your backend

The same env vars are also passed to MCP server processes at spawn time
(SKILLS.md §2 lifecycle).

### 3.6 Secret redaction in spans

D012 says secrets only live in env. The OTel SDK has no automatic
redaction — anything we pass via `SetAttributes` is exported as-is.
picoclaw applies the same redaction allowlist used by `internal/log`:
attribute keys matching `(?i)token|key|secret|password|cookie|auth` get
their values replaced with `<redacted>` before being added to a span.

Implemented as a thin wrapper:

```go
// internal/observability/tracing_attrs.go
func SetAttr(span trace.Span, k string, v any) {
    if redact.MatchKey(k) {
        span.SetAttributes(attribute.String(k, "<redacted>"))
        return
    }
    // type-aware setter
    ...
}
```

Use `obs.SetAttr` instead of `span.SetAttributes` everywhere in picoclaw
code. Linted via a small `go vet` analyzer in `tools/linters/` (post-v0).

### 3.7 Configuration

```
PICOCLAW_OTLP_ENDPOINT=                         # empty = disabled. e.g. https://api.honeycomb.io:443 or http://localhost:4317
PICOCLAW_OTLP_PROTOCOL=grpc                     # grpc | http (auto-derived from URL scheme if empty)
PICOCLAW_OTLP_HEADERS=                          # comma-separated, e.g. x-honeycomb-team=<KEY>,x-other=val. Values are secret material — set via env, never via committed file (D012). Logged with redaction.
PICOCLAW_OTLP_INSECURE=0                        # 1 to skip TLS verification (local collector only)
PICOCLAW_TRACE_SAMPLE_RATE=1.0                  # 0.0..1.0
PICOCLAW_SERVICE_NAME=picoclaw                  # in case you run multiple picoclaw instances
PICOCLAW_SERVICE_VERSION=                       # baked at build time normally
```

`PICOCLAW_OTLP_HEADERS` is the one observability env var that may carry a
secret value (vendor API key). It is read into memory at process start, used
to construct exporter headers, and never logged or exported as a metric
label. The key/value pairs go through the same redaction matcher: a header
named `x-honeycomb-team` is fine in raw form (the matcher doesn't see it),
but anything containing `token`, `key`, etc. would get redacted in logs if
ever printed by accident.

---

## 4. Module layout

```
internal/observability/
├── observability.go       # Init() called from main, returns shutdown func
├── config.go              # Config struct + env loading
├── metrics.go             # Prometheus registry + all metric definitions
├── metrics_handler.go     # http.Server wiring
├── tracing.go             # OTel tracer provider, exporter wiring
├── tracing_attrs.go       # SetAttr (with redaction)
├── shutdown.go            # graceful flush on SIGTERM
└── observability_test.go
```

`Init` is called once from `cmd/picoclaw/main.go` immediately after config
load and before any subsystem that wants to emit signals. It returns a
single `shutdown(ctx)` that flushes both subsystems with a 5-second grace
period. Subsystems do not import `internal/observability` directly — they
use the global tracer (`otel.Tracer("picoclaw/runner")`) and the metric
package functions exported by `internal/observability/metrics.go`.

---

## 5. How features wire up

| Feature | Metrics added | Trace spans added |
|---|---|---|
| `internal/store` | `picoclaw_messages_received_total`, query histogram (optional later) | `store.*` spans on hot paths |
| `internal/queue` | `picoclaw_queue_*` gauges | `queue.enqueue`, `queue.work` |
| `internal/runner` | `picoclaw_agent_*`, `picoclaw_container_*`, `picoclaw_docker_exec_duration_seconds` | `runner.run` and children |
| `internal/scheduler` | `picoclaw_scheduled_task_runs_total` | `picoclaw.task.run` root span |
| `internal/memory` | `picoclaw_memory_ops_total`, `picoclaw_memory_search_duration_seconds`, `picoclaw_memory_items` | `memory.search`, `memory.add`, embedder spans |
| `internal/skills` | `picoclaw_skill_invocations_total`, `picoclaw_skill_tool_duration_seconds` | `skills.*`, `skill_tool.*` |
| `internal/control` | `picoclaw_control_commands_total`, `picoclaw_control_dispatch_duration_seconds` | `picoclaw.control.dispatch` root span |
| `internal/llm` | `picoclaw_llm_tokens_total` | `llm.call` |
| `internal/telegram` | `picoclaw_messages_sent_total`, `picoclaw_telegram_send_duration_seconds` | `telegram.send` |

Every increment / observation lives in the package that owns the operation.
There is no central "log this thing" indirection.

---

## 6. Implementation milestones

Slot into [ROADMAP.md](../ROADMAP.md) Phase 1 as **M3.6**, immediately after
M3.5 (control plane). Logs already exist by then (M3.5/C5); M3.6 adds the
two opt-in subsystems.

| ID | Step |
|----|------|
| **O1** | `internal/observability` scaffold: `Init`, no-op providers, config loader, shutdown |
| **O2** | Metric definitions in one place, registry, the `127.0.0.1:9090/metrics` listener gated by `PICOCLAW_METRICS_ADDR` |
| **O3** | Wire counters/gauges/histograms into `store`, `queue`, `runner`, `telegram`, `control` (the existing-by-then packages) |
| **O4** | OTel scaffold: `Init`, no-op tracer when env unset, exporter selection, redaction wrapper |
| **O5** | `runner.run` root span tree + propagation env vars on `docker exec` + MCP spawn |
| **O6** | Smoke tests: hit a local Prom, hit a local OTel collector, verify cardinality |

Later milestones (M4 scheduler, M9 memory, S* skills) add their own spans
and counters as part of their normal implementation, using the helpers
from M3.6. This means M3.6 is the **only** place where adding observability
to picoclaw requires touching `internal/observability` itself.

---

## 7. Operator recipes

### 7.1 Local-only Prometheus + Grafana stack

```bash
PICOCLAW_METRICS_ADDR=127.0.0.1:9090 picoclaw serve
# Then Prometheus scrape config:
#   - job_name: picoclaw
#     static_configs:
#       - targets: ['127.0.0.1:9090']
```

### 7.2 OTel local collector

```bash
PICOCLAW_OTLP_ENDPOINT=http://127.0.0.1:4317 \
PICOCLAW_OTLP_INSECURE=1 \
picoclaw serve
```

### 7.3 Honeycomb (or any SaaS)

```bash
PICOCLAW_OTLP_ENDPOINT=https://api.honeycomb.io:443 \
PICOCLAW_OTLP_PROTOCOL=grpc \
PICOCLAW_OTLP_HEADERS="x-honeycomb-team=$HONEYCOMB_API_KEY" \
picoclaw serve
```

The API key is read from the operator's environment; it never lives in any
picoclaw config file.

### 7.4 Disable everything

```bash
unset PICOCLAW_METRICS_ADDR PICOCLAW_OTLP_ENDPOINT
picoclaw serve
```

This is the default. Logs still go to stderr and to the SQLite `logs` table.

---

## 8. Sources

- [prometheus/client_golang](https://github.com/prometheus/client_golang)
- [go.opentelemetry.io/otel](https://pkg.go.dev/go.opentelemetry.io/otel)
- [OTLP gRPC exporter](https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc)
- [OTLP HTTP exporter](https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp)
- [W3C Trace Context spec](https://www.w3.org/TR/trace-context/)
- [Prometheus naming conventions](https://prometheus.io/docs/practices/naming/)
