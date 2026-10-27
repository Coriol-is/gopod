package isolationproxy

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestInventoryObserverFixedClasses(t *testing.T) {
	for _, tc := range []struct {
		tools, choice, want string
		denied              bool
	}{
		{`[]`, `"auto"`, "inventory_both_missing", true},
		{`[{"name":"mcp__group__group_exec"}]`, `"auto"`, "inventory_request_missing", true},
		{`[{"name":"mcp__group__group_request"}]`, `"auto"`, "inventory_exec_missing", true},
		{`[{"name":"mcp__group__group_exec"},{"name":"mcp__group__group_request"}]`, `"auto"`, "inventory_both_present", false},
		{`[{"name":"mcp__group__group_exec"},{"name":"mcp__group__group_request"}]`, `"none"`, "inventory_both_present,required_tools_disabled", true},
	} {
		t.Run(tc.want+tc.choice, func(t *testing.T) {
			cfg := testConfig()
			cfg.AllowedTools = append(cfg.AllowedTools, "mcp__group__group_request")
			cfg.RequiredTools = []string{"mcp__group__group_exec", "mcp__group__group_request"}
			var events []string
			cfg.Observe = func(e string) { events = append(events, e) }
			p, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			r, _ := http.NewRequest("POST", "https://api.example.com/v1/messages", io.NopCloser(strings.NewReader(`{"model":"secret-model","messages":[],"tools":`+tc.tools+`,"tool_choice":`+tc.choice+`}`)))
			r.Header.Set("Content-Type", "application/json")
			_, err = p.validateBody(r, cfg.Routes[0])
			if (err != nil) != tc.denied {
				t.Fatal("unexpected policy result", err)
			}
			prefix := "tools_nonempty,"
			if tc.tools == "[]" {
				prefix = "tools_empty,"
			}
			if strings.Join(events, ",") != prefix+tc.want {
				t.Fatal(events)
			}
		})
	}
}

func TestKnownToolDiagnosticsPreserveRequiredIdentity(t *testing.T) {
	cfg := testConfig()
	cfg.AllowedTools = append(cfg.AllowedTools, "group_exec", "group_request", "mcp__group__group_request")
	var events []string
	cfg.Observe = func(e string) { events = append(events, e) }
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	seen := map[string]bool{}
	for _, name := range []string{"group_exec", "group_request", "mcp__group__group_exec", "mcp__group__group_request"} {
		if !p.toolAllowed(map[string]any{"name": name, "defer_loading": true}, "", AnthropicMessages, seen) {
			t.Fatal("fixture denied")
		}
	}
	want := "tool_bare_exec,tool_exec_deferred,tool_bare_request,tool_request_deferred,tool_exec_deferred,tool_request_deferred"
	if strings.Join(events, ",") != want || len(seen) != 0 {
		t.Fatal(events, seen)
	}
	events = nil
	if p.toolAllowed(map[string]any{"name": "private-secret-tool", "defer_loading": true}, "", AnthropicMessages, seen) || len(events) != 0 {
		t.Fatal("unknown tool observed")
	}
}
