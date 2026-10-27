package groupsandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTimedOutDrainRetainsLeaseUntilActualSettlement(t *testing.T) {
	m := &MCP{}
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	token, _, err := m.Grant(t.Context(), Lease{Scope: "scope-stalled", Expires: time.Now().Add(time.Minute), Validate: func(context.Context) error { return nil }, Request: func(context.Context, json.RawMessage) (string, error) { close(entered); <-release; return "late", nil }})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(finished)
		m.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp/"+token, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group_request","arguments":{}}}`)))
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Millisecond)
	if err = m.RevokeAndWait(ctx, token); err == nil {
		t.Fatal("stalled callback treated as settled")
	}
	cancel()
	m.mu.RLock()
	tracked := len(m.draining["scope-stalled"])
	active := m.leases[token] != nil
	m.mu.RUnlock()
	if tracked != 1 || active {
		t.Fatal("timeout lost draining lease or kept admission active")
	}
	if _, _, err = m.Grant(t.Context(), Lease{Scope: "scope-stalled", Expires: time.Now().Add(time.Minute), Validate: func(context.Context) error { return nil }}); err == nil {
		t.Fatal("new same-scope lease admitted before settlement")
	}
	unrelated, revokeOther, err := m.Grant(t.Context(), Lease{Scope: "scope-healthy", Expires: time.Now().Add(time.Minute), Validate: func(context.Context) error { return nil }})
	if err != nil || unrelated == "" {
		t.Fatal("global map lock blocked healthy scope")
	}
	defer revokeOther()
	ctx, cancel = context.WithTimeout(t.Context(), 15*time.Millisecond)
	if err = m.DrainScope(ctx, "scope-stalled"); err == nil {
		t.Fatal("repair forgot original busy handler")
	}
	cancel()
	denied := httptest.NewRecorder()
	m.ServeHTTP(denied, httptest.NewRequest(http.MethodPost, "/mcp/"+token, strings.NewReader(`{}`)))
	if denied.Code != http.StatusForbidden {
		t.Fatal("revoked token readmitted")
	}
	close(release)
	<-finished
	ctx, cancel = context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err = m.DrainScope(ctx, "scope-stalled"); err != nil {
		t.Fatal(err)
	}
	next, revoke, err := m.Grant(t.Context(), Lease{Scope: "scope-stalled", Expires: time.Now().Add(time.Minute), Validate: func(context.Context) error { return nil }})
	if err != nil || next == "" {
		t.Fatal("settled scope remained blocked")
	}
	defer revoke()
}
