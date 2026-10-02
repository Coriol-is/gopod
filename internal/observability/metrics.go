package observability

import "log/slog"

// initMetrics sets up the metrics subsystem.
//
// O1 is a no-op facade: there is no Prometheus registry, no metric
// definitions and no HTTP listener. When GOPOD_METRICS_ADDR is set this is
// logged so the operator knows the flag was seen, but no port is opened.
// The registry, the metric catalog from OBSERVABILITY.md §2.3 and the
// /metrics + /healthz listener are O2.
func initMetrics(cfg Config, log *slog.Logger) (shutdownFunc, error) {
	if cfg.MetricsEnabled() {
		log.Info("observability: metrics address configured but the /metrics listener is not implemented yet (O2); no port opened",
			"addr", cfg.MetricsAddr)
	} else {
		log.Debug("observability: metrics disabled")
	}

	return noopShutdown, nil
}
