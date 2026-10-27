package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) { w.code = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = 200
	}
	return w.ResponseWriter.Write(p)
}

// Only fixed RPC method classes and numeric HTTP statuses are diagnostic output.
func observeMCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, e := io.ReadAll(io.LimitReader(r.Body, 16385))
		if e != nil {
			http.Error(w, "unavailable", http.StatusBadRequest)
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(b))
		var req struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(b, &req)
		method := "other"
		switch req.Method {
		case "initialize", "ping", "tools/list", "tools/call", "notifications/initialized":
			method = req.Method
		}
		status := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(status, r)
		fmt.Fprintf(os.Stderr, "mcp_method=%s http_status=%d\n", method, status.code)
	})
}
