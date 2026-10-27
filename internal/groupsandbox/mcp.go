package groupsandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Lease is minted by the host for one current namespace/epoch and turn. Model
// arguments cannot choose its identity, expiry, executor or request permissions.
type Lease struct {
	ToolImageID string
	Scope       string
	Expires     time.Time
	Validate    func(context.Context) error
	Request     func(context.Context, json.RawMessage) (string, error)
	cancel      context.CancelFunc
	ctx         context.Context
	busy        chan struct{}
}

type MCP struct {
	Executor *Executor
	mu       sync.RWMutex
	leases   map[string]*Lease
	draining map[string]map[string]*Lease // revoked leases retained until actual handler settlement
}

func (m *MCP) Grant(ctx context.Context, l Lease) (string, func(), error) {
	if !validScope(l.Scope) || l.Validate == nil || l.Expires.Before(time.Now()) || l.Expires.After(time.Now().Add(time.Hour)) {
		return "", nil, ErrUnavailable
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := hex.EncodeToString(raw)
	l.ctx, l.cancel = context.WithDeadline(ctx, l.Expires)
	l.busy = make(chan struct{}, 1)
	m.mu.Lock()
	if len(m.draining[l.Scope]) > 0 {
		m.mu.Unlock()
		l.cancel()
		return "", nil, ErrUnavailable
	}
	if m.leases == nil {
		m.leases = make(map[string]*Lease)
	}
	if m.leases[token] != nil {
		m.mu.Unlock()
		l.cancel()
		return "", nil, ErrUnavailable
	}
	for _, pending := range m.draining {
		if pending[token] != nil {
			m.mu.Unlock()
			l.cancel()
			return "", nil, ErrUnavailable
		}
	}
	m.leases[token] = &l
	m.mu.Unlock()
	revoke := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = m.RevokeAndWait(ctx, token)
	}
	return token, revoke, nil
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (m *MCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/mcp/") || r.URL.RawQuery != "" {
		http.Error(w, "unavailable", 404)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/mcp/")
	m.mu.RLock()
	l := m.leases[token]
	if l == nil || l.ctx.Err() != nil {
		m.mu.RUnlock()
		http.Error(w, "unavailable", 403)
		return
	}
	// Do not hold the registry lock while waiting: revocation must be able
	// to remove and cancel this lease even when another RPC is still active.
	m.mu.RUnlock()
	select {
	case l.busy <- struct{}{}:
		defer func() { <-l.busy }()
	case <-r.Context().Done():
		http.Error(w, "unavailable", 403)
		return
	case <-l.ctx.Done():
		http.Error(w, "unavailable", 403)
		return
	}
	requestCtx, requestCancel := context.WithCancel(r.Context())
	stopRequest := context.AfterFunc(l.ctx, requestCancel)
	defer stopRequest()
	defer requestCancel()
	r = r.WithContext(requestCtx)
	if l.ctx.Err() != nil || l.Validate(requestCtx) != nil || requestCtx.Err() != nil {
		http.Error(w, "unavailable", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	defer r.Body.Close()
	var req rpcRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil || req.JSONRPC != "2.0" {
		http.Error(w, "invalid request", 400)
		return
	}
	var extra any
	if !errors.Is(dec.Decode(&extra), io.EOF) {
		http.Error(w, "invalid request", 400)
		return
	}
	if req.Method == "notifications/initialized" {
		w.WriteHeader(202)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	reply := func(result any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "result": result})
	}
	fail := func(code int) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(req.ID), "error": map[string]any{"code": code, "message": "request unavailable"}})
	}
	if len(req.ID) == 0 {
		w.WriteHeader(202)
		return
	}
	switch req.Method {
	case "initialize":
		reply(map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "gopod-group", "version": "1"}})
	case "ping":
		reply(map[string]any{})
	case "tools/list":
		reply(map[string]any{"tools": []any{
			map[string]any{"name": "group_exec", "description": "Run a bounded command in this group's isolated workspace. No network, credentials or personal data are available.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]string{"type": "string"}}, "required": []string{"cmd"}, "additionalProperties": false}},
			map[string]any{"name": "group_request", "description": "Submit a host-validated memory or IPC request for this group only. Follow the trusted group guide; payload cannot grant permissions.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"memory_search", "memory_add", "task", "message", "receipt"}}, "payload": map[string]string{"type": "object"}}, "required": []string{"kind", "payload"}, "additionalProperties": false}},
		}})
	case "tools/call":
		if r.Context().Err() != nil || l.ctx.Err() != nil {
			fail(-32601)
			return
		}
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &call) != nil {
			fail(-32602)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(l.ctx, cancel)
		defer stop()
		defer cancel()
		var output string
		var err error
		switch call.Name {
		case "group_exec":
			var args struct {
				Cmd string `json:"cmd"`
			}
			d := json.NewDecoder(strings.NewReader(string(call.Arguments)))
			d.DisallowUnknownFields()
			if d.Decode(&args) != nil || m.Executor == nil {
				fail(-32602)
				return
			}
			output, err = m.Executor.RunPinned(ctx, l.Scope, args.Cmd, l.ToolImageID, l.Validate)
		case "group_request":
			if l.Request == nil {
				fail(-32601)
				return
			}
			output, err = l.Request(ctx, call.Arguments)
		default:
			fail(-32601)
			return
		}
		if ctx.Err() != nil || l.Validate(ctx) != nil {
			output = ""
			err = ErrUnavailable
		}
		if len(output) > MaxOutput {
			output = output[:MaxOutput]
		}
		if err != nil {
			output = "Group operation unavailable. Check the scoped receipt before retrying."
		}
		reply(map[string]any{"content": []any{map[string]string{"type": "text", "text": output}}, "isError": err != nil})
	default:
		fail(-32601)
	}
}

// RevokeAndWait closes admission atomically, then drains active tool/export
// work. A timeout retains the lease by scope: expiration is never settlement,
// and a later explicit repair can still prove the original host handler exited.
func (m *MCP) RevokeAndWait(ctx context.Context, token string) error {
	m.mu.Lock()
	l := m.leases[token]
	if l != nil {
		delete(m.leases, token)
		if m.draining == nil {
			m.draining = map[string]map[string]*Lease{}
		}
		if m.draining[l.Scope] == nil {
			m.draining[l.Scope] = map[string]*Lease{}
		}
		m.draining[l.Scope][token] = l
		l.cancel()
	} else {
		for _, pending := range m.draining {
			if candidate := pending[token]; candidate != nil {
				l = candidate
				break
			}
		}
	}
	m.mu.Unlock()
	if l == nil {
		return nil
	}
	return m.waitDrainingLease(ctx, token, l)
}

// DrainScope is called by an explicitly serialized namespace repair before any
// policy GC or admission-marker reset. It never holds the global map lock while
// waiting for a slow validation, host request, tool cleanup or snapshot export.
func (m *MCP) DrainScope(ctx context.Context, scope string) error {
	if !validScope(scope) {
		return ErrUnavailable
	}
	for {
		m.mu.RLock()
		pending := make(map[string]*Lease, len(m.draining[scope]))
		for token, l := range m.draining[scope] {
			pending[token] = l
		}
		m.mu.RUnlock()
		if len(pending) == 0 {
			return nil
		}
		for token, l := range pending {
			if err := m.waitDrainingLease(ctx, token, l); err != nil {
				return err
			}
		}
	}
}
func (m *MCP) waitDrainingLease(ctx context.Context, token string, l *Lease) error {
	if ctx.Err() != nil {
		return ErrUnavailable
	}
	select {
	case l.busy <- struct{}{}:
		m.mu.Lock()
		if m.draining[l.Scope][token] == l {
			delete(m.draining[l.Scope], token)
			if len(m.draining[l.Scope]) == 0 {
				delete(m.draining, l.Scope)
			}
		}
		m.mu.Unlock()
		<-l.busy
		return nil
	case <-ctx.Done():
		return ErrUnavailable
	}
}
