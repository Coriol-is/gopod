package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
)

// envCases enumerates the four combinations of the two switches. Every test
// below iterates over them because O1 must behave identically (no-op, no
// ports, no errors) in all four.
var envCases = []struct {
	name         string
	metricsAddr  string
	otlpEndpoint string
}{
	{name: "both unset"},
	{name: "metrics only", metricsAddr: "127.0.0.1:9090"},
	{name: "otlp only", otlpEndpoint: "http://127.0.0.1:4317"},
	{name: "both set", metricsAddr: "127.0.0.1:9090", otlpEndpoint: "http://127.0.0.1:4317"},
}

func TestLoadConfig(t *testing.T) {
	for _, tc := range envCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvMetricsAddr, tc.metricsAddr)
			t.Setenv(EnvOTLPEndpoint, tc.otlpEndpoint)

			cfg := LoadConfig()

			if cfg.MetricsAddr != tc.metricsAddr {
				t.Errorf("MetricsAddr = %q, want %q", cfg.MetricsAddr, tc.metricsAddr)
			}
			if cfg.OTLPEndpoint != tc.otlpEndpoint {
				t.Errorf("OTLPEndpoint = %q, want %q", cfg.OTLPEndpoint, tc.otlpEndpoint)
			}
			if got, want := cfg.MetricsEnabled(), tc.metricsAddr != ""; got != want {
				t.Errorf("MetricsEnabled() = %v, want %v", got, want)
			}
			if got, want := cfg.TracingEnabled(), tc.otlpEndpoint != ""; got != want {
				t.Errorf("TracingEnabled() = %v, want %v", got, want)
			}
		})
	}
}

func TestInitAndShutdownNoop(t *testing.T) {
	for _, tc := range envCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvMetricsAddr, tc.metricsAddr)
			t.Setenv(EnvOTLPEndpoint, tc.otlpEndpoint)

			p, err := Init(LoadConfig())
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			if p.Config().MetricsAddr != tc.metricsAddr || p.Config().OTLPEndpoint != tc.otlpEndpoint {
				t.Errorf("Config() = %+v, want addr=%q endpoint=%q", p.Config(), tc.metricsAddr, tc.otlpEndpoint)
			}

			// O1 installs the otel no-op provider in every configuration.
			if _, ok := otel.GetTracerProvider().(noop.TracerProvider); !ok {
				t.Errorf("global TracerProvider = %T, want noop.TracerProvider", otel.GetTracerProvider())
			}

			if err := p.Shutdown(context.Background()); err != nil {
				t.Errorf("Shutdown: %v", err)
			}
		})
	}
}

func TestShutdownWithCancelledContext(t *testing.T) {
	t.Setenv(EnvMetricsAddr, "")
	t.Setenv(EnvOTLPEndpoint, "")

	p, err := Init(LoadConfig())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := p.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown with cancelled ctx = %v, want nil for no-op providers", err)
	}
}

func TestSpanIsNonRecordingAfterInit(t *testing.T) {
	t.Setenv(EnvMetricsAddr, "")
	t.Setenv(EnvOTLPEndpoint, "")

	p, err := Init(LoadConfig())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	_, span := otel.Tracer("gopod/test").Start(context.Background(), "test.span")
	defer span.End()

	if span.IsRecording() {
		t.Error("span.IsRecording() = true, want false under the no-op provider")
	}
	if span.SpanContext().IsValid() {
		t.Errorf("span has a valid SpanContext %v, want invalid (no-op)", span.SpanContext())
	}
}

func TestZeroProviderShutdown(t *testing.T) {
	// A zero Provider (Init never called) must still be safe to Shutdown so
	// callers can `defer p.Shutdown(ctx)` unconditionally.
	var p Provider
	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("zero Provider Shutdown = %v, want nil", err)
	}
}
