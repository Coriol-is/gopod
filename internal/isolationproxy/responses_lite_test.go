package isolationproxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestResponsesLiteFullInventoryEnforcesSameBoundary(t *testing.T) {
	cfg := testConfig()
	cfg.AllowedTools = []string{"mcp__group__group_exec", "mcp__group__group_request", "update_plan"}
	cfg.RequiredTools = cfg.AllowedTools[:2]
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	namespace := map[string]any{"type": "namespace", "name": "mcp__group", "tools": []any{map[string]any{"type": "function", "name": "group_exec"}, map[string]any{"type": "function", "name": "group_request"}}}
	prefix := func() map[string]any {
		return map[string]any{"type": "additional_tools", "role": "developer", "id": "at_00000000-0000-4000-8000-000000000000", "tools": []any{namespace}}
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		deny   bool
	}{
		{"full current inventory", func(map[string]any) {}, false},
		{"safe builtin namespace", func(v map[string]any) {
			a := v["input"].([]any)[0].(map[string]any)
			a["tools"] = append(a["tools"].([]any), map[string]any{"type": "namespace", "name": "functions", "tools": []any{map[string]any{"type": "function", "name": "update_plan"}}})
		}, false},
		{"unknown native tool", func(v map[string]any) {
			a := v["input"].([]any)[0].(map[string]any)
			a["tools"] = append(a["tools"].([]any), map[string]any{"type": "namespace", "name": "functions", "tools": []any{map[string]any{"type": "function", "name": "exec_command"}}})
		}, true},
		{"wrong role", func(v map[string]any) { v["input"].([]any)[0].(map[string]any)["role"] = "user" }, true},
		{"wrong id", func(v map[string]any) { v["input"].([]any)[0].(map[string]any)["id"] = "secret" }, true},
		{"unknown wrapper grant", func(v map[string]any) { v["input"].([]any)[0].(map[string]any)["server_url"] = "https://example.com" }, true},
		{"duplicate", func(v map[string]any) { v["input"] = append(v["input"].([]any), prefix()) }, true},
		{"nested", func(v map[string]any) {
			v["input"] = []any{map[string]any{"type": "message", "content": []any{prefix()}}}
		}, true},
		{"top level ambiguity", func(v map[string]any) { v["tools"] = []any{namespace} }, true},
		{"hosted tool", func(v map[string]any) {
			v["input"].([]any)[0].(map[string]any)["tools"] = []any{map[string]any{"type": "web_search"}}
		}, true},
		{"missing required", func(v map[string]any) {
			v["input"].([]any)[0].(map[string]any)["tools"] = []any{map[string]any{"type": "function", "name": "update_plan"}}
		}, true},
		{"remote context", func(v map[string]any) {
			v["input"] = append(v["input"].([]any), map[string]any{"type": "item_reference", "id": "account-id"})
		}, true},
		{"disabled", func(v map[string]any) { v["tool_choice"] = "none" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := map[string]any{"model": "selected", "input": []any{prefix()}, "stream": true, "tool_choice": "auto"}
			tc.mutate(v)
			raw, _ := json.Marshal(v)
			r, _ := http.NewRequest("POST", "https://api.example.com/responses", strings.NewReader(string(raw)))
			r.Header.Set("Content-Type", "application/json")
			_, err := p.validateBody(r, Route{Body: Responses})
			if (err != nil) != tc.deny {
				t.Fatalf("denial=%v err=%v", tc.deny, err)
			}
		})
	}
}

func TestUnapprovedFieldObserverNeverEmitsSubmittedNames(t *testing.T) {
	for _, field := range []string{"cache_control", "previous_message_id", "inference_geo", "speed", "PRIVATE_UNKNOWN_FIELD"} {
		cfg := testConfig()
		var events []string
		cfg.Observe = func(e string) { events = append(events, e) }
		p, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"model": "synthetic", field: "PRIVATE_VALUE"})
		r, _ := http.NewRequest("POST", "https://api.example.com/messages", strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		_, err = p.validateBody(r, Route{Body: AnthropicMessages})
		p.Close()
		want := "field_" + field
		if field == "PRIVATE_UNKNOWN_FIELD" {
			want = "field_other"
		}
		if err == nil || len(events) != 1 || events[0] != want {
			t.Fatalf("field observer %v %v", events, err)
		}
	}
}
