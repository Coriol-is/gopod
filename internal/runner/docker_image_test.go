package runner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

func TestEnsureRunningVerifiesSelectedImage(t *testing.T) {
	for _, tc := range []struct {
		name, current, desired string
		imageFailure           bool
		wantReuse, wantErr     bool
		reconcile              bool
	}{
		{"same-image", "sha256:same", "sha256:same", false, true, false, false},
		{"different-image", "sha256:old", "sha256:new", false, false, false, true},
		{"active-mismatch-preserved", "sha256:old", "sha256:new", false, false, true, false},
		{"unavailable", "sha256:old", "", true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var operations []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := strings.TrimPrefix(r.URL.Path, "/v1.47")
				operations = append(operations, r.Method+" "+path)
				w.Header().Set("Content-Type", "application/json")
				switch {
				case path == "/containers/json":
					fmt.Fprint(w, `[{"Id":"existing-container-id","Names":["/gopod-owner"]}]`)
				case path == "/containers/existing-container-id/json":
					fmt.Fprintf(w, `{"Id":"existing-container-id","Image":%q,"State":{"Running":true}}`, tc.current)
				case strings.HasPrefix(path, "/images/"):
					if tc.imageFailure {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"message":"image unavailable"}`)
					} else {
						fmt.Fprintf(w, `{"Id":%q}`, tc.desired)
					}
				case path == "/containers/existing-container-id/stop":
					w.WriteHeader(204)
				case path == "/containers/existing-container-id" && r.Method == "DELETE":
					w.WriteHeader(204)
				case path == "/containers/create":
					w.WriteHeader(201)
					fmt.Fprint(w, `{"Id":"new-container-id"}`)
				case path == "/containers/new-container-id/start":
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected request %v", r.URL)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithVersion("1.47"), client.WithHTTPClient(srv.Client()))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			d := &Docker{cli: cli, log: quietLog()}
			id, err := d.ensureRunning(context.Background(), "gopod-owner", &container.Config{Image: "selected:latest"}, &container.HostConfig{}, tc.reconcile)
			if (err != nil) != tc.wantErr {
				t.Fatal(id, err)
			}
			if tc.name == "active-mismatch-preserved" {
				var mismatch *ImageMismatchError
				if !errors.As(err, &mismatch) {
					t.Fatalf("untyped mismatch: %v", err)
				}
			}
			if tc.wantReuse && id != "existing-container-id" {
				t.Fatal(id)
			}
			mutations := 0
			for _, op := range operations {
				if strings.HasPrefix(op, "POST") || strings.HasPrefix(op, "DELETE") {
					mutations++
				}
			}
			if tc.wantReuse || tc.wantErr {
				if mutations != 0 {
					t.Fatal("existing container mutated", operations)
				}
			} else if mutations != 4 || id != "new-container-id" {
				t.Fatal(id, operations)
			}
		})
	}
}
