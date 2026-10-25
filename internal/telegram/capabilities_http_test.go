package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type capabilityRoundTrip func(*http.Request) (*http.Response, error)

func (f capabilityRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func capabilityHTTPClient(status int, body string) *http.Client {
	return &http.Client{Transport: capabilityRoundTrip(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
	})}
}

const capabilityIdentity = `"id":123,"is_bot":true,"username":"test_bot"`

func TestCapabilityDecoderOfficialFields(t *testing.T) {
	tests := []struct{ name, extra, want, class string }{
		{"true", `,"supports_inline_queries":true`, "enabled", ""},
		{"false", `,"supports_inline_queries":false`, "disabled", ""},
		{"missing", "", "unknown", ""},
		{"sdk_wrong_tag", `,"support_inline_queries":true`, "unknown", ""},
		{"null", `,"supports_inline_queries":null`, "unknown", "schema"},
		{"malformed", `,"supports_inline_queries":"true","can_join_groups":true`, "unknown", "schema"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCapabilityFetcher("123:secret", capabilityHTTPClient(200, `{"ok":true,"result":{`+capabilityIdentity+tt.extra+`}}`))
			got := f(t.Context())
			if !got.valid || got.settings["supports_inline_queries"] != tt.want || got.class != tt.class {
				t.Fatal(got)
			}
			if tt.name == "malformed" && got.settings["can_join_groups"] != "enabled" {
				t.Fatal("unrelated valid flag discarded")
			}
		})
	}
}

func TestCapabilityHTTPFailuresSanitized(t *testing.T) {
	for _, tt := range []struct {
		name        string
		status      int
		body, class string
	}{
		{"bad_json", 200, `secret body`, "schema"},
		{"wrong_identity", 200, `{"ok":true,"result":{"id":124,"is_bot":true,"username":"test_bot"}}`, "schema"},
		{"not_bot", 200, `{"ok":true,"result":{"id":123,"is_bot":false,"username":"test_bot"}}`, "schema"},
		{"bad_name", 200, `{"ok":true,"result":{"id":123,"is_bot":true,"username":"secret<script>"}}`, "schema"},
		{"auth", 401, `{"ok":false,"description":"secret token"}`, "authentication"},
		{"envelope_auth", 200, `{"ok":false,"error_code":403,"description":"secret token"}`, "authentication"},
		{"server", 503, `secret body`, "api_rejection"},
		{"envelope_error", 200, `{"ok":false,"description":"secret token"}`, "api_rejection"},
		{"oversize", 200, strings.Repeat("s", (1<<20)+1), "schema"},
		{"rate_limit", 429, `{"ok":false,"parameters":{"retry_after":999999},"description":"secret"}`, "rate_limit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := newCapabilityFetcher("123:secret", capabilityHTTPClient(tt.status, tt.body))(t.Context())
			if got.class != tt.class || got.valid || strings.Contains(got.class, "secret") || got.username != "" {
				t.Fatal(got)
			}
			if tt.name == "rate_limit" && got.retryAfter != 5*time.Minute {
				t.Fatal(got.retryAfter)
			}
		})
	}
}

func TestCapabilityHTTPMethodAndTransportErrors(t *testing.T) {
	client := &http.Client{Transport: capabilityRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Method != "POST" || req.URL.Scheme != "https" || req.URL.Host != "api.telegram.org" || req.URL.Path != "/bot123:secret/getMe" {
			t.Errorf("unexpected method/endpoint")
		}
		return nil, &url.Error{Op: "POST", URL: req.URL.String(), Err: errors.New("secret body")}
	})}
	got := newCapabilityFetcher("123:secret", client)(t.Context())
	if got.class != "transport" || got.valid {
		t.Fatal(got)
	}
}

func TestCapabilityHTTPTimeoutAndRedirect(t *testing.T) {
	client := &http.Client{Transport: capabilityRoundTrip(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if got := newCapabilityFetcher("123:secret", client)(ctx); got.class != "timeout" {
		t.Fatal(got)
	}
	var calls int
	client = &http.Client{Transport: capabilityRoundTrip(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": []string{"https://evil.invalid/token"}}, Request: req}, nil
	})}
	if got := newCapabilityFetcher("123:secret", client)(t.Context()); got.class != "api_rejection" || calls != 1 {
		t.Fatal(got, calls)
	}
}
