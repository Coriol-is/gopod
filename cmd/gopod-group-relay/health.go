package main

import (
	"io"
	"net"
	"net/http"
)

// The health request exercises the serving loop after config/policy setup.
// It is local only, contains no capability/config information and never invokes
// the proxy, controller lease probe or upstream inference.
func relayHealth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || r.Method != http.MethodGet {
			http.Error(w, "unavailable", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "ready\n")
	})
}
