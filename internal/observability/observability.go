// Package observability owns gopod's opt-in metrics and tracing providers
// (docs/OBSERVABILITY.md, D014).
//
// Both signals are off by default. Init is meant to be called once from
// cmd/gopod/main.go right after config load; other packages never import
// this package for tracing — they use the global otel.Tracer(...) and get
// no-op spans until an exporter is configured. For metrics they use the
// catalog vars in metrics.go (observability.MessagesReceived.Inc(...)),
// which are no-ops until Init binds them to a registry.
//
// State as of O2: with GOPOD_METRICS_ADDR set, Init creates a private
// Prometheus registry and serves it on that address (§2.2); with it empty
// nothing listens. Tracing is still the otel no-op provider; the OTLP
// exporter is O4.
package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// shutdownFunc flushes and stops one provider.
type shutdownFunc func(context.Context) error

// Provider is the handle returned by Init. Its only job is to stop whatever
// Init started; call Shutdown once during process exit.
type Provider struct {
	cfg         Config
	metricsAddr string
	shutdowns   []shutdownFunc
}

// Init installs the tracing and metrics providers described by cfg and
// returns a Provider whose Shutdown tears them down again.
//
// Tracing always installs the OpenTelemetry no-op tracer provider (O4
// replaces it). Metrics: when cfg.MetricsEnabled() the catalog is bound to
// a private registry and the /metrics + /healthz listener starts on
// cfg.MetricsAddr; a bind failure is returned as an error. When disabled no
// port is opened and the catalog stays a no-op.
//
// Init must run before any instrumented goroutine starts: binding the
// catalog mutates package-level metric wrappers without locking.
func Init(cfg Config) (Provider, error) {
	log := slog.Default()
	p := Provider{cfg: cfg}

	tracingShutdown, err := initTracing(cfg, log)
	if err != nil {
		return Provider{}, fmt.Errorf("initializing tracing: %w", err)
	}
	p.shutdowns = append(p.shutdowns, tracingShutdown)

	metricsShutdown, addr, err := initMetrics(cfg, log)
	if err != nil {
		// Undo tracing so a failed Init leaves nothing half-installed.
		_ = tracingShutdown(context.Background())
		return Provider{}, fmt.Errorf("initializing metrics: %w", err)
	}
	p.shutdowns = append(p.shutdowns, metricsShutdown)
	p.metricsAddr = addr

	return p, nil
}

// Config returns the configuration Init was called with.
func (p Provider) Config() Config { return p.cfg }

// MetricsAddr returns the address the metrics listener is bound to, or ""
// when metrics are disabled. It differs from Config().MetricsAddr when the
// operator asked for port 0.
func (p Provider) MetricsAddr() string { return p.metricsAddr }

// Shutdown stops every provider Init started, in reverse order, and returns
// the joined errors. It runs all hooks even if one fails so a broken
// exporter never keeps the metrics listener alive. The metrics listener
// gets at most a 5-second grace period (§2.2), bounded further by ctx.
func (p Provider) Shutdown(ctx context.Context) error {
	var errs []error
	for i := len(p.shutdowns) - 1; i >= 0; i-- {
		if err := p.shutdowns[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
