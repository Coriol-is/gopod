package observability

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// initMetricsForTest boots a provider with metrics on an ephemeral loopback
// port and returns it with a /metrics scrape helper.
func initMetricsForTest(t *testing.T, drop bool) (Provider, func() string) {
	t.Helper()
	t.Setenv(EnvOTLPEndpoint, "")

	cfg := Config{
		MetricsAddr:   "127.0.0.1:0",
		MetricsPath:   DefaultMetricsPath,
		DropChatLabel: drop,
		Version:       "test-1.0",
	}
	p, err := Init(cfg)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	scrape := func() string {
		t.Helper()
		body, status := httpGet(t, "http://"+p.MetricsAddr()+DefaultMetricsPath)
		if status != http.StatusOK {
			t.Fatalf("GET /metrics status = %d, want 200", status)
		}
		return body
	}
	return p, scrape
}

func httpGet(t *testing.T, url string) (string, int) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b), resp.StatusCode
}

func TestMetricsHandlerRoutes(t *testing.T) {
	reg, err := newRegistry(Config{Version: "test-1.0"})
	if err != nil {
		t.Fatalf("newRegistry: %v", err)
	}
	t.Cleanup(func() { _ = bindCatalog(nil, false) })
	h := newMetricsHandler("/metrics", reg)

	t.Run("GET /metrics exposes build info and process metrics", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			`gopod_build_info{`,
			`version="test-1.0"`,
			`go_version="`,
			`sha="`,
			"gopod_uptime_seconds ",
			"process_start_time_seconds ",
			"go_goroutines ",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("/metrics body lacks %q", want)
			}
		}
	})

	t.Run("GET /healthz", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
		if got := rec.Body.String(); got != "ok" {
			t.Errorf("body = %q, want %q", got, "ok")
		}
	})

	t.Run("unknown paths are 404", func(t *testing.T) {
		for _, path := range []string{"/", "/metrics/", "/healthz/", "/debug/pprof/", "/index.html", "/metricsx"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusNotFound {
				t.Errorf("GET %s status = %d, want 404", path, rec.Code)
			}
		}
	})

	t.Run("non-GET is 405", func(t *testing.T) {
		for _, path := range []string{"/metrics", "/healthz"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("POST %s status = %d, want 405", path, rec.Code)
			}
			if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
				t.Errorf("POST %s Allow = %q, want GET", path, allow)
			}
		}
	})
}

func TestMetricsPathIsConfigurable(t *testing.T) {
	reg, err := newRegistry(Config{})
	if err != nil {
		t.Fatalf("newRegistry: %v", err)
	}
	t.Cleanup(func() { _ = bindCatalog(nil, false) })

	// A missing leading slash is tolerated; empty falls back to the default.
	cases := []struct{ cfgPath, hit string }{
		{"/prom", "/prom"},
		{"prom", "/prom"},
		{"", "/metrics"},
	}
	for _, tc := range cases {
		h := newMetricsHandler(tc.cfgPath, reg)

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.hit, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("path %q: GET %s status = %d, want 200", tc.cfgPath, tc.hit, rec.Code)
		}

		other := "/metrics"
		if tc.hit == "/metrics" {
			other = "/prom"
		}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, other, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("path %q: GET %s status = %d, want 404", tc.cfgPath, other, rec.Code)
		}
	}
}

func TestListenerEndToEnd(t *testing.T) {
	p, scrape := initMetricsForTest(t, false)

	MessagesReceived.Inc("owner", "in")
	MessagesReceived.Inc("owner", "in")
	TelegramSendDuration.Observe(0.2, "owner")

	body := scrape()
	for _, want := range []string{
		`gopod_build_info{`,
		`version="test-1.0"`,
		"process_start_time_seconds ",
		`gopod_messages_received_total{chat="owner",direction="in"} 2`,
		`gopod_telegram_send_duration_seconds_count{chat="owner"} 1`,
		`gopod_telegram_send_duration_seconds_bucket{chat="owner",le="0.25"} 1`,
		`gopod_telegram_send_duration_seconds_bucket{chat="owner",le="0.1"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics body lacks %q", want)
		}
	}

	if got, status := httpGet(t, "http://"+p.MetricsAddr()+"/healthz"); got != "ok" || status != http.StatusOK {
		t.Errorf("GET /healthz = (%q, %d), want (ok, 200)", got, status)
	}
	if _, status := httpGet(t, "http://"+p.MetricsAddr()+"/nope"); status != http.StatusNotFound {
		t.Errorf("GET /nope status = %d, want 404", status)
	}
}

func TestDropChatLabel(t *testing.T) {
	_, scrape := initMetricsForTest(t, true)

	// Call sites still pass the chat value in catalog order; the wrapper
	// strips it.
	MessagesReceived.Inc("owner", "in")
	MessagesReceived.Inc("other-chat", "in")
	TelegramSendDuration.Observe(0.2, "owner")
	TelegramSendDuration.Observe(0.3, "other-chat")
	ControlCommands.Inc("ping", "public", "ok") // no chat label at all; unaffected

	body := scrape()
	for _, want := range []string{
		`gopod_messages_received_total{direction="in"} 2`,
		"gopod_telegram_send_duration_seconds_count 2",
		`gopod_control_commands_total{name="ping",perm="public",result="ok"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics body lacks %q", want)
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "gopod_") && strings.Contains(line, "chat=") {
			t.Errorf("chat label leaked with DropChatLabel: %s", line)
		}
	}
}

func TestLabelSetHelper(t *testing.T) {
	cases := []struct {
		labels   []string
		drop     bool
		wantName []string
		wantKeep []bool
	}{
		{[]string{"chat", "direction"}, false, []string{"chat", "direction"}, []bool{true, true}},
		{[]string{"chat", "direction"}, true, []string{"direction"}, []bool{false, true}},
		{[]string{"chat"}, true, []string{}, []bool{false}},
		{[]string{"name"}, true, []string{"name"}, []bool{true}},
		{nil, true, []string{}, []bool{}},
	}
	for _, tc := range cases {
		names, keep := labelSet(tc.labels, tc.drop)
		if strings.Join(names, ",") != strings.Join(tc.wantName, ",") {
			t.Errorf("labelSet(%v, %v) names = %v, want %v", tc.labels, tc.drop, names, tc.wantName)
		}
		if len(keep) != len(tc.wantKeep) {
			t.Fatalf("labelSet(%v, %v) keep len = %d, want %d", tc.labels, tc.drop, len(keep), len(tc.wantKeep))
		}
		for i := range keep {
			if keep[i] != tc.wantKeep[i] {
				t.Errorf("labelSet(%v, %v) keep = %v, want %v", tc.labels, tc.drop, keep, tc.wantKeep)
				break
			}
		}
	}
}

func TestUnboundCatalogIsNoopButChecksArity(t *testing.T) {
	if err := bindCatalog(nil, false); err != nil {
		t.Fatalf("bindCatalog(nil): %v", err)
	}

	// Correct arity: silently ignored.
	MessagesReceived.Inc("owner", "in")
	ActiveContainers.Set(3)
	AgentDuration.ObserveSince(time.Now(), "owner", "chat", "claude", "opus")

	// Wrong arity panics even when unbound, so O3 tests catch it.
	defer func() {
		if recover() == nil {
			t.Error("Inc with wrong label arity did not panic")
		}
	}()
	MessagesReceived.Inc("owner")
}

func TestCatalogMatchesSpec(t *testing.T) {
	// Names and label sets are fixed by docs/OBSERVABILITY.md §2.3.
	wantCounters := map[string][]string{
		"gopod_messages_received_total":          {"chat", "direction"},
		"gopod_messages_sent_total":              {"chat"},
		"gopod_agent_invocations_total":          {"chat", "mode", "provider", "model", "result"},
		"gopod_agent_errors_total":               {"chat", "kind"},
		"gopod_memory_ops_total":                 {"chat", "op"},
		"gopod_scheduled_task_runs_total":        {"chat", "status"},
		"gopod_skill_invocations_total":          {"skill", "tool", "result"},
		"gopod_control_commands_total":           {"name", "perm", "result"},
		"gopod_container_starts_total":           {"chat"},
		"gopod_container_crashes_total":          {"chat"},
		"gopod_container_idle_kills_total":       {"chat"},
		"gopod_container_leftover_cleaned_total": {},
		"gopod_llm_tokens_total":                 {"chat", "provider", "model", "kind"},
	}
	wantGauges := map[string][]string{
		"gopod_active_containers":      {},
		"gopod_queue_pending":          {},
		"gopod_queue_pending_per_chat": {"chat"},
		"gopod_queue_active_chats":     {},
		"gopod_memory_items":           {"chat"},
	}
	wantHistograms := map[string][]string{
		"gopod_agent_duration_seconds":            {"chat", "mode", "provider", "model"},
		"gopod_message_to_reply_seconds":          {"chat"},
		"gopod_embedding_duration_seconds":        {"provider", "model", "batch_size"},
		"gopod_memory_search_duration_seconds":    {"chat"},
		"gopod_telegram_send_duration_seconds":    {"chat"},
		"gopod_docker_exec_duration_seconds":      {"chat"},
		"gopod_skill_tool_duration_seconds":       {"skill", "tool"},
		"gopod_control_dispatch_duration_seconds": {"name"},
	}

	check := func(kind string, got map[string][]string, want map[string][]string) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: catalog has %d entries, spec has %d", kind, len(got), len(want))
		}
		for name, labels := range want {
			gl, ok := got[name]
			if !ok {
				t.Errorf("%s: %s missing from catalog", kind, name)
				continue
			}
			if strings.Join(gl, ",") != strings.Join(labels, ",") {
				t.Errorf("%s: %s labels = %v, want %v", kind, name, gl, labels)
			}
		}
	}

	gotC := map[string][]string{}
	for _, c := range allCounters {
		gotC[c.def.name] = c.def.labels
	}
	gotG := map[string][]string{}
	for _, g := range allGauges {
		gotG[g.def.name] = g.def.labels
	}
	gotH := map[string][]string{}
	for _, h := range allHistograms {
		gotH[h.def.name] = h.def.labels
	}
	check("counters", gotC, wantCounters)
	check("gauges", gotG, wantGauges)
	check("histograms", gotH, wantHistograms)

	wantBuckets := []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}
	if len(DefaultBuckets) != len(wantBuckets) {
		t.Fatalf("DefaultBuckets = %v, want %v", DefaultBuckets, wantBuckets)
	}
	for i := range wantBuckets {
		if DefaultBuckets[i] != wantBuckets[i] {
			t.Fatalf("DefaultBuckets = %v, want %v", DefaultBuckets, wantBuckets)
		}
	}
}

func TestDisabledOpensNoPort(t *testing.T) {
	t.Setenv(EnvMetricsAddr, "")
	t.Setenv(EnvOTLPEndpoint, "")

	p, err := Init(LoadConfig())
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	if p.MetricsAddr() != "" {
		t.Errorf("MetricsAddr() = %q with metrics disabled, want empty (no server created)", p.MetricsAddr())
	}
	// The catalog is unbound: increments go nowhere and nothing panics.
	MessagesReceived.Inc("owner", "in")
	if MessagesReceived.vec != nil {
		t.Error("catalog is bound to a registry with metrics disabled")
	}
}

func TestInitFailsWhenAddressIsTaken(t *testing.T) {
	t.Setenv(EnvOTLPEndpoint, "")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()

	_, err = Init(Config{MetricsAddr: ln.Addr().String(), MetricsPath: DefaultMetricsPath})
	if err == nil {
		t.Fatal("Init on an already-bound address returned nil error")
	}
	if !strings.Contains(err.Error(), "initializing metrics") {
		t.Errorf("error = %q, want it wrapped as initializing metrics", err)
	}
	// A failed Init leaves the catalog unbound.
	if MessagesReceived.vec != nil {
		t.Error("catalog stayed bound after failed Init")
	}
}

func TestShutdownWithinGrace(t *testing.T) {
	p, scrape := initMetricsForTest(t, false)
	_ = scrape() // prove it is up

	addr := p.MetricsAddr()
	start := time.Now()
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if took := time.Since(start); took > metricsShutdownGrace {
		t.Errorf("Shutdown took %v, want within the %v grace period", took, metricsShutdownGrace)
	}

	// The port is released after Shutdown returns.
	if _, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		t.Errorf("dial %s succeeded after Shutdown, want connection refused", addr)
	}
}

func TestShutdownWithCancelledContextClosesListener(t *testing.T) {
	p, scrape := initMetricsForTest(t, false)
	_ = scrape()
	addr := p.MetricsAddr()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// With no in-flight requests Shutdown completes regardless of ctx; if
	// it reports an error the listener must still be closed.
	_ = p.Shutdown(ctx)

	if _, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		t.Errorf("dial %s succeeded after Shutdown with cancelled ctx", addr)
	}
}
