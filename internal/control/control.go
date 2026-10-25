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
	ChatKind string // trusted Telegram chat type; empty for unknown/CLI
	ChatID   int64  // Telegram chat ID; 0 for CLI
	// ThreadID is the private-chat topic the command was sent in;
	// 0 for the main chat and for CLI. It routes the command to a
	// conversation inside an already-authorized chat and never grants
	// anything: permission checks use ChatID/IsOwner only.
	ThreadID int
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
	// Confirmed is set when the caller already confirmed a risky
	// command: the Yes button of a confirmation, or the CLI --yes flag.
	// A confirmable handler without it answers with Confirm instead of
	// acting. It skips the question only, never a permission check:
	// the Router checks Perm on every Dispatch, confirmed or not (D028).
	Confirmed bool
}

// Response carries the handler's result. Frontends pick whichever
// field fits their medium (Text for Telegram, Data for --json CLI).
type Response struct {
	Text string         // pre-rendered text
	Data map[string]any // structured payload (CLI --json, future HTTP)
	Code int            // 0 = OK, CodeConfirm = asks to confirm, other non-zero = logical error
	// Buttons are follow-up commands offered with the text, in rows
	// (D028). Telegram shows them as inline buttons whose press
	// dispatches the Button's command through the Router; text
	// frontends print them (FormatText). Optional.
	Buttons [][]Button
}

// CodeConfirm is the Response.Code of a command that asks the caller
// to confirm before it acts (Confirm). Nothing has changed yet.
const CodeConfirm = 2

// Button is one follow-up action of a Response: pressing it dispatches
// Command with Args through the Router as a new Command from the same
// caller, so Perm applies exactly as to a typed command. A Button with
// an empty Command dismisses the prompt (the Cancel of a confirmation).
// Args are ids and names only: a frontend may keep them in memory, but
// never puts secrets into a button (D012, D028).
type Button struct {
	Label   string
	Command string // canonical command name, e.g. "model"; "" = dismiss
	Args    []string
	Style   string // "", StylePrimary, StyleDanger or StyleSuccess
	// Confirmed dispatches the command with Command.Confirmed: the Yes
	// of a confirmation. A frontend makes such a button single-use.
	Confirmed bool
}

// Button styles: a hint, frontends without colours ignore them.
const (
	StylePrimary = "primary"
	StyleDanger  = "danger"
	StyleSuccess = "success"
)

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
	e, resp, err := r.lookup(cmd)
	if err != nil {
		return resp, err
	}
	return e.handler(ctx, cmd)
}

// Check reports whether Dispatch would run cmd's handler: it returns
// the same refusal (Response and ErrUnknownCommand or ErrNotAuthorized,
// a denial logged) without running anything. A frontend uses it to
// refuse quickly before a slow command; Dispatch checks again.
func (r *Router) Check(cmd Command) (Response, error) {
	_, resp, err := r.lookup(cmd)
	return resp, err
}

// lookup finds cmd's handler and checks the caller's permission.
func (r *Router) lookup(cmd Command) (entry, Response, error) {
	r.mu.RLock()
	e, ok := r.handlers[cmd.Name]
	r.mu.RUnlock()

	if !ok {
		return entry{}, Response{Text: "unknown command", Code: 1}, ErrUnknownCommand
	}

	if !r.checkPerm(e.perm, cmd.Caller) {
		r.log.Warn("control: permission denied",
			slog.String("command", cmd.Name),
			slog.String("perm", permString(e.perm)),
			slog.Int64("chat_id", cmd.Caller.ChatID),
			slog.Bool("is_owner", cmd.Caller.IsOwner),
			slog.Bool("confirmed", cmd.Confirmed))
		return entry{}, Response{Text: "Not authorized.", Code: 1}, ErrNotAuthorized
	}
	return e, Response{}, nil
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
