// gopod-group-relay is the trusted inference/MCP sidecar. It contains no native
// provider credentials and accepts no general-purpose upstream destination.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Coriol-is/gopod/internal/isolationproxy"
)

type relayConfig struct {
	Proxy  isolationproxy.Config
	MCPURL string
}

func main() {
	if err := run(); err != nil {
		_, _ = os.Stderr.WriteString("group relay unavailable\n")
		os.Exit(1)
	}
}
func run() error {
	f, err := os.Open("/etc/gopod-group/relay.json")
	if err != nil {
		return errors.New("config unavailable")
	}
	defer f.Close()
	var cfg relayConfig
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	if dec.Decode(&cfg) != nil {
		return errors.New("config invalid")
	}
	target, err := fixedMCPTarget(cfg)
	if err != nil {
		return err
	}
	proxy, err := isolationproxy.New(cfg.Proxy)
	if err != nil {
		return errors.New("policy unavailable")
	}
	defer proxy.Close()
	mcp := httputil.NewSingleHostReverseProxy(target)
	mcp.Transport = &http.Transport{Proxy: nil, ResponseHeaderTimeout: 25 * time.Second, DisableKeepAlives: true}
	mcp.ErrorLog = log.New(io.Discard, "", 0)
	mcp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { http.Error(w, "unavailable", 502) }
	mcp.Director = func(r *http.Request) {
		r.URL.Scheme = target.Scheme
		r.URL.Host = target.Host
		r.URL.Path = target.Path
		r.URL.RawPath = ""
		r.URL.RawQuery = ""
		r.Host = target.Host
		r.Header.Del("Authorization")
		r.Header.Del("Proxy-Authorization")
		r.Header.Del("Cookie")
		r.Header.Del("Forwarded")
		r.Header.Del("X-Forwarded-For")
	}
	pingClient := newPingClient()
	handler := gateCONNECT(cfg.Proxy.Token, target, pingClient, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == target.Path && r.URL.RawQuery == "" {
			r.Body = http.MaxBytesReader(w, r.Body, 16384)
			mcp.ServeHTTP(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	srv := &http.Server{Addr: ":8787", Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384, ErrorLog: log.New(io.Discard, "", 0)}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = proxy.Close()
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	err = srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func fixedMCPTarget(cfg relayConfig) (*url.URL, error) {
	target, err := url.Parse(cfg.MCPURL)
	if err != nil || target.Scheme != "http" || target.Host != "host.docker.internal:9877" || target.Path != "/mcp/"+cfg.Proxy.Token || target.RawPath != "" || target.RawQuery != "" || target.ForceQuery || target.Fragment != "" || target.User != nil || target.Opaque != "" {
		return nil, errors.New("MCP route invalid")
	}
	return target, nil
}
func proxyCapability(header, token string) bool {
	scheme, value, ok := strings.Cut(header, " ")
	if !ok || scheme != "Basic" {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return false
	}
	user, password, ok := strings.Cut(string(decoded), ":")
	return ok && user == "group" && subtle.ConstantTimeCompare([]byte(password), []byte(token)) == 1
}

// Authenticate locally before touching controller state. A stopped, expired,
// retired or revision-mismatched lease can never authorize another CONNECT.
func gateCONNECT(token string, target *url.URL, client *http.Client, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect && (!proxyCapability(r.Header.Get("Proxy-Authorization"), token) || !leaseActive(r.Context(), target, client)) {
			http.Error(w, "egress unavailable", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func leaseActive(ctx context.Context, target *url.URL, client *http.Client) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(`{"jsonrpc":"2.0","id":"egress","method":"ping"}`))
	if err != nil {
		return false
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1025))
	if err != nil || len(data) > 1024 {
		return false
	}
	var reply struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	return json.Unmarshal(data, &reply) == nil && reply.JSONRPC == "2.0" && reply.ID == "egress" && len(reply.Result) > 0 && len(reply.Error) == 0
}

func newPingClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 2 * time.Second}, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
