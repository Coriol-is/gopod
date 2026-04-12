package control

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// RegisterBuiltins registers the built-in commands that don't depend
// on external subsystems (store, runner, etc). These are pure-logic
// commands: help, ping, version, whoami.
//
// Commands that depend on external state (login, register, chats.list)
// are registered by the subsystem that owns them — typically in
// main.go or in the telegram package, using Router.Register directly.
func RegisterBuiltins(r *Router) {
	r.Register("help", "help", "list available commands", PermPublic, helpHandler(r))
	r.Register("ping", "ping", "check the bot is alive", PermPublic, pingHandler)
	r.Register("version", "version", "show build info", PermPublic, versionHandler)
}

func helpHandler(r *Router) Handler {
	return func(ctx context.Context, cmd Command) (Response, error) {
		cmds := r.List()
		var sb strings.Builder
		sb.WriteString("**picoclaw commands**\n\n")
		for _, c := range cmds {
			if c.Perm == PermOwnerOnly && !cmd.Caller.IsOwner {
				continue
			}
			fmt.Fprintf(&sb, "/%s — %s\n", c.SlashName, c.Description)
		}
		sb.WriteString("\nSend any non-/ text to talk to Claude.")
		return Response{Text: sb.String()}, nil
	}
}

func pingHandler(_ context.Context, _ Command) (Response, error) {
	return Response{Text: "pong"}, nil
}

// RegisterWhoami registers the /whoami command. Needs the store and
// owner chat ID — passed as a closure.
func RegisterWhoami(r *Router, ownerChatID int64) {
	r.Register("whoami", "whoami", "show this chat's id and registration state", PermPublic,
		func(ctx context.Context, cmd Command) (Response, error) {
			var sb strings.Builder
			fmt.Fprintf(&sb, "chat_id: %d\n", cmd.Caller.ChatID)
			fmt.Fprintf(&sb, "from_user_id: %d\n", cmd.Caller.UserID)
			fmt.Fprintf(&sb, "source: %s\n", cmd.Caller.Source)
			if cmd.Caller.IsOwner {
				sb.WriteString("owner: yes\n")
			} else {
				sb.WriteString("owner: no\n")
			}
			return Response{Text: sb.String()}, nil
		})
}

// RegisterVoiceCommand adds /voice to the Router with a callback for
// setting the per-chat reply mode.
func RegisterVoiceCommand(r *Router, setMode func(chatID int64, mode string)) {
	r.Register("voice", "voice", "set reply mode: voice, text, voice+text, auto", PermChatLocal,
		func(ctx context.Context, cmd Command) (Response, error) {
			mode := "auto"
			if len(cmd.Args) > 0 {
				mode = cmd.Args[0]
			}
			switch mode {
			case "voice", "text", "voice+text", "auto":
				setMode(cmd.Caller.ChatID, mode)
				if mode == "auto" {
					return Response{Text: "Reply mode: auto (voice input → voice+text reply, text input → text reply)"}, nil
				}
				return Response{Text: "Reply mode set to: " + mode}, nil
			default:
				return Response{
					Text: "Usage: /voice <mode>\n\nModes:\n  auto — voice input → voice+text, text → text (default)\n  voice — always reply with voice\n  text — always reply with text only\n  voice+text — always reply with both",
					Code: 1,
				}, nil
			}
		})
}

func versionHandler(_ context.Context, _ Command) (Response, error) {
	ver := "dev"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				if len(s.Value) > 12 {
					ver = s.Value[:12]
				} else {
					ver = s.Value
				}
				break
			}
		}
	}
	text := fmt.Sprintf("picoclaw %s (go %s)", ver, runtime.Version())
	return Response{
		Text: text,
		Data: map[string]any{"version": ver, "go": runtime.Version()},
	}, nil
}
