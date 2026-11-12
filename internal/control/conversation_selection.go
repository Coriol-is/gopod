package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Coriol-is/gopod/internal/store"
)

type ConversationSelectionDeps struct {
	Read      func(context.Context, string, int) (store.ConversationSelection, error)
	Change    func(context.Context, store.ConversationSelection, string, string) (store.ConversationSelection, error)
	Folder    func(int64) string
	Providers []string
}

// RegisterConversationSelectionCommands replaces chat-wide provider/model UX.
// All changes retain Router authorization and bind confirmations to the settings
// revision, so an old button cannot overwrite intervening choices.
func RegisterConversationSelectionCommands(r *Router, d ConversationSelectionDeps) {
	for _, name := range []string{"provider", "model"} {
		r.Register(name, name, "выбор для текущего диалога", PermChatLocal, func(ctx context.Context, cmd Command) (Response, error) {
			if groupMemberDenied(cmd.Caller) {
				return notAuthorizedInGroup(), ErrNotAuthorized
			}
			folder := d.Folder(cmd.Caller.ChatID)
			if folder == "" {
				return Response{Text: "Chat not registered.", Code: 1}, nil
			}
			thread := cmd.Caller.ThreadID
			if cmd.Caller.ChatID < 0 {
				thread = 0
			}
			selected, err := d.Read(ctx, folder, thread)
			if err != nil {
				return Response{}, err
			}
			scope := fmt.Sprintf("диалог %d", thread)
			source := "свой выбор"
			if selected.ProviderInherited {
				source = "наследуется"
			}
			if cmd.Name == "provider" && len(cmd.Args) == 0 {
				choices := append(append([]string{}, d.Providers...), "inherit")
				return Response{Text: fmt.Sprintf("Провайдер: %s (%s). По умолчанию для чата: %s.\nОбласть: %s.\n/provider <имя> — только этот диалог; /provider inherit — наследовать; /provider default <имя> — изменить умолчание чата.", selected.Provider, source, selected.DefaultProvider, scope), Buttons: pickerButtons("provider", choices, selected.Provider)}, nil
			}
			modelText := selected.Model
			if modelText == "" {
				modelText = "default"
			}
			choices := []string{"default", "inherit"}
			if selected.Provider == "claude" {
				choices = append([]string{"fable", "opus", "sonnet", "haiku"}, choices...)
			}
			if cmd.Name == "model" && len(cmd.Args) == 0 {
				source = "свой выбор"
				if selected.ModelInherited {
					source = "наследуется"
				}
				return Response{Text: fmt.Sprintf("Модель: %s (%s), провайдер %s, %s.\n/model default — стандартная модель провайдера; /model inherit — наследовать модель чата. Выбор другого диалога не меняется.", modelText, source, selected.Provider, scope), Buttons: pickerButtons("model", choices, modelText)}, nil
			}
			args := slices.Clone(cmd.Args)
			if cmd.Confirmed && cmd.Name == "provider" && cmd.Caller.Source != SourceCLI {
				if len(args) < 2 {
					return Response{Code: 1, Text: "Подтверждение устарело."}, nil
				}
				if args[len(args)-1] != selectionConfirmation(selected) {
					return Response{Code: 1, Text: "Настройки изменились. Повторите команду."}, nil
				}
				args = args[:len(args)-1]
			}
			kind, value := "", ""
			if cmd.Name == "provider" {
				switch {
				case len(args) == 1 && args[0] == "inherit":
					kind = "provider"
				case len(args) == 1 && slices.Contains(d.Providers, args[0]):
					kind, value = "provider", args[0]
				case len(args) == 2 && args[0] == "default" && slices.Contains(d.Providers, args[1]):
					kind, value = "default_provider", args[1]
				default:
					return Response{Code: 1, Text: "Используйте /provider <имя>, /provider inherit или /provider default <имя>."}, nil
				}
				if !cmd.Confirmed {
					cmd.Args = append(args, selectionConfirmation(selected))
					question := fmt.Sprintf("Изменить провайдера для %s на %s?", scope, value)
					if value == "" {
						question = "Наследовать провайдера чата для " + scope + "?"
					}
					if kind == "default_provider" {
						question = "Изменить провайдера по умолчанию для чата на " + value + "? Затронуты только наследующие диалоги."
					}
					return Confirm(cmd, question), nil
				}
			} else {
				if len(args) != 1 || len(args[0]) > 128 || strings.ContainsAny(args[0], "\n\r\x00") {
					return Response{Code: 1, Text: "Используйте /model <имя>, default или inherit."}, nil
				}
				if !compatibleModel(selected.Provider, args[0]) {
					return Response{Code: 1, Text: "Модель относится к другому провайдеру. Выберите совместимую модель или /model default."}, nil
				}
				kind, value = "model", args[0]
				if value == "default" {
					value = ""
				}
				if value == "inherit" {
					kind, value = "inherit_model", ""
				}
			}
			changed, err := d.Change(ctx, selected, kind, value)
			if err != nil {
				if errors.Is(err, store.ErrSelectionChanged) || errors.Is(err, store.ErrStaleScope) {
					return Response{Code: 1, Text: "Настройки изменились. Повторите команду."}, nil
				}
				return Response{Code: 1, Text: "Не удалось изменить выбор. Проверьте доступность диалога и повторите команду."}, nil
			}
			model := changed.Model
			if model == "" {
				model = "default"
			}
			return Response{Text: fmt.Sprintf("Сохранено: %s / %s, %s. Применяется со следующей попытки; уже запущенная работа не меняется.", changed.Provider, model, scope)}, nil
		})
	}
}

func selectionConfirmation(s store.ConversationSelection) string {
	return fmt.Sprintf("selection:%s:%d:%d:%d", s.Scope.NamespaceID, s.Scope.NamespaceEpoch, s.Scope.RegistrationRevision, s.Revision)
}
func compatibleModel(provider, model string) bool {
	if model == "default" || model == "inherit" {
		return true
	}
	if provider == "codex" && (slices.Contains([]string{"fable", "opus", "sonnet", "haiku"}, model) || strings.HasPrefix(model, "claude")) {
		return false
	}
	if provider == "claude" && (strings.HasPrefix(model, "gpt-") || slices.Contains([]string{"o1", "o3", "o4-mini"}, model)) {
		return false
	}
	return true
}
