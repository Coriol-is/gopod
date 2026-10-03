package observability

import "os"

// Environment variable names. They are fixed by docs/OBSERVABILITY.md §2.5
// and §3.7 and by D014; do not rename them here.
const (
	// EnvMetricsAddr is the listen address for the Prometheus /metrics
	// endpoint, e.g. "127.0.0.1:9090". Empty disables metrics.
	EnvMetricsAddr = "GOPOD_METRICS_ADDR"

	// EnvMetricsPath is the URL path the exposition is served on. Empty
	// means DefaultMetricsPath.
	EnvMetricsPath = "GOPOD_METRICS_PATH"

	// EnvMetricsDropChatLabel, when set to exactly "1", omits the `chat`
	// label from every metric that carries one (OBSERVABILITY.md §2.4).
	EnvMetricsDropChatLabel = "GOPOD_METRICS_DROP_CHAT_LABEL"

	// EnvOTLPEndpoint is the OTLP collector endpoint, e.g.
	// "http://127.0.0.1:4317". Empty disables tracing.
	EnvOTLPEndpoint = "GOPOD_OTLP_ENDPOINT"
)

// DefaultMetricsPath is the exposition path when GOPOD_METRICS_PATH is unset.
const DefaultMetricsPath = "/metrics"

// Config holds the observability switches. Both signals are opt-in: an
// empty value means the corresponding subsystem stays a no-op and opens no
// port and makes no network call.
//
// The OTLP knobs beyond the endpoint (protocol/headers/insecure, sample
// rate, service name) land with O4 alongside the code that uses them.
type Config struct {
	// MetricsAddr is the host:port for the Prometheus exposition listener.
	// Empty = disabled.
	MetricsAddr string

	// MetricsPath is the URL path the exposition is served on. LoadConfig
	// fills in DefaultMetricsPath when the env var is empty.
	MetricsPath string

	// DropChatLabel omits the `chat` label from every metric that has one,
	// bounding cardinality for installs with many chats (§2.4).
	DropChatLabel bool

	// Version is the gopod build version reported by gopod_build_info. It
	// is not read from the environment; cmd/gopod/main.go sets it from its
	// link-time/VCS version after LoadConfig. Empty is reported as "dev".
	Version string

	// OTLPEndpoint is the OTLP trace collector URL. Empty = disabled.
	OTLPEndpoint string
}

// LoadConfig reads Config from the process environment. Per D012 the
// environment is the only source of truth; there is no config-file fallback.
func LoadConfig() Config {
	path := os.Getenv(EnvMetricsPath)
	if path == "" {
		path = DefaultMetricsPath
	}
	return Config{
		MetricsAddr:   os.Getenv(EnvMetricsAddr),
		MetricsPath:   path,
		DropChatLabel: os.Getenv(EnvMetricsDropChatLabel) == "1",
		OTLPEndpoint:  os.Getenv(EnvOTLPEndpoint),
	}
}

// MetricsEnabled reports whether the operator asked for the metrics
// listener. When false, Init opens no port and the metric catalog stays a
// no-op.
func (c Config) MetricsEnabled() bool { return c.MetricsAddr != "" }

// TracingEnabled reports whether the operator asked for OTLP trace export.
// Until O4 this only affects logging.
func (c Config) TracingEnabled() bool { return c.OTLPEndpoint != "" }
