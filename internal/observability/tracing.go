package observability

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
)

// initTracing installs the global OpenTelemetry tracer provider.
//
// O1 always installs the no-op provider: every otel.Tracer(...).Start call
// anywhere in gopod yields a non-recording span, so subsystems can start
// adding spans (O3/O5) before the real exporter exists. When
// GOPOD_OTLP_ENDPOINT is set this is logged, but no SDK provider is built
// and nothing is dialled; the OTLP exporter wiring is O4.
func initTracing(cfg Config, log *slog.Logger) (shutdownFunc, error) {
	// The otel global delegates only once: tracers obtained before the first
	// SetTracerProvider are re-pointed by that call and by no later one. O4
	// must therefore replace this line with a single noop-or-SDK choice
	// (OBSERVABILITY.md §3.2), never "noop first, then SDK".
	otel.SetTracerProvider(noop.NewTracerProvider())

	if cfg.TracingEnabled() {
		log.Info("observability: OTLP endpoint configured but trace export is not implemented yet (O4); tracing stays no-op",
			"endpoint", cfg.OTLPEndpoint)
	} else {
		log.Debug("observability: tracing disabled (no-op tracer provider)")
	}

	return noopShutdown, nil
}

// noopShutdown is the shutdown hook for the no-op providers. It ignores the
// context on purpose: there is nothing to flush, so a cancelled or expired
// context must not turn into an error at process exit.
func noopShutdown(context.Context) error { return nil }
