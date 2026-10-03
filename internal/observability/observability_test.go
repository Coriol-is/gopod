package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
)

// envCases enumerates the four combinations of the two switches. Metrics
// use port 0 so an enabled case binds an ephemeral loopback port instead
// of colliding with a real 9090.
var envCases = []struct {
	name         string
	metricsAddr  string
	otlpEndpoint string
}{
	{name: "both unset"},
	{name: "metrics only", metricsAddr: "127.0.0.1:0"},
	{name: "otlp only", otlpEndpoint: "http://127.0.0.1:4317"},
	{name: "both set", metricsAddr: "127.0.0.1:0", otlpEndpoint: "http://127.0.0.1:4317"},
}

func TestLoadConfig(t *testing.T) {
	for _, tc := range envCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvMetricsAddr, tc.metricsAddr)
			t.Setenv(EnvOTLPEndpoint, tc.otlpEndpoint)
			t.Setenv(EnvMetricsPath, "")
			t.Setenv(EnvMetricsDropChatLabel, "")

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
			if cfg.MetricsPath != DefaultMetricsPath {
				t.Errorf("MetricsPath = %q, want default %q", cfg.MetricsPath, DefaultMetricsPath)
			}
			if cfg.DropChatLabel {
				t.Error("DropChatLabel = true with env unset, want false")
			}
			if cfg.Version != "" {
				t.Errorf("Version = %q from env, want empty (set by main, not env)", cfg.Version)
			}
		})
	}
}

func TestLoadConfigMetricsKnobs(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		drop     string
		wantPath string
		wantDrop bool
	}{
		{name: "defaults", wantPath: DefaultMetricsPath},
		{name: "custom path", path: "/prom", wantPath: "/prom"},
		{name: "drop=1", drop: "1", wantPath: DefaultMetricsPath, wantDrop: true},
		{name: "drop=true is not 1", drop: "true", wantPath: DefaultMetricsPath},
		{name: "drop=0", drop: "0", wantPath: DefaultMetricsPath},
		{name: "drop=yes is not 1", drop: "yes", wantPath: DefaultMetricsPath},
		{name: "drop= 1 with space is not 1", drop: " 1", wantPath: DefaultMetricsPath},
		{name: "both", path: "/m", drop: "1", wantPath: "/m", wantDrop: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvMetricsAddr, "")
			t.Setenv(EnvOTLPEndpoint, "")
			t.Setenv(EnvMetricsPath, tc.path)
			t.Setenv(EnvMetricsDropChatLabel, tc.drop)

			cfg := LoadConfig()
			if cfg.MetricsPath != tc.wantPath {
				t.Errorf("MetricsPath = %q, want %q", cfg.MetricsPath, tc.wantPath)
			}
			if cfg.DropChatLabel != tc.wantDrop {
				t.Errorf("DropChatLabel = %v, want %v", cfg.DropChatLabel, tc.wantDrop)
			}
		})
	}
}

func TestInitAndShutdown(t *testing.T) {
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
			if got, want := p.MetricsAddr() != "", tc.metricsAddr != ""; got != want {
				t.Errorf("MetricsAddr() = %q, want bound=%v", p.MetricsAddr(), want)
			}

			// Tracing is still the otel no-op provider in every configuration.
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
