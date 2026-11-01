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
}

const sessionsUsage = "Use /sessions [status|adopt]."

// RegisterSessionsCommand registers the owner-only /sessions status|adopt.
// Adoption only records a request; it is applied by the next startup.
func RegisterSessionsCommand(r *Router, d SessionsDeps) {
	r.Register("sessions", "sessions", "(owner) durable session mapping status; adopt legacy sessions", PermOwnerOnly, func(ctx context.Context, cmd Command) (Response, error) {
		action := "status"
		if len(cmd.Args) > 1 {
			return Response{Code: 1, Text: sessionsUsage}, nil
		}
		if len(cmd.Args) == 1 {
			action = cmd.Args[0]
		}
		switch action {
		case "status", "adopt":
		default:
			return Response{Code: 1, Text: sessionsUsage}, nil
		}
		mode, requested, err := d.Mode(ctx)
		if err != nil {
			return Response{}, fmt.Errorf("session mapping mode: %w", err)
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
		resp := Response{Text: fmt.Sprintf("Session mapping: %s. Adoption requested: %s. Durable sessions: %d, native contexts: %d.", mode, yn, sessions, contexts)}
		if len(cmd.Args) == 0 && mode == "legacy" && !requested {
			resp.Buttons = [][]Button{{{Label: "Adopt", Command: "sessions", Args: []string{"adopt"}}}}
		}
		return resp, nil
	})
}
