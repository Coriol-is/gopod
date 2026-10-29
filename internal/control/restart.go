package control

import "context"

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
