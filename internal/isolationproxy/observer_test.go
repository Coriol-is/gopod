package isolationproxy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestObserverDiscardsUntrustedDetails(t *testing.T) {
	const secret = "private-key-host-tool\nforged_event"
	if got := bodyEvent(errors.New(secret)); got != "body_denied" {
		t.Fatal(got)
	}
	for _, tc := range []struct{ reason, want string }{
		{"subscription authentication required", "body_auth_denied"},
		{"required external tool unavailable", "body_required_tools_denied"},
		{"unapproved tool", "body_unapproved_tool"},
		{"unapproved inference field", "body_field_denied"},
	} {
		if got := bodyEvent(errors.New(tc.reason)); got != tc.want {
			t.Fatal(got)
		}
	}
	cfg := testConfig()
	var events []string
	cfg.Observe = func(event string) { events = append(events, event) }
	encoded, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(encoded), "Observe") {
		t.Fatalf("observer serialized: %v", err)
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r := httptest.NewRequest("CONNECT", "https://malicious.example:443/private-key-host-tool", nil)
	p.ServeHTTP(httptest.NewRecorder(), r)
	r.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("group:"+cfg.Token)))
	p.ServeHTTP(httptest.NewRecorder(), r)
	if strings.Join(events, ",") != "connect_auth_denied,connect_authority_denied" {
		t.Fatal(events)
	}
	if upstreamEvent(403) != "upstream_http_403" || upstreamEvent(-123456) != "upstream_http_other" {
		t.Fatal("unbounded status")
	}
}

func TestObserverBodyAndUpstreamClasses(t *testing.T) {
	p, client := makeProxy(t)
	events := make(chan string, 10)
	p.observe = func(event string) {
		if !strings.HasPrefix(event, "tools_") {
			events <- event
		}
	}
	p.transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		res := response("denied")
		res.StatusCode = 403
		return res, nil
	})
	res := post(t, client, "https://api.example.com/v1/messages", `{"model":"private-model","messages":[],"max_tokens":1}`)
	if res.StatusCode != 403 {
		t.Fatal(res.StatusCode)
	}
	if a, b := <-events, <-events; a != "forwarded" || b != "upstream_http_403" {
		t.Fatal(a, b)
	}
	res = post(t, client, "https://api.example.com/v1/messages", `{"model":"private-model","messages":[],"max_tokens":1,"private-secret-field":true}`)
	if res.StatusCode != 403 || <-events != "field_other" || <-events != "body_field_denied" {
		t.Fatal("body classification")
	}
}
