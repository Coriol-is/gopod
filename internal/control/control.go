// Package control is gopod's unified command dispatch layer.
//
// Every admin/diagnostic command — whether invoked from Telegram,
// the CLI, or a future web/MCP frontend — goes through the Router.
// The Router enforces permission checks before calling handlers, so
// authorization lives in exactly one place.
//
// Design: [docs/CONTROL.md](../../docs/CONTROL.md)
// Decision: [D011](../../docs/DECISIONS.md)
package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// Source identifies where a Command came from.
type Source string

const (
	SourceTelegram Source = "telegram"
	SourceCLI      Source = "cli"
)

// Perm declares the minimum privilege a handler requires.
type Perm int

const (
	// PermPublic — any chat, any user.
	PermPublic Perm = iota
	// PermChatLocal — any chat, but effects must be scoped to the
	// calling chat. The Router cannot enforce scoping — that is a
	// handler discipline backed by tests.
	PermChatLocal
	// PermOwnerOnly — only the owner chat, CLI, or authorized socket.
	PermOwnerOnly
)

// Caller describes the origin of a Command. Frontends populate this.
type Caller struct {
	ChatID   int64  // Telegram chat ID; 0 for CLI
	UserID   int64  // Telegram user ID; 0 for CLI
	Username string // display name / @username
	Source   Source
	IsOwner  bool // computed by the frontend
}

// Command is a frontend-agnostic request.
type Command struct {
	Name   string   // dot-separated, e.g. "ping", "chats.register"
	Args   []string // positional arguments after the command name
	Caller Caller
}

// Response carries the handler's result. Frontends pick whichever
// field fits their medium (Text for Telegram, Data for --json CLI).
type Response struct {
	Text string         // pre-rendered text
	Data map[string]any // structured payload (CLI --json, future HTTP)
	Code int            // 0 = OK, non-zero = logical error
}

// Handler implements one command. It receives a fully-validated
// Command: the Router has already checked permissions before calling.
type Handler func(ctx context.Context, cmd Command) (Response, error)

// CommandInfo describes one registered command for /help and
// setMyCommands.
type CommandInfo struct {
	Name        string // "ping", "chats.register"
	Perm        Perm
	Description string // one-line help text
	SlashName   string // Telegram slash form, e.g. "ping", "register"
}

// Router is the central dispatch table. One per gopod process.
type Router struct {
	log *slog.Logger

	mu       sync.RWMutex
	handlers map[string]entry
}

type entry struct {
	handler     Handler
	perm        Perm
	description string
	slashName   string
}

// New creates an empty Router.
func New(log *slog.Logger) *Router {
	if log == nil {
		log = slog.Default()
	}
	return &Router{
		log:      log,
		handlers: make(map[string]entry),
	}
}

// Register adds a command handler. name is the dot-separated
// canonical name (e.g. "ping", "chats.register"). slashName is the
// Telegram /command form (e.g. "ping", "register"). description is
// the one-liner for /help.
//
// Panics on duplicate name — registration happens at startup, not
// at runtime, so duplicates are programmer errors.
func (r *Router) Register(name, slashName, description string, perm Perm, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.handlers[name]; dup {
		panic(fmt.Sprintf("control: duplicate command %q", name))
	}
	r.handlers[name] = entry{
		handler:     h,
		perm:        perm,
		description: description,
		slashName:   slashName,
	}
}

// Dispatch looks up the command by Name, checks permissions, and
// calls the handler. Returns ErrUnknownCommand if the name is not
// registered, or ErrNotAuthorized if the caller lacks permission.
func (r *Router) Dispatch(ctx context.Context, cmd Command) (Response, error) {
	r.mu.RLock()
	e, ok := r.handlers[cmd.Name]
	r.mu.RUnlock()

	if !ok {
		return Response{Text: "unknown command", Code: 1}, ErrUnknownCommand
	}

	if !r.checkPerm(e.perm, cmd.Caller) {
		r.log.Warn("control: permission denied",
			slog.String("command", cmd.Name),
			slog.String("perm", permString(e.perm)),
			slog.Int64("chat_id", cmd.Caller.ChatID),
			slog.Bool("is_owner", cmd.Caller.IsOwner))
		return Response{Text: "Not authorized.", Code: 1}, ErrNotAuthorized
	}

	return e.handler(ctx, cmd)
}

// List returns all registered commands sorted by name. Used by
// /help and setMyCommands.
func (r *Router) List() []CommandInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]CommandInfo, 0, len(r.handlers))
	for name, e := range r.handlers {
		out = append(out, CommandInfo{
			Name:        name,
			Perm:        e.perm,
			Description: e.description,
			SlashName:   e.slashName,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

// LookupBySlash finds a command by its Telegram slash name.
// Returns ("", false) if no command matches.
func (r *Router) LookupBySlash(slash string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, e := range r.handlers {
		if e.slashName == slash {
			return name, true
		}
	}
	return "", false
}

func (r *Router) checkPerm(perm Perm, caller Caller) bool {
	switch perm {
	case PermPublic:
		return true
	case PermChatLocal:
		return true // enforcement is handler discipline, not Router's job
	case PermOwnerOnly:
		return caller.IsOwner
	default:
		return false
	}
}

func permString(p Perm) string {
	switch p {
	case PermPublic:
		return "public"
	case PermChatLocal:
		return "chat_local"
	case PermOwnerOnly:
		return "owner_only"
	default:
		return "unknown"
	}
}

// Sentinel errors for Dispatch.
var (
	ErrUnknownCommand = errors.New("control: unknown command")
	ErrNotAuthorized  = errors.New("control: not authorized")
)
