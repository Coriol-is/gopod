package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metricsShutdownGrace is the §2.2 grace period for in-flight scrapes.
const metricsShutdownGrace = 5 * time.Second

// metricsReadHeaderTimeout bounds slowloris-style clients on the unauthenticated
// listener.
const metricsReadHeaderTimeout = 5 * time.Second

// metricsServer is the §2.2 listener: its own http.Server in its own
// goroutine, serving exactly GET <path> and GET /healthz.
type metricsServer struct {
	srv  *http.Server
	ln   net.Listener
	done chan struct{}
}

// normalizeMetricsPath guarantees a leading slash and the default when empty.
func normalizeMetricsPath(p string) string {
	if p == "" {
		return DefaultMetricsPath
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// newMetricsHandler builds the two-endpoint handler. Any other path is 404
// and any method other than GET is 405; there is no mux to grow.
func newMetricsHandler(path string, g prometheus.Gatherer) http.Handler {
	path = normalizeMetricsPath(path)
	exposition := promhttp.HandlerFor(g, promhttp.HandlerOpts{})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case path, "/healthz":
		default:
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		exposition.ServeHTTP(w, r)
	})
}

// startMetricsServer binds cfg.MetricsAddr synchronously (so Init can fail
// on a bad address) and serves in a background goroutine.
func startMetricsServer(cfg Config, g prometheus.Gatherer, log *slog.Logger) (*metricsServer, error) {
	ln, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		return nil, fmt.Errorf("listening on %q: %w", cfg.MetricsAddr, err)
	}

	s := &metricsServer{
		srv: &http.Server{
			Handler:           newMetricsHandler(cfg.MetricsPath, g),
			ReadHeaderTimeout: metricsReadHeaderTimeout,
		},
		ln:   ln,
		done: make(chan struct{}),
	}

	go func() {
		defer close(s.done)
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("observability: metrics listener stopped unexpectedly",
				"addr", ln.Addr().String(), "err", err)
		}
	}()

	return s, nil
}

// addr is the bound address, useful when cfg.MetricsAddr used port 0.
func (s *metricsServer) addr() string { return s.ln.Addr().String() }

// shutdown stops accepting, waits up to metricsShutdownGrace (or ctx,
// whichever ends first) for in-flight requests, then force-closes.
func (s *metricsServer) shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, metricsShutdownGrace)
	defer cancel()

	err := s.srv.Shutdown(ctx)
	if err != nil {
		// Grace expired or ctx cancelled: drop whatever is still open.
		_ = s.srv.Close()
	}
	<-s.done
	if err != nil {
		return fmt.Errorf("stopping metrics listener: %w", err)
	}
	return nil
}
