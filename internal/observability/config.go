package observability

import "os"

// Environment variable names. They are fixed by docs/OBSERVABILITY.md §2.5
// and §3.7 and by D014; do not rename them here.
const (
	// EnvMetricsAddr is the listen address for the Prometheus /metrics
	// endpoint, e.g. "127.0.0.1:9090". Empty disables metrics.
	EnvMetricsAddr = "GOPOD_METRICS_ADDR"

	// EnvOTLPEndpoint is the OTLP collector endpoint, e.g.
	// "http://127.0.0.1:4317". Empty disables tracing.
	EnvOTLPEndpoint = "GOPOD_OTLP_ENDPOINT"
)

// Config holds the observability switches. Both signals are opt-in: an
// empty value means the corresponding subsystem stays a no-op and opens no
// port and makes no network call.
//
// O1 carries only the two on/off switches. The remaining knobs from
// OBSERVABILITY.md (metrics path, OTLP protocol/headers/insecure, sample
// rate, service name) land with O2 and O4 alongside the code that uses them.
type Config struct {
	// MetricsAddr is the host:port for the Prometheus exposition listener.
	// Empty = disabled.
	MetricsAddr string

	// OTLPEndpoint is the OTLP trace collector URL. Empty = disabled.
	OTLPEndpoint string
}

// LoadConfig reads Config from the process environment. Per D012 the
// environment is the only source of truth; there is no config-file fallback.
func LoadConfig() Config {
	return Config{
		MetricsAddr:  os.Getenv(EnvMetricsAddr),
		OTLPEndpoint: os.Getenv(EnvOTLPEndpoint),
	}
}

// MetricsEnabled reports whether the operator asked for the metrics
// listener. In O1 this only affects logging; the listener itself is O2.
func (c Config) MetricsEnabled() bool { return c.MetricsAddr != "" }

// TracingEnabled reports whether the operator asked for OTLP trace export.
// In O1 this only affects logging; the real exporter is O4.
func (c Config) TracingEnabled() bool { return c.OTLPEndpoint != "" }
