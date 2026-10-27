package isolationproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDiagnosticsRemovedBeforeUpstream(t *testing.T) {
	p, client := makeProxy(t)
	p.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if _, exists := got["diagnostics"]; exists || strings.Contains(string(raw), "private-comparison-id") {
			t.Fatal("diagnostics reached upstream")
		}
		if got["model"] != "selected" || got["max_tokens"] != float64(3) || got["stream"] != true || len(got["messages"].([]any)) != 1 {
			t.Fatal("generation envelope changed")
		}
		return response("ok"), nil
	})
	res := post(t, client, "https://api.example.com/v1/messages", `{"model":"selected","max_tokens":3,"stream":true,"messages":[{"role":"user","content":"hello"}],"diagnostics":{"previous_message_id":"private-comparison-id"}}`)
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
}

func TestDiagnosticsSchemaAndProviderBoundary(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		policy BodyPolicy
		deny   bool
	}{
		{`null`, AnthropicMessages, false},
		{`{}`, AnthropicMessages, false},
		{`{"previous_message_id":null}`, AnthropicMessages, false},
		{`{"previous_message_id":"valid"}`, AnthropicMessages, false},
		{`{"previous_message_id":1}`, AnthropicMessages, true},
		{`{"previous_message_id":{}}`, AnthropicMessages, true},
		{`{"unknown":"secret"}`, AnthropicMessages, true},
		{`[]`, AnthropicMessages, true},
		{`"secret"`, AnthropicMessages, true},
		{`null`, Responses, true},
		{`{"previous_message_id":"valid"}`, Responses, true},
		{`{"previous_message_id":"` + strings.Repeat("x", 257) + `"}`, AnthropicMessages, true},
	} {
		t.Run(string(tc.policy)+tc.raw, func(t *testing.T) {
			cfg := testConfig()
			p, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			r, _ := http.NewRequest("POST", "https://api.example.com", strings.NewReader(`{"model":"selected","diagnostics":`+tc.raw+`}`))
			r.Header.Set("Content-Type", "application/json")
			out, err := p.validateBody(r, Route{Body: tc.policy})
			if (err != nil) != tc.deny {
				t.Fatal(err)
			}
			if err == nil && strings.Contains(string(out), "diagnostics") {
				t.Fatal("not stripped")
			}
		})
	}
}
