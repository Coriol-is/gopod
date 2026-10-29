package control

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"slices"
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
		if len(cmd.Args) > 0 {
			if text, ok := telegramHelpTopic(cmd.Args[0]); ok {
				if !telegramPrivate(cmd.Caller) {
					return Response{}, nil
				}
				if len(cmd.Args) != 1 {
					return Response{Text: "Use /help guest, /help inline or /help topics in private chat.", Code: 1}, nil
				}
				return Response{Text: text}, nil
			}
			return Response{Text: "Use /help or /help guest|inline|topics.", Code: 1}, nil
		}
		cmds := r.List()
		var sb strings.Builder
		sb.WriteString("**gopod commands**\n\n")
		for _, c := range cmds {
			if c.SlashName == "" || (c.Name == "telegram" && !telegramPrivate(cmd.Caller)) {
				continue
			}
			if c.Perm == PermOwnerOnly && !cmd.Caller.IsOwner {
				continue
			}
			if c.Perm == PermGroupOwner && !r.checkPerm(PermGroupOwner, cmd.Caller) {
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
			if cmd.Caller.IsGroupOwner {
				sb.WriteString("group_owner: yes (this group only)\n")
			}
			return Response{Text: sb.String()}, nil
		})
}

// RegisterVoiceCommand adds /voice to the Router with a callback for
// setting the per-chat reply mode.
// RegisterLogsCommand adds /logs to the Router. Owner-only.
// CompactFunc is the callback control uses to trigger session compact
// without importing runner. Wired from main.go. thread is the
// conversation inside the chat (0 = main chat).
type CompactFunc func(ctx context.Context, chatFolder string, thread int, clear bool) (string, error)

// RegisterSessionCommands adds /clear and /compact to the Router. Both
// act on the conversation the command was sent in (Caller.ThreadID)
// and leave the chat's other conversations alone.
func RegisterSessionCommands(r *Router, compactFn CompactFunc, chatFolderLookup func(chatID int64) string) {
	r.Register("clear", "clear", "clear conversation context (memory preserved)", PermChatLocal,
		func(ctx context.Context, cmd Command) (Response, error) {
			folder := chatFolderLookup(cmd.Caller.ChatID)
			if folder == "" {
				return Response{Text: "Chat not registered.", Code: 1}, nil
			}
			if !cmd.Confirmed {
				return Confirm(cmd, "Clear this conversation's context? Long-term memory is kept."), nil
			}
			_, err := compactFn(ctx, folder, cmd.Caller.ThreadID, true) // clear = true → no summary
			if err != nil {
				return Response{}, err
			}
			return Response{Text: "Session cleared. Long-term memory preserved — I still remember your preferences and facts."}, nil
		})

	r.Register("compact", "compact", "summarize conversation + clear context", PermChatLocal,
		func(ctx context.Context, cmd Command) (Response, error) {
			folder := chatFolderLookup(cmd.Caller.ChatID)
			if folder == "" {
				return Response{Text: "Chat not registered.", Code: 1}, nil
			}
			result, err := compactFn(ctx, folder, cmd.Caller.ThreadID, false) // clear = false → summarize first
			if err != nil {
				return Response{}, err
			}
			return Response{Text: "Session compacted. " + result}, nil
		})
}

func RegisterLogsCommand(r *Router, queryFn func(level, subsystem string, limit int) (string, error)) {
	r.Register("logs", "logs", "(owner) show recent gopod logs", PermOwnerOnly,
		func(ctx context.Context, cmd Command) (Response, error) {
			level := ""
			subsys := ""
			limit := 20
			for i, a := range cmd.Args {
				switch a {
				case "--level", "-l":
					if i+1 < len(cmd.Args) {
						level = cmd.Args[i+1]
					}
				case "--subsys", "-s":
					if i+1 < len(cmd.Args) {
						subsys = cmd.Args[i+1]
					}
				}
			}
			text, err := queryFn(level, subsys, limit)
			if err != nil {
				return Response{}, err
			}
			if text == "" {
				text = "No log entries found."
			}
			return Response{Text: text}, nil
		})
}

func RegisterVoiceCommand(r *Router, setMode func(chatID int64, mode string)) {
	r.Register("voice", "voice", "set reply mode: voice, text, voice+text, auto", PermChatLocal,
		func(ctx context.Context, cmd Command) (Response, error) {
			mode := "auto"
			if len(cmd.Args) > 0 {
				mode = cmd.Args[0]
			}
			if cmd.Caller.ChatID <= 0 && mode != "text" {
				return Response{Code: 1, Text: "Voice services are unavailable in the group profile. Replies remain text; personal speech services are not inherited."}, nil
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

// ProviderSwitchFunc is the callback for /provider command.
type ProviderSwitchFunc func(ctx context.Context, chatFolder, providerName string) (string, error)

// RegisterProviderCommand adds /provider for per-chat agent switching.
// currentFn returns the chat's provider name; names are the providers a
// picker offers. With no args /provider answers with a picker; a switch
// affects the entire chat, so it asks to confirm first (D028/D030).
func RegisterProviderCommand(r *Router, switchFn ProviderSwitchFunc, currentFn func(chatFolder string) string, names []string, chatFolderLookup func(chatID int64) string) {
	r.Register("provider", "provider", "switch agent provider (claude, codex)", PermChatLocal,
		func(ctx context.Context, cmd Command) (Response, error) {
			if groupMemberDenied(cmd.Caller) {
				return notAuthorizedInGroup(), ErrNotAuthorized
			}
			folder := chatFolderLookup(cmd.Caller.ChatID)
			if folder == "" {
				return Response{Text: "Chat not registered.", Code: 1}, nil
			}
			if len(cmd.Args) == 0 {
				cur := currentFn(folder)
				return Response{
					Text:    "Current provider: " + cur + "\n\nUsage: /provider <name>\n\n/provider claude — Claude Code CLI (default)\n/provider codex — OpenAI Codex CLI\n\nSwitching preserves sessions and transfers recent context separately for each conversation.",
					Buttons: pickerButtons("provider", names, cur),
				}, nil
			}
			// Validate before asking: an unknown name or the current
			// provider must not cost a confirmation (or a provider change).
			if !slices.Contains(names, cmd.Args[0]) {
				return Response{
					Text: fmt.Sprintf("unknown provider %q (available: %s)", cmd.Args[0], strings.Join(names, ", ")),
					Code: 1,
				}, nil
			}
			if cmd.Args[0] == currentFn(folder) {
				return Response{Text: "Already using " + cmd.Args[0] + ". Nothing changed."}, nil
			}
			if !cmd.Confirmed && !cmd.Caller.IsGroupOwner {
				return Confirm(cmd, "Switch this chat to "+cmd.Args[0]+"? Conversations keep their recent context."), nil
			}
			result, err := switchFn(ctx, folder, cmd.Args[0])
			if err != nil {
				return Response{Text: err.Error(), Code: 1}, nil
			}
			return Response{Text: result}, nil
		})
}

// RegisterModelCommand adds /model for per-chat model selection
// (CP-008). currentFn returns the stored override ("" = provider
// default); setFn stores one ("" clears) and returns the reply text.
// choicesFn returns the names a picker offers for the chat's provider
// (nil: none known); with no args /model answers with those buttons
// plus "default", the current one marked.
func RegisterModelCommand(r *Router, currentFn func(chatFolder string) string, setFn func(chatFolder, model string) (string, error), choicesFn func(chatFolder string) []string, chatFolderLookup func(chatID int64) string) {
	r.Register("model", "model", "show or set the model for this chat", PermChatLocal,
		func(ctx context.Context, cmd Command) (Response, error) {
			if groupMemberDenied(cmd.Caller) {
				return notAuthorizedInGroup(), ErrNotAuthorized
			}
			folder := chatFolderLookup(cmd.Caller.ChatID)
			if folder == "" {
				return Response{Text: "Chat not registered.", Code: 1}, nil
			}
			if len(cmd.Args) == 0 {
				cur := currentFn(folder)
				if cur == "" {
					cur = "default"
				}
				var choices []string
				if choicesFn != nil {
					// A copy: the "default" appended below must not
					// write into the caller's slice.
					choices = append(choices, choicesFn(folder)...)
				}
				return Response{
					Text:    "Current model: " + cur + "\n\nUsage: /model <name> | /model default\nClaude aliases: fable, opus, sonnet, haiku (or a full model id)\nCodex: any model id the account can use (e.g. gpt-5)\nApplies from the next message; unknown names fail on the next turn.",
					Buttons: pickerButtons("model", append(choices, "default"), cur),
				}, nil
			}
			model := cmd.Args[0]
			if model == "default" {
				model = ""
			}
			result, err := setFn(folder, model)
			if err != nil {
				return Response{Text: err.Error(), Code: 1}, nil
			}
			return Response{Text: result}, nil
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
	text := fmt.Sprintf("gopod %s (go %s)", ver, runtime.Version())
	return Response{
		Text: text,
		Data: map[string]any{"version": ver, "go": runtime.Version()},
	}, nil
}
