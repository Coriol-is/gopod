package main

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Coriol-is/gopod/internal/isolationproxy"
)

func TestCONNECTRevalidatesCurrentLeaseAfterLocalAuthentication(t *testing.T) {
	token := strings.Repeat("a", 64)
	var pings, forwarded atomic.Int32
	var revoked atomic.Bool
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pings.Add(1)
		data, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.Path != "/mcp/"+token || string(data) != `{"jsonrpc":"2.0","id":"egress","method":"ping"}` || r.Header.Get("Authorization") != "" {
			t.Error("nonfixed ping request")
		}
		if revoked.Load() {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"jsonrpc":"2.0","id":"egress","result":{}}`)
	}))
	defer controller.Close()
	target, _ := url.Parse(controller.URL + "/mcp/" + token)
	gate := gateCONNECT(token, target, newPingClient(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1); w.WriteHeader(204) }))
	request := func(cap string) int {
		r := httptest.NewRequest("CONNECT", "api.example.com:443", nil)
		r.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("group:"+cap)))
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		return w.Code
	}
	if request("wrong") != 403 || pings.Load() != 0 {
		t.Fatal("unauthenticated client reached controller")
	}
	if request(token) != 204 || forwarded.Load() != 1 || pings.Load() != 1 {
		t.Fatal("current lease failed positive control")
	}
	revoked.Store(true)
	if request(token) != 403 || forwarded.Load() != 1 || pings.Load() != 2 {
		t.Fatal("revoked lease authorized more inference")
	}
}
func TestLeasePingRejectsBusyMalformedOversizeAndRedirects(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
	}{{"busy", 429, `{}`}, {"malformed", 200, `not json`}, {"rpc error", 200, `{"jsonrpc":"2.0","id":"egress","error":{"code":-1}}`}, {"oversize", 200, strings.Repeat("x", 1025)}, {"wrong id", 200, `{"jsonrpc":"2.0","id":"other","result":{}}`}} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code); io.WriteString(w, tc.body) }))
			defer s.Close()
			u, _ := url.Parse(s.URL)
			if leaseActive(context.Background(), u, newPingClient()) {
				t.Fatal("invalid ping allowed")
			}
		})
	}
	var followed atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Store(true)
		io.WriteString(w, `{"jsonrpc":"2.0","id":"egress","result":{}}`)
	}))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 307) }))
	defer redirect.Close()
	u, _ := url.Parse(redirect.URL)
	if leaseActive(context.Background(), u, newPingClient()) || followed.Load() {
		t.Fatal("ping followed redirect")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if leaseActive(ctx, u, newPingClient()) {
		t.Fatal("cancelled ping allowed")
	}
	if newPingClient().Transport.(*http.Transport).Proxy != nil {
		t.Fatal("ping inherited proxy environment")
	}
}
func TestFixedControllerMCPRouteRejectsRedirectableIdentity(t *testing.T) {
	token := strings.Repeat("a", 64)
	cfg := relayConfig{Proxy: isolationproxy.Config{Token: token}, MCPURL: "http://host.docker.internal:9877/mcp/" + token}
	if _, err := fixedMCPTarget(cfg); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"http://elsewhere:9877/mcp/" + token, cfg.MCPURL + "?other=1", cfg.MCPURL + "#fragment", strings.Replace(cfg.MCPURL, "/mcp/", "/mcp/%61", 1), "http://host.docker.internal:9877/mcp/" + strings.Repeat("b", 64)} {
		cfg.MCPURL = target
		if _, err := fixedMCPTarget(cfg); err == nil {
			t.Fatalf("accepted target %q", target)
		}
	}
}

func TestGateObserverNeverLeaksAuthenticationOrLeasePayload(t *testing.T) {
	token := strings.Repeat("a", 64)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "secret-upstream-body", 403) }))
	defer controller.Close()
	target, _ := url.Parse(controller.URL)
	var events []string
	gate := gateCONNECT(token, target, newPingClient(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("denial forwarded") }), func(event string) { events = append(events, event) })
	for _, cap := range []string{"secret-malicious-key", token} {
		r := httptest.NewRequest("CONNECT", "malicious-host.example:443", nil)
		r.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("group:"+cap)))
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	if strings.Join(events, ",") != "connect_auth_denied,connect_lease_denied" {
		t.Fatal(events)
	}
}
