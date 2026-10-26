package groupruntime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Coriol-is/gopod/internal/isolationproxy"
)

func TestReadonlySubscriptionConfigOnlyPermitsPinnedInferenceRoutes(t *testing.T) {
	claude := isolationproxy.Route{Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages", Body: isolationproxy.AnthropicMessages}
	codex := isolationproxy.Route{Host: "chatgpt.com", Method: "POST", Path: "/backend-api/codex/responses", Body: isolationproxy.Responses}
	models := isolationproxy.Route{Host: "chatgpt.com", Method: "GET", Path: "/backend-api/codex/models", Query: "client_version=0.164.0", Body: isolationproxy.NoBody}
	load := func(provider string, routes []isolationproxy.Route) error {
		cfg := Config{EnabledGroups: []string{"tg:-2"}, RelayImage: "relay:pinned", ToolImage: "tools:pinned", Qualification: "synthetic-qualified", Providers: map[string]ProviderConfig{provider: {Image: "group:pinned", AuthFile: "/explicit/readonly-auth.json", Routes: routes}}}
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "group.json")
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		_, err = Load(path)
		return err
	}
	beta := claude
	beta.Query = "beta=true"
	for _, tc := range []struct {
		name, provider string
		routes         []isolationproxy.Route
		valid          bool
	}{
		{"Claude inference", "claude", []isolationproxy.Route{claude, beta}, true},
		{"Codex inference", "codex", []isolationproxy.Route{codex, models}, true},
		{"Claude refresh rotates readonly owner token", "claude", []isolationproxy.Route{{Host: "claude.ai", Method: "POST", Path: "/v1/oauth/token", Body: isolationproxy.ExactJSON, JSONFields: []string{"refresh_token"}}}, false},
		{"Claude account access", "claude", []isolationproxy.Route{{Host: "api.anthropic.com", Method: "GET", Path: "/api/oauth/profile", Body: isolationproxy.NoBody}}, false},
		{"Codex refresh rotates readonly owner token", "codex", []isolationproxy.Route{codex, {Host: "auth.openai.com", Method: "POST", Path: "/oauth/token", Body: isolationproxy.ExactJSON, JSONFields: []string{"refresh_token"}}}, false},
		{"API billing fallback", "codex", []isolationproxy.Route{{Host: "api.openai.com", Method: "POST", Path: "/v1/responses", Body: isolationproxy.Responses}}, false},
		{"account service", "codex", []isolationproxy.Route{codex, {Host: "chatgpt.com", Method: "GET", Path: "/backend-api/settings", Body: isolationproxy.NoBody}}, false},
		{"unqualified model query", "codex", []isolationproxy.Route{codex, {Host: "chatgpt.com", Method: "GET", Path: "/backend-api/codex/models", Body: isolationproxy.NoBody}}, false},
		{"metadata without inference", "codex", []isolationproxy.Route{models}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := load(tc.provider, tc.routes)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
			if err != nil && !errors.Is(err, ErrUnavailable) {
				t.Fatal("unsafe config error")
			}
		})
	}
}
