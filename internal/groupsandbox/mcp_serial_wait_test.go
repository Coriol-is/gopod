package groupsandbox

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serialLease(t *testing.T, request func(context.Context, json.RawMessage) (string, error)) (*MCP, string, *Lease) {
	t.Helper()
	m := &MCP{}
	token, revoke, err := m.Grant(t.Context(), Lease{Scope: "scope-serial", Expires: time.Now().Add(time.Minute), Validate: func(context.Context) error { return nil }, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revoke)
	return m, token, m.leases[token]
}
func serialRPC(m *MCP, token, method, params string, ctx context.Context) <-chan int {
	done := make(chan int, 1)
	go func() {
		body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `"`
		if params != "" {
			body += `,"params":` + params
		}
		body += `}`
		req := httptest.NewRequest("POST", "/mcp/"+token, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		m.ServeHTTP(w, req)
		done <- w.Code
	}()
	return done
}
func TestMCPConcurrentDiscoveryWaitsForActiveRPC(t *testing.T) {
	m, token, l := serialLease(t, nil)
	l.busy <- struct{}{}
	var calls []<-chan int
	for _, method := range []string{"initialize", "tools/list", "ping"} {
		calls = append(calls, serialRPC(m, token, method, "", t.Context()))
	}
	for _, done := range calls {
		select {
		case code := <-done:
			t.Fatalf("RPC returned before serialization release: %d", code)
		case <-time.After(20 * time.Millisecond):
		}
	}
	<-l.busy
	for _, done := range calls {
		select {
		case code := <-done:
			if code != 200 {
				t.Fatalf("discovery status %d", code)
			}
		case <-time.After(time.Second):
			t.Fatal("RPC did not settle")
		}
	}
}
func TestMCPRevokedWaitingRPCNeverRuns(t *testing.T) {
	var calls atomic.Int32
	m, token, l := serialLease(t, func(context.Context, json.RawMessage) (string, error) { calls.Add(1); return "unexpected", nil })
	l.busy <- struct{}{}
	done := serialRPC(m, token, "tools/call", `{"name":"group_request","arguments":{}}`, t.Context())
	select {
	case code := <-done:
		t.Fatalf("waiter returned prematurely: %d", code)
	case <-time.After(20 * time.Millisecond):
	}
	revoked := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		revoked <- m.RevokeAndWait(ctx, token)
	}()
	select {
	case code := <-done:
		if code != 403 {
			t.Fatalf("revoked status %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("revocation blocked on registry lock")
	}
	<-l.busy
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("revoked request executed")
	}
}
func TestMCPCanceledWaitingRPCNeverRuns(t *testing.T) {
	m, token, l := serialLease(t, nil)
	l.busy <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	done := serialRPC(m, token, "ping", "", ctx)
	cancel()
	select {
	case code := <-done:
		if code != 403 {
			t.Fatalf("canceled status %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not settle")
	}
	<-l.busy
}
