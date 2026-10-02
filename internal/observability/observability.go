// Package observability owns gopod's opt-in metrics and tracing providers
// (docs/OBSERVABILITY.md, D014).
//
// Both signals are off by default. Init is meant to be called once from
// cmd/gopod/main.go right after config load; other packages never import
// this package for tracing — they use the global otel.Tracer(...) and get
// no-op spans until an exporter is configured.
//
// O1 scope: this package installs no-op providers only. It never opens a
// port and never makes a network call, regardless of configuration. The
// Prometheus listener is O2 and the OTLP exporter is O4.
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
	cfg       Config
	shutdowns []shutdownFunc
}

// Init installs the tracing and metrics providers described by cfg and
// returns a Provider whose Shutdown tears them down again.
//
// With both switches empty, Init installs the OpenTelemetry no-op tracer
// provider and a no-op metrics facade. With either switch set, O1 still
// behaves as a no-op and logs that the real provider lands in O2/O4.
func Init(cfg Config) (Provider, error) {
	log := slog.Default()
	p := Provider{cfg: cfg}

	tracingShutdown, err := initTracing(cfg, log)
	if err != nil {
		return Provider{}, fmt.Errorf("initializing tracing: %w", err)
	}
	p.shutdowns = append(p.shutdowns, tracingShutdown)

	metricsShutdown, err := initMetrics(cfg, log)
	if err != nil {
		// Undo tracing so a failed Init leaves nothing half-installed.
		_ = tracingShutdown(context.Background())
		return Provider{}, fmt.Errorf("initializing metrics: %w", err)
	}
	p.shutdowns = append(p.shutdowns, metricsShutdown)

	return p, nil
}

// Config returns the configuration Init was called with.
func (p Provider) Config() Config { return p.cfg }

// Shutdown stops every provider Init started, in reverse order, and returns
// the joined errors. It runs all hooks even if one fails so a broken
// exporter never keeps the metrics listener (O2) alive. For the O1 no-op
// providers it always returns nil, including with an already-cancelled ctx.
func (p Provider) Shutdown(ctx context.Context) error {
	var errs []error
	for i := len(p.shutdowns) - 1; i >= 0; i-- {
		if err := p.shutdowns[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
