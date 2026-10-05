package memory

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOps records the chat each call was made for.
type fakeOps struct{ chats []string }

func (f *fakeOps) Search(_ context.Context, chat, _ string, _ int) ([]Item, error) {
	f.chats = append(f.chats, chat)
	return []Item{{ID: 1, Kind: "fact", Content: "x", Score: 0.9}}, nil
}
func (f *fakeOps) Add(_ context.Context, chat, _, _, _, _ string) (int64, error) {
	f.chats = append(f.chats, chat)
	return 7, nil
}
func (f *fakeOps) List(_ context.Context, chat string, _ int) ([]Item, error) {
	f.chats = append(f.chats, chat)
	return nil, nil
}

func newTestAPI(t *testing.T) (*APIServer, *fakeOps, *Tokens) {
	t.Helper()
	ops := &fakeOps{}
	ts := NewTokens()
	s := newAPIServer(ops, "127.0.0.1:0", ts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return s, ops, ts
}

func do(s *APIServer, method, target, bearer, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(rr, req)
	return rr
}

func TestAPIRejectsMissingOrUnknownCapability(t *testing.T) {
	s, ops, _ := newTestAPI(t)
	for _, tc := range []struct{ name, bearer string }{{"missing", ""}, {"unknown", "deadbeef"}} {
		for _, target := range []string{"/memory/search?q=x", "/memory/list", "/memory/add"} {
			method := http.MethodGet
			body := ""
			if strings.HasSuffix(target, "add") {
				method, body = http.MethodPost, `{"content":"x"}`
			}
			rr := do(s, method, target, tc.bearer, body)
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with %s capability: status %d, want 401", method, target, tc.name, rr.Code)
			}
		}
	}
	if len(ops.chats) != 0 {
		t.Errorf("memory touched without a capability: %v", ops.chats)
	}
}

func TestAPIDerivesChatFromCapabilityAndIgnoresParam(t *testing.T) {
	s, ops, ts := newTestAPI(t)
	tok := ts.Mint("alice")
	rr := do(s, http.MethodGet, "/memory/search?q=hi&chat=owner", tok, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("search: %d %s", rr.Code, rr.Body.String())
	}
	rr = do(s, http.MethodPost, "/memory/add", tok, `{"chat":"owner","kind":"fact","content":"remember"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("add: %d %s", rr.Code, rr.Body.String())
	}
	var added map[string]int64
	if err := json.Unmarshal(rr.Body.Bytes(), &added); err != nil || added["id"] != 7 {
		t.Errorf("add body = %s", rr.Body.String())
	}
	rr = do(s, http.MethodGet, "/memory/list?chat=owner", tok, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rr.Code, rr.Body.String())
	}
	for i, chat := range ops.chats {
		if chat != "alice" {
			t.Errorf("call %d used chat %q from the request instead of the capability's alice", i, chat)
		}
	}
	if len(ops.chats) != 3 {
		t.Errorf("expected 3 memory calls, got %d", len(ops.chats))
	}
}

func TestAPIHealthNeedsNoCapability(t *testing.T) {
	s, _, _ := newTestAPI(t)
	if rr := do(s, http.MethodGet, "/health", "", ""); rr.Code != http.StatusOK {
		t.Errorf("/health = %d", rr.Code)
	}
}

func TestAPIRevokedCapabilityIsRejected(t *testing.T) {
	s, _, ts := newTestAPI(t)
	tok := ts.Mint("alice")
	ts.Revoke(tok)
	if rr := do(s, http.MethodGet, "/memory/list", tok, ""); rr.Code != http.StatusUnauthorized {
		t.Errorf("revoked capability: %d, want 401", rr.Code)
	}
}

// container/skills/memory/SKILL.md must carry the same instructions as
// AgentInstructions so Claude Code (skill mount) and Codex (AGENTS.md)
// learn the same calls.
func TestSkillFileMatchesAgentInstructions(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "container", "skills", "memory", "SKILL.md"))
	if err != nil {
		t.Skipf("skill file not reachable from test cwd: %v", err)
	}
	if strings.TrimSpace(string(b)) != strings.TrimSpace(AgentInstructions()) {
		t.Error("container/skills/memory/SKILL.md is out of sync with memory.AgentInstructions()")
	}
}
