package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestMCPObserverPreservesBodyResponseAndHidesUntrustedMetadata(t *testing.T) {
	for _, method := range []string{"tools/list", "secret_method\nforged_event"} {
		t.Run(method, func(t *testing.T) {
			body := `{"method":"` + strings.ReplaceAll(method, "\n", `\n`) + `","params":{"secret":"PRIVATE_BODY_TOKEN"}}`
			request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp/PRIVATE_CAPABILITY", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer PRIVATE_AUTH_TOKEN")
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, err := io.ReadAll(r.Body)
				if err != nil || string(got) != body {
					t.Error("observer altered request body")
				}
				if r.URL.Path != "/mcp/PRIVATE_CAPABILITY" || r.Header.Get("Authorization") != "Bearer PRIVATE_AUTH_TOKEN" {
					t.Error("observer altered request metadata")
				}
				w.Header().Set("X-Fixture", "preserved")
				w.WriteHeader(202)
				_, _ = io.WriteString(w, "PRIVATE_RESPONSE_UNCHANGED")
			})
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			original := os.Stderr
			os.Stderr = write
			defer func() { os.Stderr = original; write.Close(); read.Close() }()
			response := httptest.NewRecorder()
			observeMCP(next).ServeHTTP(response, request)
			os.Stderr = original
			write.Close()
			logged, err := io.ReadAll(read)
			if err != nil {
				t.Fatal(err)
			}
			want := "mcp_method=other http_status=202\n"
			if method == "tools/list" {
				want = "mcp_method=tools/list http_status=202\n"
			}
			if string(logged) != want {
				t.Fatalf("unbounded diagnostic output: %q", logged)
			}
			if response.Code != 202 || response.Body.String() != "PRIVATE_RESPONSE_UNCHANGED" || response.Header().Get("X-Fixture") != "preserved" {
				t.Fatal("observer changed response")
			}
		})
	}
}
