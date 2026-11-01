package control

import (
	"context"
	"strings"
)

// SilentStateKey prefixes the router_state key that holds a chat's silent
// mode: SilentStateKey + chatFolder is "1" when on and "" (or absent) when
// off. The Telegram bot reads it at the start of every streamed turn.
const SilentStateKey = "silent:"

const silentUsage = "Usage: /silent on | /silent off"

// RegisterSilentCommand registers /silent: with no argument it shows the
// chat's silent mode with an on/off picker (D028); with "on" or "off" it
// sets it. Silent mode keeps answer text out of the draft: the chat sees
// only progress until the final message (D055). Per chat, like /model.
func RegisterSilentCommand(r *Router, currentFn func(chatFolder string) bool, setFn func(chatFolder string, on bool) error, chatFolderLookup func(chatID int64) string) {
	r.Register("silent", "silent", "show or toggle silent mode for this chat (progress only, answer at the end)", PermChatLocal,
		func(ctx context.Context, cmd Command) (Response, error) {
			if groupMemberDenied(cmd.Caller) {
				return notAuthorizedInGroup(), ErrNotAuthorized
			}
			folder := chatFolderLookup(cmd.Caller.ChatID)
			if folder == "" {
				return Response{Text: "Chat not registered.", Code: 1}, nil
			}
			if len(cmd.Args) == 0 {
				cur := "off"
				if currentFn(folder) {
					cur = "on"
				}
				return Response{
					Text:    "Silent mode: " + cur + "\n\nOn: the draft shows only progress (steps and elapsed time); the answer arrives as one message at the end.\n" + silentUsage,
					Buttons: pickerButtons("silent", []string{"on", "off"}, cur),
				}, nil
			}
			var on bool
			switch strings.ToLower(cmd.Args[0]) {
			case "on":
				on = true
			case "off":
				on = false
			default:
				return Response{Text: silentUsage, Code: 1}, nil
			}
			if err := setFn(folder, on); err != nil {
				return Response{Text: err.Error(), Code: 1}, nil
			}
			if on {
				return Response{Text: "Silent mode on. Applies from the next message."}, nil
			}
			return Response{Text: "Silent mode off. Applies from the next message."}, nil
		})
}
