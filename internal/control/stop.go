package control

import "context"

// RegisterStopCommand stops an interactive answer in the caller's private
// conversation. It never enqueues work or clears pending messages or history.
//
// The identity test matches `/restart`: a real live private message, which a
// positive MessageID is the evidence for. The inline Stop control is its own
// callback ("st"), not a Router command button, so it does not pass through
// here and is unaffected.
func RegisterStopCommand(r *Router, stop func(chatID int64, threadID int) bool) {
	r.Register("stop", "stop", "(owner, private) stop the current answer in this topic", PermOwnerOnly, func(_ context.Context, cmd Command) (Response, error) {
		if cmd.Caller.Source != SourceTelegram || cmd.Caller.ChatKind != "private" || cmd.Caller.MessageID <= 0 || len(cmd.Args) != 0 {
			return Response{Code: 1, Text: "Use /stop without arguments in the owner's private chat."}, nil
		}
		if stop == nil {
			return Response{Code: 1, Text: "Stop is unavailable."}, nil
		}
		if !stop(cmd.Caller.ChatID, cmd.Caller.ThreadID) {
			return Response{Text: "No active answer in this topic (or stop already requested)."}, nil
		}
		return Response{Text: "Stopping the current answer. History is preserved; queued messages remain queued."}, nil
	})
}
