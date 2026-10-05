package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// APIServer is a small HTTP server that exposes memory operations to
// the agent inside its container. The agent calls it via curl through
// its shell tool — no MCP setup needed.
//
// Endpoints (all but /health require `Authorization: Bearer <cap>`):
//
//	GET  /memory/search?q=<query>&k=<limit>
//	POST /memory/add    {kind, content, title}
//	GET  /memory/list?limit=<n>
//
// It binds 0.0.0.0 so sibling containers reach it via
// host.docker.internal, which means anything on the Docker bridge (or
// the LAN, if the port is published) can connect. Authorization is
// therefore a per-exec capability token (CP-001): the runner mints one
// per agent run, passes it in the exec environment as CapabilityEnv,
// and the API resolves the chat from the token. Request data never
// names the chat.
type APIServer struct {
	mem    memoryOps
	tokens *Tokens
	log    *slog.Logger
	server *http.Server
}

// memoryOps is the slice of *Memory the API uses; declared here so the
// handlers can be tested against a fake.
type memoryOps interface {
	Search(ctx context.Context, chatFolder, query string, k int) ([]Item, error)
	Add(ctx context.Context, chatFolder, kind, title, content, source string) (int64, error)
	List(ctx context.Context, chatFolder string, limit int) ([]Item, error)
}

// NewAPIServer creates a memory API server. addr is the listen
// address, e.g. "0.0.0.0:9876". tokens must be the same store the
// runner mints from.
func NewAPIServer(mem *Memory, addr string, tokens *Tokens, log *slog.Logger) *APIServer {
	return newAPIServer(mem, addr, tokens, log)
}

func newAPIServer(mem memoryOps, addr string, tokens *Tokens, log *slog.Logger) *APIServer {
	if log == nil {
		log = slog.Default()
	}
	if tokens == nil {
		tokens = NewTokens() // empty store: every request is 401
	}
	mux := http.NewServeMux()
	s := &APIServer{
		mem:    mem,
		tokens: tokens,
		log:    log,
		server: &http.Server{
			Addr:    addr,
			Handler: mux,
		},
	}
	mux.HandleFunc("/memory/search", s.withChat(s.handleSearch))
	mux.HandleFunc("/memory/add", s.withChat(s.handleAdd))
	mux.HandleFunc("/memory/list", s.withChat(s.handleList))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	return s
}

// chatHandler is an HTTP handler that already knows which chat the
// capability belongs to.
type chatHandler func(w http.ResponseWriter, r *http.Request, chat string)

// withChat resolves the bearer capability to a chat folder and rejects
// everything else with 401. The chat is the only authorization input.
func (s *APIServer) withChat(h chatHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		tok, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || tok == "" {
			http.Error(w, `{"error":"missing capability: send Authorization: Bearer $`+CapabilityEnv+`"}`, http.StatusUnauthorized)
			return
		}
		chat, ok := s.tokens.Lookup(strings.TrimSpace(tok))
		if !ok {
			http.Error(w, `{"error":"invalid or expired capability"}`, http.StatusUnauthorized)
			return
		}
		h(w, r, chat)
	}
}

// Start begins listening. Blocks until the server is shut down.
func (s *APIServer) Start() error {
	ln, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return fmt.Errorf("memory api: listen: %w", err)
	}
	s.log.Info("memory API listening", slog.String("addr", s.server.Addr))
	return s.server.Serve(ln)
}

// Shutdown gracefully stops the server.
func (s *APIServer) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *APIServer) handleSearch(w http.ResponseWriter, r *http.Request, chat string) {
	q := r.URL.Query().Get("q")
	k, _ := strconv.Atoi(r.URL.Query().Get("k"))
	if k <= 0 {
		k = 5
	}
	if q == "" {
		http.Error(w, `{"error":"q param required"}`, http.StatusBadRequest)
		return
	}

	items, err := s.mem.Search(r.Context(), chat, q, k)
	if err != nil {
		s.log.Error("memory api: search", slog.Any("err", err))
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}

	type result struct {
		ID      int64   `json:"id"`
		Kind    string  `json:"kind"`
		Title   string  `json:"title,omitempty"`
		Content string  `json:"content"`
		Score   float64 `json:"score"`
	}
	results := make([]result, len(items))
	for i, it := range items {
		results[i] = result{ID: it.ID, Kind: it.Kind, Title: it.Title, Content: it.Content, Score: it.Score}
	}
	writeJSON(w, results)
}

func (s *APIServer) handleAdd(w http.ResponseWriter, r *http.Request, chat string) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST required"}`, http.StatusMethodNotAllowed)
		return
	}
	// A "chat" field in the body is accepted for backwards compatibility
	// with older skill text and ignored: the capability decides.
	var req struct {
		Kind    string `json:"kind"`
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"bad json: %s"}`, err), http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		http.Error(w, `{"error":"content required"}`, http.StatusBadRequest)
		return
	}
	if req.Kind == "" {
		req.Kind = "fact"
	}

	id, err := s.mem.Add(r.Context(), chat, req.Kind, req.Title, req.Content, "agent")
	if err != nil {
		s.log.Error("memory api: add", slog.Any("err", err))
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"id": id})
}

func (s *APIServer) handleList(w http.ResponseWriter, r *http.Request, chat string) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}

	items, err := s.mem.List(r.Context(), chat, limit)
	if err != nil {
		s.log.Error("memory api: list", slog.Any("err", err))
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}

	type result struct {
		ID      int64  `json:"id"`
		Kind    string `json:"kind"`
		Title   string `json:"title,omitempty"`
		Content string `json:"content"`
	}
	results := make([]result, len(items))
	for i, it := range items {
		results[i] = result{ID: it.ID, Kind: it.Kind, Title: it.Title, Content: it.Content}
	}
	writeJSON(w, results)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// MemoryAPIAddr returns the default address for the memory API.
// 0.0.0.0 so containers can reach it via host.docker.internal.
func MemoryAPIAddr() string {
	return "0.0.0.0:9876"
}

// AgentInstructions is the text that teaches an agent how to call the
// memory API. container/skills/memory/SKILL.md carries the same text;
// keep them in sync. The token arrives in the exec environment as
// CapabilityEnv, which both Claude Code's and Codex's shell tools expose.
func AgentInstructions() string {
	return `# Memory skill

You have access to a long-term memory system. Use it to:
- **Search** past facts, decisions, and preferences
- **Store** new important facts the user tells you
- **List** recent memories

Every call needs the capability from the environment:
` + "`-H \"Authorization: Bearer $" + CapabilityEnv + "\"`" + `. It is valid for this
turn only and already identifies your chat; never pass a chat name.

## Commands (via curl in your shell tool)

### Search memory
` + "```bash" + `
curl -s -H "Authorization: Bearer $` + CapabilityEnv + `" \
  "http://host.docker.internal:9876/memory/search?q=YOUR+QUERY&k=5"
` + "```" + `

### Remember a fact
` + "```bash" + `
curl -s -X POST -H "Authorization: Bearer $` + CapabilityEnv + `" \
  -H "Content-Type: application/json" \
  -d '{"kind":"fact","content":"THE FACT TO REMEMBER"}' \
  "http://host.docker.internal:9876/memory/add"
` + "```" + `

### List recent memories
` + "```bash" + `
curl -s -H "Authorization: Bearer $` + CapabilityEnv + `" \
  "http://host.docker.internal:9876/memory/list?limit=10"
` + "```" + `

## When to use
- **Search**: when the user asks you to recall something, or when you need context from past conversations
- **Store**: when the user states a preference, makes a decision, or tells you an important fact
- **Don't store**: greetings, ephemeral questions, process discussion

## Important
- The system prompt already includes your top-5 most relevant memories automatically
- Use explicit search only when you need MORE context than what's already provided
- kind must be one of: fact, decision, preference, hypothesis
`
}
