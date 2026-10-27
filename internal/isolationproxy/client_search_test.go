package isolationproxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func searchDescriptor() map[string]any {
	return map[string]any{"type": "tool_search", "execution": "client", "description": "Search local tools", "parameters": map[string]any{"type": "object", "properties": map[string]any{"limit": map[string]any{"type": "number"}, "query": map[string]any{"type": "string"}}, "required": []any{"query"}, "additionalProperties": false}}
}
func TestClientSearchExplicitHostCatalogBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		mutate  func(map[string]any)
		deny    bool
	}{
		{"default disabled", false, func(map[string]any) {}, true},
		{"explicit initial catalog", true, func(map[string]any) {}, false},
		{"server search", true, func(v map[string]any) { v["tools"].([]any)[0].(map[string]any)["execution"] = "server" }, true},
		{"extra search selector", true, func(v map[string]any) {
			v["tools"].([]any)[0].(map[string]any)["server_url"] = "https://remote.example"
		}, true},
		{"unsafe schema", true, func(v map[string]any) {
			v["tools"].([]any)[0].(map[string]any)["parameters"].(map[string]any)["additionalProperties"] = true
		}, true},
		{"missing search", true, func(v map[string]any) { v["tools"] = []any{} }, true},
		{"duplicate search", true, func(v map[string]any) { v["tools"] = append(v["tools"].([]any), searchDescriptor()) }, true},
		{"discovered pair", true, func(v map[string]any) { v["input"] = []any{searchOutput("mcp__group", "group_exec")} }, false},
		{"foreign discovered namespace", true, func(v map[string]any) { v["input"] = []any{searchOutput("mcp__personal", "group_exec")} }, true},
		{"deferred foreign granted tool", true, func(v map[string]any) {
			o := searchOutput("mcp__group", "extra")
			ns := o["tools"].([]any)[0].(map[string]any)
			ns["tools"].([]any)[0].(map[string]any)["defer_loading"] = true
			v["input"] = []any{o}
		}, true},
		{"native discovered tool", true, func(v map[string]any) { v["input"] = []any{searchOutput("mcp__group", "exec_command")} }, true},
		{"client call history", true, func(v map[string]any) {
			v["input"] = []any{map[string]any{"type": "tool_search_call", "execution": "client", "call_id": "call_synthetic", "arguments": map[string]any{"query": "group tools", "limit": 2}, "status": "completed"}}
		}, false},
		{"server call history", true, func(v map[string]any) {
			v["input"] = []any{map[string]any{"type": "tool_search_call", "execution": "server", "call_id": "call_synthetic", "arguments": map[string]any{"query": "group tools"}}}
		}, true},
		{"remote call selector", true, func(v map[string]any) {
			v["input"] = []any{map[string]any{"type": "tool_search_call", "execution": "client", "call_id": "call_synthetic", "arguments": map[string]any{"query": "group tools", "server_url": "https://remote.example"}}}
		}, true},
		{"server output", true, func(v map[string]any) {
			o := searchOutput("mcp__group", "group_exec")
			o["execution"] = "server"
			v["input"] = []any{o}
		}, true},
		{"nested output", true, func(v map[string]any) {
			v["input"] = []any{map[string]any{"type": "message", "content": []any{searchOutput("mcp__group", "group_exec")}}}
		}, true},
		{"remote context", true, func(v map[string]any) { v["input"] = []any{map[string]any{"type": "item_reference", "id": "remote"}} }, true},
		{"disabled choice", true, func(v map[string]any) { v["tool_choice"] = "none" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.AllowedTools = []string{"mcp__group__group_exec", "mcp__group__group_request", "mcp__group.extra"}
			cfg.RequiredTools = cfg.AllowedTools[:2]
			cfg.AllowClientToolSearch = tc.enabled
			p, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			value := map[string]any{"model": "synthetic", "tools": []any{searchDescriptor()}, "input": []any{}, "tool_choice": "auto"}
			tc.mutate(value)
			raw, _ := json.Marshal(value)
			r, _ := http.NewRequest("POST", "https://api.example.com/responses", strings.NewReader(string(raw)))
			r.Header.Set("Content-Type", "application/json")
			_, err = p.validateBody(r, Route{Body: Responses})
			if (err != nil) != tc.deny {
				t.Fatalf("denial=%v err=%v", tc.deny, err)
			}
		})
	}
	cfg := testConfig()
	cfg.AllowClientToolSearch = true
	if p, err := New(cfg); err == nil {
		p.Close()
		t.Fatal("search without exact required host catalog accepted")
	}
}
func searchOutput(namespace, name string) map[string]any {
	return map[string]any{"type": "tool_search_output", "execution": "client", "status": "completed", "call_id": "call_synthetic", "tools": []any{map[string]any{"type": "namespace", "name": namespace, "tools": []any{map[string]any{"type": "function", "name": name}}}}}
}

func TestClientSearchCallLimitsAndOpaqueHistoryFailClosed(t *testing.T) {
	for _, extra := range []string{`,"limit":0`, `,"limit":1.5`, `,"limit":101`} {
		var call map[string]any
		decoder := json.NewDecoder(strings.NewReader(`{"type":"tool_search_call","execution":"client","call_id":"synthetic","arguments":{"query":"group"` + extra + `}}`))
		decoder.UseNumber()
		if err := decoder.Decode(&call); err != nil {
			t.Fatal(err)
		}
		if validClientSearchCall(call) {
			t.Fatal("invalid local search limit accepted")
		}
	}
	if validClientSearchCall(map[string]any{"type": "tool_search_call", "execution": "client", "call_id": "synthetic", "arguments": map[string]any{"query": "group"}, "encrypted_content": "opaque"}) {
		t.Fatal("unsupported opaque search history accepted")
	}
}
