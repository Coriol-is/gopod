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

// APIServer is a tiny localhost-only HTTP server that exposes memory
// operations to the agent inside the container. The agent calls it
// via curl through its Bash tool — no MCP setup needed.
//
// Endpoints:
//   GET  /memory/search?q=<query>&chat=<folder>&k=<limit>
//   POST /memory/add    {chat, kind, content, title}
//   GET  /memory/list?chat=<folder>&limit=<n>
//
// Binds to 127.0.0.1 (or 0.0.0.0 if the container needs to reach
// it via host.docker.internal). No auth — it's localhost-only and
// the container already has the agent's trust level.
type APIServer struct {
	mem    *Memory
	log    *slog.Logger
	server *http.Server
}

// NewAPIServer creates a memory API server. addr is the listen
// address, e.g. "0.0.0.0:9876" (reachable from containers via
// host.docker.internal:9876).
func NewAPIServer(mem *Memory, addr string, log *slog.Logger) *APIServer {
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	s := &APIServer{
		mem: mem,
		log: log,
		server: &http.Server{
			Addr:    addr,
			Handler: mux,
		},
	}
	mux.HandleFunc("/memory/search", s.handleSearch)
	mux.HandleFunc("/memory/add", s.handleAdd)
	mux.HandleFunc("/memory/list", s.handleList)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	return s
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

func (s *APIServer) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	chat := r.URL.Query().Get("chat")
	k, _ := strconv.Atoi(r.URL.Query().Get("k"))
	if k <= 0 {
		k = 5
	}
	if q == "" || chat == "" {
		http.Error(w, `{"error":"q and chat params required"}`, http.StatusBadRequest)
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

func (s *APIServer) handleAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST required"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Chat    string `json:"chat"`
		Kind    string `json:"kind"`
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"bad json: %s"}`, err), http.StatusBadRequest)
		return
	}
	if req.Chat == "" || req.Content == "" {
		http.Error(w, `{"error":"chat and content required"}`, http.StatusBadRequest)
		return
	}
	if req.Kind == "" {
		req.Kind = "fact"
	}

	id, err := s.mem.Add(r.Context(), req.Chat, req.Kind, req.Title, req.Content, "agent")
	if err != nil {
		s.log.Error("memory api: add", slog.Any("err", err))
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"id": id})
}

func (s *APIServer) handleList(w http.ResponseWriter, r *http.Request) {
	chat := r.URL.Query().Get("chat")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 20
	}
	if chat == "" {
		http.Error(w, `{"error":"chat param required"}`, http.StatusBadRequest)
		return
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

// AgentInstructions returns the text that goes into the container
// skill SKILL.md, teaching the agent how to use the memory API.
func AgentInstructions(chatFolder string) string {
	return strings.ReplaceAll(`# Memory skill

You have access to a long-term memory system. Use it to:
- **Search** past facts, decisions, and preferences
- **Store** new important facts the user tells you
- **List** recent memories

## Commands (via curl in Bash tool)

### Search memory
`+"```bash"+`
curl -s "http://host.docker.internal:9876/memory/search?q=YOUR+QUERY&chat=CHAT_FOLDER&k=5"
`+"```"+`

### Remember a fact
`+"```bash"+`
curl -s -X POST "http://host.docker.internal:9876/memory/add" \
  -H "Content-Type: application/json" \
  -d '{"chat":"CHAT_FOLDER","kind":"fact","content":"THE FACT TO REMEMBER"}'
`+"```"+`

### List recent memories
`+"```bash"+`
curl -s "http://host.docker.internal:9876/memory/list?chat=CHAT_FOLDER&limit=10"
`+"```"+`

## When to use
- **Search**: when the user asks you to recall something, or when you need context from past conversations
- **Store**: when the user states a preference, makes a decision, or tells you an important fact
- **Don't store**: greetings, ephemeral questions, process discussion

## Important
- The system prompt already includes your top-5 most relevant memories automatically
- Use explicit search only when you need MORE context than what's already provided
- kind must be one of: fact, decision, preference, hypothesis
`, "CHAT_FOLDER", chatFolder)
}
