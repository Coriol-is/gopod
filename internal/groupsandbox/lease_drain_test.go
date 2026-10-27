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

func TestRevokeCancelsAndDrainsActiveRequest(t *testing.T) {
	m := &MCP{}
	entered := make(chan struct{})
	exited := make(chan struct{})
	token, revoke, err := m.Grant(context.Background(), Lease{Scope: "namespace-drain", Expires: time.Now().Add(time.Minute), Validate: func(context.Context) error { return nil }, Request: func(ctx context.Context, _ json.RawMessage) (string, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return "late", ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		m.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp/"+token, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group_request","arguments":{}}}`)))
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = m.RevokeAndWait(ctx, token); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("returned before active request settled")
	}
	<-finished
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mcp/"+token, strings.NewReader(`{}`)))
	if w.Code != 403 {
		t.Fatal("revoked lease admitted")
	}
}
func TestRevokeIsNotBlockedByValidationLock(t *testing.T) {
	m := &MCP{}
	entered := make(chan struct{})
	done := make(chan struct{})
	token, revoke, err := m.Grant(context.Background(), Lease{Scope: "namespace-drain", Expires: time.Now().Add(time.Minute), Validate: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	go func() {
		defer close(done)
		m.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp/"+token, strings.NewReader(`{}`)))
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = m.RevokeAndWait(ctx, token); err != nil {
		t.Fatal("validation held revocation lock", err)
	}
	<-done
}
