package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

type threadConfirmation struct {
	caller  Caller
	scope   store.ScopeRef
	expires time.Time
}

// RegisterThreadCommands exposes only the caller's private nonzero topic. A nil
// remove keeps the destructive action unavailable while qualification is pending.
func RegisterThreadCommands(r *Router, st *store.Store, remove func(context.Context, store.ScopeRef, int) error) {
	registerThreadCommands(r, st, remove, time.Now)
}

func registerThreadCommands(r *Router, st *store.Store, remove func(context.Context, store.ScopeRef, int) error, clock func() time.Time) {
	var mu sync.Mutex
	pending := map[string]threadConfirmation{}
	r.Register("thread", "thread", "(owner, private) topic info or confirmed deletion", PermOwnerOnly, func(ctx context.Context, cmd Command) (Response, error) {
		c := cmd.Caller
		if c.Source != SourceTelegram || c.ChatKind != "private" || c.ChatID <= 0 || c.UserID != c.ChatID || c.ThreadID < 0 || c.MessageID <= 0 {
			return Response{Code: 1, Text: "Используйте /thread внутри темы личного чата с ботом."}, nil
		}
		action := "info"
		if len(cmd.Args) > 0 {
			action = cmd.Args[0]
		}
		if len(cmd.Args) > 2 || (action != "info" && action != "delete") || (action == "info" && len(cmd.Args) > 1) {
			return Response{Code: 1, Text: "Используйте /thread info или /thread delete."}, nil
		}
		conv, err := st.SnapshotConversation(ctx, fmt.Sprintf("tg:%d", c.ChatID), c.ThreadID, store.VisibilityConversation)
		if err != nil {
			return Response{Code: 1, Text: "Регистрация чата недоступна."}, nil
		}

		if c.ThreadID == 0 {
			if action != "info" {
				return Response{Code: 1, Text: "Основной чат удалить нельзя. Используйте /thread delete внутри нужной темы."}, nil
			}
			intents, err := st.PendingTopicDeletions(ctx)
			if err != nil {
				return Response{Code: 1, Text: "Список операций недоступен."}, nil
			}
			var lines []string
			for _, d := range intents {
				if d.Scope.NamespaceID == conv.Scope.NamespaceID && d.Scope.NamespaceEpoch == conv.Scope.NamespaceEpoch {
					state := threadDeletionState(d.State)
					if d.Scope != conv.Scope {
						state += " (требуется сверка регистрации)"
					}
					lines = append(lines, fmt.Sprintf("Тема %d: %s", d.ThreadID, state))
				}
			}
			if len(lines) == 0 {
				return Response{Text: "Незавершённых операций удаления нет. Это локальный журнал, не список тем Telegram."}, nil
			}
			return Response{Text: "Локальные незавершённые операции (повторные запросы не отправляются):\n" + strings.Join(lines, "\n")}, nil
		}
		d, found, err := st.GetTopicDeletion(ctx, conv.Scope, c.ThreadID)
		if err != nil {
			return Response{Code: 1, Text: "Состояние темы требует сверки регистрации."}, nil
		}
		if action == "info" {
			state := "нет локальной операции удаления"
			if found {
				state = threadDeletionState(d.State)
			}
			return Response{Text: fmt.Sprintf("Тема %d: %s. Это локальное состояние, не проверка существования темы в Telegram.", c.ThreadID, state)}, nil
		}
		if remove == nil {
			return Response{Code: 1, Text: "Удаление тем пока отключено: требуется проверка безопасной остановки и доставки. /thread info показывает локальное состояние."}, nil
		}
		if found && d.State != "refused" {
			return Response{Code: 1, Text: "Для этой темы уже сохранена операция удаления: " + threadDeletionState(d.State) + ". Повторный запрос Telegram не отправлен."}, nil
		}
		if !cmd.Confirmed {
			if len(cmd.Args) != 1 {
				return Response{Code: 1, Text: "Используйте /thread delete без дополнительных аргументов."}, nil
			}
			var bytes [16]byte
			if _, err = rand.Read(bytes[:]); err != nil {
				return Response{}, err
			}
			tok := hex.EncodeToString(bytes[:])
			now := clock()
			mu.Lock()
			for k, v := range pending {
				if !now.Before(v.expires) {
					delete(pending, k)
				}
			}
			if len(pending) >= 256 {
				mu.Unlock()
				return Response{Code: 1, Text: "Слишком много ожидающих подтверждений. Повторите позже."}, nil
			}
			pending[tok] = threadConfirmation{c, conv.Scope, now.Add(5 * time.Minute)}
			mu.Unlock()
			cmd.Args = []string{"delete", tok}
			return Confirm(cmd, fmt.Sprintf("Удалить тему %d? Все сообщения Telegram в этой теме будут потеряны. Контекст сессий будет закрыт; общие файлы и память сохранятся.", c.ThreadID)), nil
		}
		if len(cmd.Args) != 2 {
			return Response{Code: 1, Text: "Подтверждение отсутствует или устарело."}, nil
		}
		mu.Lock()
		p, ok := pending[cmd.Args[1]]
		valid := ok && clock().Before(p.expires) && p.caller.ChatID == c.ChatID && p.caller.UserID == c.UserID && p.caller.ThreadID == c.ThreadID && p.scope == conv.Scope
		if valid {
			delete(pending, cmd.Args[1])
		}
		mu.Unlock()
		if !valid {
			return Response{Code: 1, Text: "Подтверждение отсутствует или устарело."}, nil
		}
		if err = remove(ctx, p.scope, c.ThreadID); err != nil {
			return Response{Code: 1, Text: "Удаление не запущено: требуется сверка состояния темы."}, nil
		}
		return Response{Text: "Запрос удаления принят. Ожидается остановка работы этой темы; результат будет сообщён в основном чате."}, nil
	})
}

func threadDeletionState(state string) string {
	switch state {
	case "deleting":
		return "удаление ожидает завершения"
	case "uncertain":
		return "исход запроса неизвестен; требуется сверка"
	case "deleted":
		return "удалена в Telegram; локальная очистка ожидает завершения"
	case "retired":
		return "удалена; контекст сессий закрыт"
	case "refused":
		return "удаление отклонено или не отправлено"
	default:
		return "состояние неизвестно"
	}
}
