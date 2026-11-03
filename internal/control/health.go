package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

const DefaultHealthAddress = "127.0.0.1:9878"

// Health describes local controller readiness, never provider/auth availability.
// Failure is latched for this process, so a loop exiting during startup cannot
// be hidden by the eventual startup-ready transition.
type Health struct {
	ready  atomic.Bool
	failed atomic.Bool
	parent context.Context
	check  func(context.Context) error
}

func NewHealth(parent context.Context, check func(context.Context) error) *Health {
	return &Health{parent: parent, check: check}
}
func (h *Health) Ready() { h.ready.Store(true) }
func (h *Health) Fail()  { h.failed.Store(true) }

func (h *Health) Handler() http.Handler {
	r := New(nil)
	r.Register("healthcheck", "", "local readiness", PermPublic, func(ctx context.Context, _ Command) (Response, error) {
		if !h.ready.Load() || h.failed.Load() || h.parent.Err() != nil {
			return Response{Code: 1}, nil
		}
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if h.check != nil && h.check(ctx) != nil {
			return Response{Code: 1}, nil
		}
		return Response{}, nil
	})
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.URL.Path != "/healthz" {
			http.NotFound(w, req)
			return
		}
		res, err := r.Dispatch(req.Context(), Command{Name: "healthcheck"})
		w.Header().Set("Cache-Control", "no-store")
		if err != nil || res.Code != 0 {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ready\n")
	})
}

// ServeHealth only binds a literal loopback IP. It exposes no user data and
// provides no actions, credential probes, network dependency checks or writes.
func ServeHealth(address string, handler http.Handler) (func(), error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, errors.New("health address must be a loopback IP")
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen health: %w", err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: 2 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return func() { _ = srv.Close() }, nil
}

// CheckHealth is the offline CLI/Docker probe. It never loads config or opens
// the database, and bypasses proxies to reach only the local controller/relay.
func CheckHealth(ctx context.Context, address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("health address must be a loopback IP")
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/healthz", nil)
	if err != nil {
		return errors.New("invalid health request")
	}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("health unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("not ready")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 16))
	if err != nil || string(body) != "ready\n" {
		return errors.New("invalid health response")
	}
	return nil
}
