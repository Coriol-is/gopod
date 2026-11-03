package control

import (
	"context"
	"fmt"
)

// SessionsDeps are the store operations behind /sessions.
type SessionsDeps struct {
	Mode    func(ctx context.Context) (mode string, requested bool, err error)
	Request func(ctx context.Context) error
	Count   func(ctx context.Context) (sessions, contexts int, err error)
	// Cancel clears a pending adoption request.
	Cancel func(ctx context.Context) error
	// Unfinished counts unfinished turns; while any exist a pending adoption
	// is deferred at startup.
	Unfinished func(ctx context.Context) (int, error)
}

const sessionsUsage = "Use /sessions [status|adopt|cancel]."

// RegisterSessionsCommand registers the owner-only /sessions status|adopt|cancel.
// Adoption only records a request; it is applied by the next startup. cancel
// withdraws a pending request without confirmation (nothing is changed yet).
func RegisterSessionsCommand(r *Router, d SessionsDeps) {
	r.Register("sessions", "sessions", "(owner) durable session mapping status; adopt legacy sessions or cancel a pending adoption", PermOwnerOnly, func(ctx context.Context, cmd Command) (Response, error) {
		action := "status"
		if len(cmd.Args) > 1 {
			return Response{Code: 1, Text: sessionsUsage}, nil
		}
		if len(cmd.Args) == 1 {
			action = cmd.Args[0]
		}
		switch action {
		case "status", "adopt", "cancel":
		default:
			return Response{Code: 1, Text: sessionsUsage}, nil
		}
		mode, requested, err := d.Mode(ctx)
		if err != nil {
			return Response{}, fmt.Errorf("session mapping mode: %w", err)
		}
		if action == "cancel" {
			if !requested {
				return Response{Code: 1, Text: "No adoption requested."}, nil
			}
			if err := d.Cancel(ctx); err != nil {
				return Response{}, fmt.Errorf("cancel session adoption: %w", err)
			}
			return Response{Text: "Adoption request cancelled."}, nil
		}
		if action == "adopt" {
			if mode != "legacy" {
				return Response{Code: 1, Text: "Already adopted."}, nil
			}
			if !cmd.Confirmed {
				return Confirm(cmd, "Adopt legacy sessions into durable identity on the next restart? The current mappings are copied once; after that the old table is no longer read. Requires a restart to take effect."), nil
			}
			if err := d.Request(ctx); err != nil {
				return Response{}, fmt.Errorf("request session adoption: %w", err)
			}
			return Response{Text: "Adoption requested. Run /restart to apply."}, nil
		}
		sessions, contexts, err := d.Count(ctx)
		if err != nil {
			return Response{}, fmt.Errorf("count session rows: %w", err)
		}
		yn := "no"
		if requested {
			yn = "yes"
		}
		text := fmt.Sprintf("Session mapping: %s. Adoption requested: %s. Durable sessions: %d, native contexts: %d.", mode, yn, sessions, contexts)
		if requested {
			n, err := d.Unfinished(ctx)
			if err != nil {
				return Response{}, fmt.Errorf("count unfinished turns: %w", err)
			}
			if n > 0 {
				text += fmt.Sprintf(" Deferred: %d unfinished turns.", n)
			}
		}
		resp := Response{Text: text}
		if len(cmd.Args) == 0 && mode == "legacy" && !requested {
			resp.Buttons = [][]Button{{{Label: "Adopt", Command: "sessions", Args: []string{"adopt"}}}}
		}
		return resp, nil
	})
}
