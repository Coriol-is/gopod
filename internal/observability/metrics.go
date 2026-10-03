package observability

import (
	"fmt"
	"log/slog"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// initMetrics sets up the metrics subsystem.
//
// Disabled (GOPOD_METRICS_ADDR empty): the catalog is unbound so every
// wrapper is a no-op, and no port is opened. Enabled: a private registry
// is created with the Go/process collectors and the full §2.3 catalog, and
// the §2.2 listener is started. Returns the bound address for the enabled
// case.
func initMetrics(cfg Config, log *slog.Logger) (shutdownFunc, string, error) {
	if !cfg.MetricsEnabled() {
		if err := bindCatalog(nil, false); err != nil {
			return nil, "", err
		}
		log.Debug("observability: metrics disabled")
		return noopShutdown, "", nil
	}

	reg, err := newRegistry(cfg)
	if err != nil {
		return nil, "", err
	}

	srv, err := startMetricsServer(cfg, reg, log)
	if err != nil {
		// Leave nothing half-installed: unbind so later Inits start clean.
		_ = bindCatalog(nil, false)
		return nil, "", err
	}

	log.Info("observability: metrics listener started",
		"addr", srv.addr(),
		"path", normalizeMetricsPath(cfg.MetricsPath),
		"drop_chat_label", cfg.DropChatLabel)

	return srv.shutdown, srv.addr(), nil
}

// This file is the single home of gopod's metric catalog
// (docs/OBSERVABILITY.md §2.3). Every counter, gauge and histogram is
// declared here as an exported package-level var; other packages (O3) call
// e.g. observability.MessagesReceived.Inc("owner", "in") and never import
// prometheus themselves.
//
// Label values are always passed in catalog order, `chat` included. When
// GOPOD_METRICS_DROP_CHAT_LABEL=1 (§2.4) the chat value is dropped by the
// wrapper before it reaches Prometheus, so call sites do not change.
//
// Until Init binds the catalog to a registry (or when metrics are
// disabled) every wrapper is a no-op that still validates label arity, so
// an instrumentation bug surfaces in tests regardless of configuration.

// DefaultBuckets are the §2.3 histogram buckets in seconds: wide enough for
// a single store query up to a 5-minute agent run.
var DefaultBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}

// chatLabel is the only high-cardinality label (§2.4).
const chatLabel = "chat"

// metricDef is the static part of a catalog entry.
type metricDef struct {
	name   string
	help   string
	labels []string // catalog order; may contain chatLabel
}

// labelSet is the small helper that builds the effective label set for one
// metric: the catalog labels with `chat` removed when drop is set, plus a
// keep mask so values can be filtered the same way at call time.
func labelSet(labels []string, drop bool) (names []string, keep []bool) {
	names = make([]string, 0, len(labels))
	dropped := false
	keep = make([]bool, len(labels))
	for i, l := range labels {
		if drop && l == chatLabel {
			dropped = true
			continue
		}
		names = append(names, l)
		keep[i] = true
	}
	if !dropped {
		// Nothing filtered: a nil mask lets values() return vals as-is, so
		// the Inc/Observe hot path stays allocation-free.
		return names, nil
	}
	return names, keep
}

// values validates arity against the catalog definition and applies the
// keep mask. It panics on arity mismatch, matching prometheus' own
// WithLabelValues contract, so the bug is caught in the instrumented
// package's tests even when metrics are disabled.
func (d metricDef) values(keep []bool, vals []string) []string {
	if len(vals) != len(d.labels) {
		panic(fmt.Sprintf("observability: %s expects %d label values %v, got %d",
			d.name, len(d.labels), d.labels, len(vals)))
	}
	if keep == nil {
		return vals
	}
	out := make([]string, 0, len(vals))
	for i, v := range vals {
		if keep[i] {
			out = append(out, v)
		}
	}
	return out
}

// Counter is a catalog counter. The zero/unbound value is a no-op.
type Counter struct {
	def  metricDef
	keep []bool
	vec  *prometheus.CounterVec
}

// Inc adds 1. Values are in catalog order, `chat` included.
func (c *Counter) Inc(values ...string) { c.Add(1, values...) }

// Add adds n (must be >= 0). Values are in catalog order, `chat` included.
func (c *Counter) Add(n float64, values ...string) {
	v := c.def.values(c.keep, values)
	if c.vec == nil {
		return
	}
	c.vec.WithLabelValues(v...).Add(n)
}

// Gauge is a catalog gauge. The zero/unbound value is a no-op.
type Gauge struct {
	def  metricDef
	keep []bool
	vec  *prometheus.GaugeVec
}

// Set stores v. Values are in catalog order, `chat` included.
func (g *Gauge) Set(v float64, values ...string) {
	lv := g.def.values(g.keep, values)
	if g.vec == nil {
		return
	}
	g.vec.WithLabelValues(lv...).Set(v)
}

// Add adds delta (may be negative). Values are in catalog order.
func (g *Gauge) Add(delta float64, values ...string) {
	lv := g.def.values(g.keep, values)
	if g.vec == nil {
		return
	}
	g.vec.WithLabelValues(lv...).Add(delta)
}

// Inc adds 1.
func (g *Gauge) Inc(values ...string) { g.Add(1, values...) }

// Dec subtracts 1.
func (g *Gauge) Dec(values ...string) { g.Add(-1, values...) }

// Histogram is a catalog histogram with DefaultBuckets. The zero/unbound
// value is a no-op.
type Histogram struct {
	def  metricDef
	keep []bool
	vec  *prometheus.HistogramVec
}

// Observe records v (seconds). Values are in catalog order, `chat` included.
func (h *Histogram) Observe(v float64, values ...string) {
	lv := h.def.values(h.keep, values)
	if h.vec == nil {
		return
	}
	h.vec.WithLabelValues(lv...).Observe(v)
}

// ObserveSince records the seconds elapsed since start.
func (h *Histogram) ObserveSince(start time.Time, values ...string) {
	h.Observe(time.Since(start).Seconds(), values...)
}

// The catalog. Constructors append to the all* slices so bindCatalog can
// (re)register every entry without a second hand-maintained list.
var (
	allCounters   []*Counter
	allGauges     []*Gauge
	allHistograms []*Histogram
)

func counter(name, help string, labels ...string) *Counter {
	c := &Counter{def: metricDef{name: name, help: help, labels: labels}}
	allCounters = append(allCounters, c)
	return c
}

func gauge(name, help string, labels ...string) *Gauge {
	g := &Gauge{def: metricDef{name: name, help: help, labels: labels}}
	allGauges = append(allGauges, g)
	return g
}

func histogram(name, help string, labels ...string) *Histogram {
	h := &Histogram{def: metricDef{name: name, help: help, labels: labels}}
	allHistograms = append(allHistograms, h)
	return h
}

// Counters (§2.3).
var (
	MessagesReceived       = counter("gopod_messages_received_total", "Telegram updates stored in messages.", chatLabel, "direction")
	MessagesSent           = counter("gopod_messages_sent_total", "Successful telegram.Send calls.", chatLabel)
	AgentInvocations       = counter("gopod_agent_invocations_total", "runner.Run completions by result.", chatLabel, "mode", "provider", "model", "result")
	AgentErrors            = counter("gopod_agent_errors_total", "Agent errors by kind.", chatLabel, "kind")
	MemoryOps              = counter("gopod_memory_ops_total", "Memory tool handler runs by op.", chatLabel, "op")
	ScheduledTaskRuns      = counter("gopod_scheduled_task_runs_total", "Scheduler task executions by status.", chatLabel, "status")
	SkillInvocations       = counter("gopod_skill_invocations_total", "MCP skill tool calls by result.", "skill", "tool", "result")
	ControlCommands        = counter("gopod_control_commands_total", "Router.Dispatch completions by result.", "name", "perm", "result")
	ContainerStarts        = counter("gopod_container_starts_total", "Container spawns.", chatLabel)
	ContainerCrashes       = counter("gopod_container_crashes_total", "Containers that exited non-zero.", chatLabel)
	ContainerIdleKills     = counter("gopod_container_idle_kills_total", "Containers stopped by the idle watcher.", chatLabel)
	ContainerLeftoverClean = counter("gopod_container_leftover_cleaned_total", "Containers removed by boot-time leftover cleanup.")
	LLMTokens              = counter("gopod_llm_tokens_total", "LLM tokens by kind, when usage is reported.", chatLabel, "provider", "model", "kind")
)

// Gauges (§2.3). gopod_uptime_seconds and gopod_build_info are registered
// directly by bindCatalog because they are derived, not set by callers.
var (
	ActiveContainers    = gauge("gopod_active_containers", "Currently running gopod containers.")
	QueuePending        = gauge("gopod_queue_pending", "Messages awaiting processing across all chats.")
	QueuePendingPerChat = gauge("gopod_queue_pending_per_chat", "Per-chat message backlog.", chatLabel)
	QueueActiveChats    = gauge("gopod_queue_active_chats", "Chats currently holding a worker slot.")
	MemoryItems         = gauge("gopod_memory_items", "Rows in memories for this chat.", chatLabel)
)

// Histograms (§2.3), all in seconds with DefaultBuckets.
var (
	AgentDuration           = histogram("gopod_agent_duration_seconds", "Wall time of one runner.Run.", chatLabel, "mode", "provider", "model")
	MessageToReply          = histogram("gopod_message_to_reply_seconds", "End-to-end Telegram in to Telegram out.", chatLabel)
	EmbeddingDuration       = histogram("gopod_embedding_duration_seconds", "One embedder call.", "provider", "model", "batch_size")
	MemorySearchDuration    = histogram("gopod_memory_search_duration_seconds", "One hybrid vec0+fts5 search.", chatLabel)
	TelegramSendDuration    = histogram("gopod_telegram_send_duration_seconds", "One bot.SendMessage.", chatLabel)
	DockerExecDuration      = histogram("gopod_docker_exec_duration_seconds", "One ContainerExecAttach cycle.", chatLabel)
	SkillToolDuration       = histogram("gopod_skill_tool_duration_seconds", "One MCP tool round-trip.", "skill", "tool")
	ControlDispatchDuration = histogram("gopod_control_dispatch_duration_seconds", "One Router.Dispatch.", "name")
)

// startTime feeds gopod_uptime_seconds.
var startTime = time.Now()

// newRegistry creates the private registry (never the prometheus default),
// registers the Go and process collectors plus the derived gauges, and
// binds the whole catalog to it honoring cfg.DropChatLabel.
func newRegistry(cfg Config) (*prometheus.Registry, error) {
	reg := prometheus.NewRegistry()

	if err := reg.Register(collectors.NewGoCollector()); err != nil {
		return nil, fmt.Errorf("registering go collector: %w", err)
	}
	if err := reg.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return nil, fmt.Errorf("registering process collector: %w", err)
	}

	uptime := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "gopod_uptime_seconds",
		Help: "Seconds since gopod started.",
	}, func() float64 { return time.Since(startTime).Seconds() })
	if err := reg.Register(uptime); err != nil {
		return nil, fmt.Errorf("registering gopod_uptime_seconds: %w", err)
	}

	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gopod_build_info",
		Help: "Constant 1; the labels carry the build info.",
	}, []string{"version", "sha", "go_version"})
	if err := reg.Register(buildInfo); err != nil {
		return nil, fmt.Errorf("registering gopod_build_info: %w", err)
	}
	version := cfg.Version
	if version == "" {
		version = "dev"
	}
	buildInfo.WithLabelValues(version, vcsRevision(), runtime.Version()).Set(1)

	if err := bindCatalog(reg, cfg.DropChatLabel); err != nil {
		return nil, err
	}
	return reg, nil
}

// bindCatalog (re)creates every catalog vec against reg. A nil reg unbinds
// the catalog, returning every wrapper to its no-op state.
//
// It mutates package-level wrappers without synchronization: Init (and
// therefore bindCatalog) must run strictly before any instrumented goroutine
// starts. O3 must keep calling Init from main before subsystems boot.
func bindCatalog(reg *prometheus.Registry, drop bool) error {
	for _, c := range allCounters {
		c.vec, c.keep = nil, nil
		if reg == nil {
			continue
		}
		names, keep := labelSet(c.def.labels, drop)
		vec := prometheus.NewCounterVec(prometheus.CounterOpts{Name: c.def.name, Help: c.def.help}, names)
		if err := reg.Register(vec); err != nil {
			return fmt.Errorf("registering %s: %w", c.def.name, err)
		}
		c.vec, c.keep = vec, keep
	}
	for _, g := range allGauges {
		g.vec, g.keep = nil, nil
		if reg == nil {
			continue
		}
		names, keep := labelSet(g.def.labels, drop)
		vec := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: g.def.name, Help: g.def.help}, names)
		if err := reg.Register(vec); err != nil {
			return fmt.Errorf("registering %s: %w", g.def.name, err)
		}
		g.vec, g.keep = vec, keep
	}
	for _, h := range allHistograms {
		h.vec, h.keep = nil, nil
		if reg == nil {
			continue
		}
		names, keep := labelSet(h.def.labels, drop)
		vec := prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    h.def.name,
			Help:    h.def.help,
			Buckets: DefaultBuckets,
		}, names)
		if err := reg.Register(vec); err != nil {
			return fmt.Errorf("registering %s: %w", h.def.name, err)
		}
		h.vec, h.keep = vec, keep
	}
	return nil
}

// vcsRevision returns the VCS revision embedded by `go build`, or
// "unknown" for builds without VCS info.
func vcsRevision() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				return s.Value
			}
		}
	}
	return "unknown"
}
