package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRelayHealthLocalOnlyAndNeverProxies(t *testing.T) {
	called := false
	h := relayHealth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	for _, tc := range []struct {
		addr, method string
		want         int
	}{{"127.0.0.1:1", "GET", 200}, {"[::1]:1", "GET", 200}, {"172.18.0.2:1", "GET", 403}, {"127.0.0.1:1", "POST", 403}} {
		req := httptest.NewRequest(tc.method, "/healthz", nil)
		req.RemoteAddr = tc.addr
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want || called {
			t.Fatalf("%+v: %d proxy=%v", tc, w.Code, called)
		}
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/mcp", nil))
	if !called {
		t.Fatal("non-health route swallowed")
	}
}
