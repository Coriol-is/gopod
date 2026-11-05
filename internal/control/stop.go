package control

import "context"

// RegisterStopCommand stops an interactive answer in the caller's private
// conversation. It never enqueues work or clears pending messages or history.
//
// The identity test matches `/restart`: a real live private message, which a
// positive MessageID is the evidence for. The inline Stop control is its own
// callback ("st"), not a Router command button, so it does not pass through
// here and is unaffected.
type StopOutcome int

const (
	StopFinished StopOutcome = iota
	StopAccepted
	StopAlreadyRequested
	StopFailed
)

func RegisterStopCommand(r *Router, stop func(context.Context, int64, int) StopOutcome) {
	r.Register("stop", "stop", "(owner, private) stop the current answer in this topic", PermOwnerOnly, func(ctx context.Context, cmd Command) (Response, error) {
		if cmd.Caller.Source != SourceTelegram || cmd.Caller.ChatKind != "private" || cmd.Caller.MessageID <= 0 || len(cmd.Args) != 0 {
			return Response{Code: 1, Text: "Use /stop without arguments in the owner's private chat."}, nil
		}
		if stop == nil {
			return Response{Code: 1, Text: "Stop is unavailable."}, nil
		}
		switch stop(ctx, cmd.Caller.ChatID, cmd.Caller.ThreadID) {
		case StopFinished:
			return Response{Text: "No active answer in this topic."}, nil
		case StopAlreadyRequested:
			return Response{Text: "Stop already requested; completion is still being reconciled."}, nil
		case StopAccepted:
			return Response{Text: "Stop requested. Native completion is still being reconciled; queued messages remain queued."}, nil
		case StopFailed:
			return Response{Code: 1, Text: "Stop could not be confirmed. The answer is held for reconciliation."}, nil
		}
		return Response{Code: 1, Text: "Stop outcome is unavailable."}, nil
	})
}
