package isolationproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testConfig() Config {
	return Config{Token: strings.Repeat("a", 48), Routes: []Route{{Host: "api.example.com", Method: "POST", Path: "/v1/messages", Body: AnthropicMessages}, {Host: "chatgpt.example.com", Method: "POST", Path: "/backend-api/codex/responses", Body: Responses}}, AllowedTools: []string{"mcp__group__group_exec", "mcp__group.group_exec", "update_plan"}}
}
func makeProxy(t *testing.T) (*Proxy, *http.Client) {
	t.Helper()
	p, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(p)
	t.Cleanup(func() { p.Close(); server.Close() })
	proxyURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("group", testConfig().Token)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(p.CertificatePEM()) {
		t.Fatal("CA invalid")
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	return p, &http.Client{Transport: transport, Timeout: 3 * time.Second}
}
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}
func post(t *testing.T, c *http.Client, address, body string) *http.Response {
	t.Helper()
	r, err := http.NewRequest("POST", address, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer credential-canary")
	res, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestTerminatedHTTPSAllowedInference(t *testing.T) {
	p, c := makeProxy(t)
	calls := 0
	p.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://api.example.com/v1/messages" || r.Header.Get("Authorization") != "Bearer credential-canary" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("bad forwarding envelope")
		}
		return response("data: synthetic\n\n"), nil
	})
	res := post(t, c, "https://api.example.com/v1/messages", `{"model":"test","tools":[{"name":"mcp__group__group_exec","input_schema":{"type":"object"}}]}`)
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || string(b) != "data: synthetic\n\n" || calls != 1 {
		t.Fatalf("status %d calls %d", res.StatusCode, calls)
	}
}
func TestDenyRoutesAndServerCapabilities(t *testing.T) {
	p, c := makeProxy(t)
	var calls atomic.Int32
	p.transport = roundTripFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return response("unexpected"), nil })
	for _, tc := range []struct{ path, body string }{
		{"/account", `{}`}, {"/v1/messages?anything=1", `{}`}, {"/v1/%6dessages", `{}`}, {"/v1/messages/../messages", `{}`},
		{"/v1/messages", `{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`},
		{"/v1/messages", `{"tools":[{"name":"Bash","input_schema":{}}]}`},
		{"/v1/messages", `{"tools":[{"name":"mcp__group__group_exec","server_url":"https://private"}]}`},
		{"/v1/messages", `{"mcp_servers":[]}`}, {"/v1/messages", `{"connectors":[]}`},
		{"/v1/messages", `{"tools":[],"tools":[{"name":"Bash"}]}`},
		{"/v1/messages", `{"background":false}`}, {"/v1/messages", `{} {}`},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			res := post(t, c, "https://api.example.com"+tc.path, tc.body)
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != 403 || strings.Contains(string(body), "credential-canary") {
				t.Fatalf("status %d body %q", res.StatusCode, body)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("denied request reached upstream")
	}
}
func TestResponsesOnlyExplicitLocalFunctions(t *testing.T) {
	p, c := makeProxy(t)
	var calls atomic.Int32
	p.transport = roundTripFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return response("ok"), nil })
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"tools":[{"type":"namespace","name":"mcp__group","tools":[{"type":"function","name":"group_exec","parameters":{}}]}]}`, 200},
		{`{"tools":[{"type":"function","name":"update_plan","parameters":{}}]}`, 200},
		{`{"tools":[{"type":"mcp","server_label":"gmail","server_url":"https://example.com"}]}`, 403},
		{`{"tools":[{"type":"function","name":"exec_command","parameters":{}}]}`, 403},
		{`{"tools":[{"type":"namespace","name":"mcp__group","tools":[{"type":"function","name":"read_auth","parameters":{}}]}]}`, 403},
		{`{"tools":[{"type":"web_search"}]}`, 403}, {`{"previous_response_id":"foreign"}`, 403},
	} {
		res := post(t, c, "https://chatgpt.example.com/backend-api/codex/responses", tc.body)
		io.Copy(io.Discard, res.Body)
		if res.StatusCode != tc.status {
			t.Fatalf("status %d want %d", res.StatusCode, tc.status)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected upstream count")
	}
}
func TestOuterProxyRequiresCapabilityAndExactHost(t *testing.T) {
	p, _ := makeProxy(t)
	for _, tc := range []struct{ method, target, token string }{
		{"GET", "http://api.example.com/v1/messages", testConfig().Token},
		{"CONNECT", "api.example.com:443", "wrong"},
		{"CONNECT", "127.0.0.1:443", testConfig().Token},
		{"CONNECT", "[::1]:443", testConfig().Token},
		{"CONNECT", "api.example.com:80", testConfig().Token},
		{"CONNECT", "api.example.com.:443", testConfig().Token},
		{"CONNECT", "private.example.com:443", testConfig().Token},
	} {
		req := httptest.NewRequest(tc.method, tc.target, nil)
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("group:"+tc.token)))
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Fatalf("%s got %d", tc.target, rec.Code)
		}
	}
}
func TestPublicAddresses(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "172.17.0.1", "192.168.1.1", "100.100.100.200", "192.0.2.1", "198.18.0.1", "0.0.0.0", "224.0.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "64:ff9b::a00:1", "2002:7f00:1::1", "2001:db8::1"} {
		if PublicAddress(net.ParseIP(s)) {
			t.Errorf("allowed %s", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !PublicAddress(net.ParseIP(s)) {
			t.Errorf("blocked public %s", s)
		}
	}
}
func TestRedirectResponseAndLimits(t *testing.T) {
	for _, status := range []int{302, 101} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p, c := makeProxy(t)
			p.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				r := response("x")
				r.StatusCode = status
				r.Header.Set("Location", "http://127.0.0.1/private")
				return r, nil
			})
			res := post(t, c, "https://api.example.com/v1/messages", `{}`)
			if res.StatusCode != 502 {
				t.Fatal(res.StatusCode)
			}
		})
	}
	t.Run("request", func(t *testing.T) {
		p, c := makeProxy(t)
		p.maxRequestBytes = 10
		res := post(t, c, "https://api.example.com/v1/messages", `{"long":"xxxxxxxxxxxxxxxxx"}`)
		if res.StatusCode != 403 {
			t.Fatal(res.StatusCode)
		}
	})
	t.Run("response", func(t *testing.T) {
		p, c := makeProxy(t)
		p.maxResponseBytes = 3
		p.transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return response("long response"), nil })
		res := post(t, c, "https://api.example.com/v1/messages", `{}`)
		if res.StatusCode != 502 {
			t.Fatal(res.StatusCode)
		}
	})
}
func TestDisconnectCancelsUpstream(t *testing.T) {
	p, c := makeProxy(t)
	started, stopped := make(chan struct{}), make(chan struct{})
	p.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(stopped)
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.example.com/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, _ := c.Do(req)
		if res != nil {
			res.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("disconnected request left upstream running")
	}
	<-done
}
func TestClosedProxyStopsInFlight(t *testing.T) {
	p, c := makeProxy(t)
	started, stopped := make(chan struct{}), make(chan struct{})
	p.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(stopped)
		return nil, errors.New("secret upstream failure")
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequest("POST", "https://api.example.com/v1/messages", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		res, _ := c.Do(req)
		if res != nil {
			res.Body.Close()
		}
	}()
	<-started
	p.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("proxy Close did not cancel upstream")
	}
	<-done
}
func TestCAReuseAndImmutableConfig(t *testing.T) {
	c := testConfig()
	cert, key, err := GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	c.CACertificatePEM, c.CAPrivateKeyPEM = cert, key
	p, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if string(p.CertificatePEM()) != string(cert) {
		t.Fatal("CA not reused")
	}
	c.Routes[0].Path = "/account"
	if p.routes[0].Path != "/v1/messages" {
		t.Fatal("mutable routes")
	}
	for _, c := range []Config{{}, {Token: strings.Repeat("a", 32)}, {Token: strings.Repeat("a", 32), Routes: []Route{{Host: "127.0.0.1", Method: "GET", Path: "/", Body: NoBody}}}} {
		if _, err := New(c); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}

func TestInferenceCannotFetchAccountContext(t *testing.T) {
	p, c := makeProxy(t)
	p.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("remote context reached upstream")
		return nil, nil
	})
	for _, body := range []string{`{"input":[{"type":"item_reference","id":"private"}]}`, `{"input":[{"type":"input_file","file_id":"private"}]}`, `{"input":[{"type":"input_file","file_url":"https://private"}]}`, `{"input":[{"type":"input_image","image_url":"https://private"}]}`, `{"unreviewed_future_feature":true}`} {
		res := post(t, c, "https://chatgpt.example.com/backend-api/codex/responses", body)
		if res.StatusCode != 403 {
			t.Fatal(res.StatusCode)
		}
	}
}
func TestExactQueryAndAuthRefreshAreOptIn(t *testing.T) {
	c := testConfig()
	c.Routes = []Route{{Host: "auth.example.com", Method: "POST", Path: "/token", Query: "", Body: ExactJSON, JSONFields: []string{"grant_type", "refresh_token", "client_id"}}}
	p, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, tc := range []struct {
		body  string
		valid bool
	}{{`{"grant_type":"refresh_token","refresh_token":"secret-canary","client_id":"group"}`, true}, {`{"grant_type":"password","password":"secret"}`, false}, {`{"refresh_token":"a","refresh_token":"b"}`, false}} {
		r := httptest.NewRequest("POST", "https://auth.example.com/token", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		_, err := p.validateBody(r, c.Routes[0])
		if (err == nil) != tc.valid {
			t.Fatal("wrong refresh field policy")
		}
	}
}
func TestHeaderGuardAndJSONDepth(t *testing.T) {
	reader := &headerGuardReader{reader: strings.NewReader("GET / HTTP/1.1\r\nX-Long: " + strings.Repeat("x", 70<<10) + "\r\n\r\n")}
	if _, err := io.ReadAll(reader); err == nil {
		t.Fatal("unbounded headers")
	}
	if _, err := decodeObject([]byte(`{"deep":` + strings.Repeat(`[`, 70) + `0` + strings.Repeat(`]`, 70) + `}`)); err == nil {
		t.Fatal("accepted invalid JSON")
	}
}

func TestDNSAllAnswersValidatedAndDialPinned(t *testing.T) {
	calls := 0
	dialled := ""
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		calls++
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}, {IP: net.ParseIP("10.0.0.1")}}, nil
	}
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		dialled = address
		return nil, errors.New("synthetic")
	}
	if _, err := dialPublic(context.Background(), "api.example.com:443", lookup, dial); err == nil || dialled != "" {
		t.Fatal("mixed private DNS answer reached socket")
	}
	lookup = func(context.Context, string) ([]net.IPAddr, error) {
		calls++
		return []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}}, nil
	}
	calls = 0
	_, _ = dialPublic(context.Background(), "api.example.com:443", lookup, dial)
	if calls != 1 || dialled != "1.1.1.1:443" {
		t.Fatal("connection could re-resolve hostname")
	}
	if publicTransport().Proxy != nil {
		t.Fatal("upstream proxy environment inherited")
	}
}
func TestSSEUnknownLengthStreamBound(t *testing.T) {
	p, c := makeProxy(t)
	p.maxResponseBytes = 5
	p.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		r := response("0123456789")
		r.ContentLength = -1
		return r, nil
	})
	res := post(t, c, "https://api.example.com/v1/messages", `{}`)
	data, err := io.ReadAll(res.Body)
	if err == nil || len(data) > 5 {
		t.Fatalf("stream not bounded bytes=%d err=%v", len(data), err)
	}
}
func TestUpstreamErrorsNeverDiscloseCredentials(t *testing.T) {
	p, c := makeProxy(t)
	p.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("credential-canary personal-path upstream error")
	})
	res := post(t, c, "https://api.example.com/v1/messages", `{}`)
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != 502 || strings.Contains(string(data), "canary") || strings.Contains(string(data), "personal") {
		t.Fatalf("unsafe upstream error: %q", data)
	}
}

func TestRequiredMCPToolsEveryInferenceRequest(t *testing.T) {
	config := testConfig()
	config.AllowedTools = append(config.AllowedTools, "mcp__group__group_request")
	config.RequiredTools = []string{"mcp__group__group_exec", "mcp__group/group_request"}
	p, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, tc := range []struct {
		body   string
		policy BodyPolicy
		valid  bool
	}{
		{`{}`, Responses, false},
		{`{"tools":[]}`, Responses, false},
		{`{"tools":[{"type":"function","name":"mcp__group__group_exec"}]}`, Responses, false},
		{`{"tools":[{"type":"namespace","name":"mcp__group","tools":[{"type":"function","name":"group_exec"},{"type":"function","name":"group_request"}]}]}`, Responses, true},
		{`{"tools":[{"type":"function","name":"mcp__group/group_exec"},{"type":"function","name":"mcp__group__group_request"}],"client_metadata":{}}`, Responses, true},
		{`{"tools":[{"name":"mcp__group__group_exec"},{"name":"mcp__group__group_request"}]}`, AnthropicMessages, true},
		{`{"tools":[{"name":"mcp__group__group_exec"},{"name":"mcp__group__group_request","defer_loading":true}]}`, AnthropicMessages, false},
		{`{"tools":[{"name":"mcp__group__group_exec"},{"name":"mcp__group__group_request"}],"tool_choice":{"type":"none"}}`, AnthropicMessages, false},
		{`{"tools":[{"type":"function","name":"mcp__group__group_exec"},{"type":"function","name":"mcp__group__group_request"}],"tool_choice":"none"}`, Responses, false},
	} {
		r := httptest.NewRequest("POST", "https://api.example.com/v1/messages", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		_, err := p.validateBody(r, Route{Body: tc.policy})
		if (err == nil) != tc.valid {
			t.Errorf("valid=%v body=%s err=%v", tc.valid, tc.body, err)
		}
	}
	config.RequiredTools = []string{"foreign"}
	if _, err := New(config); err == nil {
		t.Fatal("required ungranted tool accepted")
	}
}
func TestWebSocketUpgradeOnlySignalsFallbackOnGrantedInference(t *testing.T) {
	p, c := makeProxy(t)
	p.transport = roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("WebSocket reached upstream"); return nil, nil })
	for _, tc := range []struct {
		address string
		status  int
	}{{"https://chatgpt.example.com/backend-api/codex/responses", 426}, {"https://chatgpt.example.com/account", 403}, {"https://api.example.com/v1/messages", 403}} {
		r, err := http.NewRequest("GET", tc.address, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Connection", "Upgrade")
		res, err := c.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("%s status=%d", tc.address, res.StatusCode)
		}
	}
}

func TestSharedSubscriptionCannotFallBackToAPIKeyOrAnonymousAuth(t *testing.T) {
	p, c := makeProxy(t)
	p.requireSubscriptionBearer = true
	var calls atomic.Int32
	p.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer subscription-canary" {
			t.Error("subscription bearer changed")
		}
		return response("ok"), nil
	})
	for _, tc := range []struct {
		name, authorization, apiKey string
		duplicate                   bool
		status                      int
	}{
		{name: "subscription", authorization: "Bearer subscription-canary", status: 200},
		{name: "anonymous", status: 403}, {name: "basic", authorization: "Basic unsafe", status: 403},
		{name: "empty bearer", authorization: "Bearer ", status: 403},
		{name: "API key", apiKey: "synthetic-api-key", status: 403},
		{name: "API key alongside OAuth", authorization: "Bearer subscription-canary", apiKey: "synthetic-api-key", status: 403},
		{name: "duplicate bearer", authorization: "Bearer subscription-canary", duplicate: true, status: 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest("POST", "https://api.example.com/v1/messages", strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			if tc.authorization != "" {
				r.Header.Set("Authorization", tc.authorization)
			}
			if tc.apiKey != "" {
				r.Header.Set("X-Api-Key", tc.apiKey)
			}
			if tc.duplicate {
				r.Header.Add("Authorization", "Bearer other")
			}
			res, err := c.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode != tc.status {
				t.Fatalf("status=%d", res.StatusCode)
			}
		})
	}
	if calls.Load() != 1 {
		t.Fatal("disallowed authentication reached inference upstream")
	}
	cfg := testConfig()
	cfg.RequireSubscriptionBearer = true
	q, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if !q.requireSubscriptionBearer {
		t.Fatal("subscription guard lost from configuration")
	}
}
