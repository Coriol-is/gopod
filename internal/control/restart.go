package control

import (
	"context"
	"strconv"
)

// RegisterRestartCommand uses the normal Router permission gate. Only a real
// private Telegram message can carry the durable replay identity.
func RegisterRestartCommand(r *Router, restart func(context.Context, Caller) error) {
	r.Register("restart", "restart", "(owner) restart gateway and agent containers", PermOwnerOnly, func(ctx context.Context, cmd Command) (Response, error) {
		if cmd.Caller.Source != SourceTelegram || cmd.Caller.ChatKind != "private" || cmd.Caller.MessageID <= 0 || len(cmd.Args) != 0 {
			return Response{Code: 1, Text: "Use /restart in the owner's private chat."}, nil
		}
		if restart == nil {
			return Response{Code: 1, Text: "Restart is unavailable: a supervised Docker controller is required."}, nil
		}
		if err := restart(ctx, cmd.Caller); err != nil {
			return Response{Code: 1, Text: err.Error()}, nil
		}
		return Response{}, nil // the restart service already sent the acknowledgement
	})
}

// RegisterRestartLifecycle exposes graceful drain plus explicitly confirmed force.
func RegisterRestartLifecycle(r *Router, restart func(context.Context, Caller, string) (string, error)) {
	r.Register("restart", "restart", "(owner) restart after drain; force, status or cancel", PermOwnerOnly, func(ctx context.Context, cmd Command) (Response, error) {
		if cmd.Caller.Source != SourceTelegram || cmd.Caller.ChatKind != "private" || cmd.Caller.MessageID <= 0 {
			return Response{Code: 1, Text: "Use /restart in the owner's private chat."}, nil
		}
		action := ""
		if len(cmd.Args) > 2 {
			return Response{Code: 1, Text: "Use /restart [force|status|cancel]."}, nil
		}
		if len(cmd.Args) > 0 {
			action = cmd.Args[0]
		}
		if len(cmd.Args) == 2 {
			if action != "cancel" && action != "status" {
				return Response{Code: 1, Text: "Use /restart [force|status|cancel]."}, nil
			}
			if id, e := strconv.Atoi(cmd.Args[1]); e != nil || id <= 0 {
				return Response{Code: 1, Text: "Invalid restart operation."}, nil
			}
		}
		switch action {
		case "", "status", "cancel":
		case "force":
			if !cmd.Confirmed {
				return Confirm(cmd, "Force restart? Active replies and all pending/running background work will be cancelled. Schedules and files are kept."), nil
			}
		default:
			return Response{Code: 1, Text: "Use /restart [force|status|cancel]."}, nil
		}
		if restart == nil {
			return Response{Code: 1, Text: "Restart is unavailable."}, nil
		}
		dispatch := action
		if len(cmd.Args) == 2 {
			dispatch += ":" + cmd.Args[1]
		}
		text, err := restart(ctx, cmd.Caller, dispatch)
		if err != nil {
			return Response{}, err
		}
		if action == "" {
			id := strconv.Itoa(cmd.Caller.MessageID)
			return Response{Text: "Restart is waiting for active work. Queued input is retained.", Buttons: [][]Button{{
				{Label: "Status", Command: "restart", Args: []string{"status", id}},
				{Label: "Cancel restart", Command: "restart", Args: []string{"cancel", id}},
			}}}, nil
		}
		return Response{Text: text}, nil
	})
}
