package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthReadinessIsLiveAndFailureLatched(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	failStore := false
	reads := 0
	h := NewHealth(ctx, func(context.Context) error {
		reads++
		if failStore {
			return errors.New("private detail")
		}
		return nil
	})
	request := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		h.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
		if w.Code != want || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("%d %q", w.Code, w.Body.String())
		}
	}
	request(503)
	if reads != 0 {
		t.Fatal("startup touched store")
	}
	h.Ready()
	request(200)
	failStore = true
	request(503)
	failStore = false
	request(200)
	h.Fail()
	h.Ready()
	request(503)
	other := NewHealth(ctx, nil)
	other.Ready()
	cancel()
	w := httptest.NewRecorder()
	other.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 503 {
		t.Fatal("shutdown remained ready")
	}
}

func TestHealthStoreReadHasDeadline(t *testing.T) {
	h := NewHealth(t.Context(), func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Fatal("unbounded read")
		}
		return context.DeadlineExceeded
	})
	h.Ready()
	w := httptest.NewRecorder()
	h.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestHealthProbeOnlyLocalAndNoRedirect(t *testing.T) {
	for _, address := range []string{"0.0.0.0:9878", "example.com:80", "localhost:9878"} {
		if _, err := ServeHealth(address, http.NotFoundHandler()); err == nil {
			t.Fatal(address)
		}
		if err := CheckHealth(t.Context(), address); err == nil {
			t.Fatal(address)
		}
	}
	h := NewHealth(t.Context(), nil)
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	if CheckHealth(t.Context(), addr) == nil {
		t.Fatal("startup healthy")
	}
	h.Ready()
	if err := CheckHealth(t.Context(), addr); err != nil {
		t.Fatal(err)
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, srv.URL+"/healthz", 302) }))
	defer redirect.Close()
	if CheckHealth(t.Context(), strings.TrimPrefix(redirect.URL, "http://")) == nil {
		t.Fatal("followed redirect")
	}
}
