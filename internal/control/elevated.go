package control

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"strconv"
	"sync"
	"time"
)

type ElevatedSession interface {
	Active() bool
	Generation() string
	Status() string
	Start(context.Context, string) (string, error)
	Close(context.Context, string) error
	Log(context.Context, int) (string, error)
	StopAnswer() bool
}

func RegisterElevated(r *Router, owner int64, e ElevatedSession) {
	var confirmMu sync.Mutex
	pending := ""
	r.Register("root", "root", "(owner, private) host-root session", PermOwnerOnly, func(ctx context.Context, cmd Command) (Response, error) {
		if cmd.Caller.Source != SourceTelegram || cmd.Caller.ChatKind != "private" || cmd.Caller.ChatID != owner || cmd.Caller.UserID != owner || cmd.Caller.MessageID <= 0 {
			return Response{Code: 1, Text: "ROOT доступен только владельцу в личном Telegram-чате."}, nil
		}
		action := ""
		if len(cmd.Args) > 0 {
			action = cmd.Args[0]
		}
		if len(cmd.Args) > 2 {
			return Response{Code: 1, Text: "Используйте /root, /root status, /root log или /root stop."}, nil
		}
		controls := rootControls(e)
		switch action {
		case "":
			if e.Active() {
				return Response{Text: e.Status(), Buttons: controls}, nil
			}
			return Response{Text: "Выберите провайдера для отдельной ROOT-сессии:", Buttons: pickerButtons("root", []string{"claude", "codex"}, "")}, nil
		case "status":
			return Response{Text: e.Status(), Buttons: controls}, nil
		case "log":
			limit := 20
			if len(cmd.Args) == 2 {
				var err error
				limit, err = strconv.Atoi(cmd.Args[1])
				if err != nil || limit < 1 || limit > 100 {
					return Response{Code: 1, Text: "/root log [1..100]"}, nil
				}
			}
			text, err := e.Log(ctx, limit)
			return Response{Text: text}, err
		case "cancel":
			confirmMu.Lock()
			if e.Active() {
				confirmMu.Unlock()
				return Response{Text: "ROOT уже запущен. Для закрытия используйте /root stop.\n" + e.Status(), Buttons: controls}, nil
			}
			if len(cmd.Args) == 2 && cmd.Args[1] != pending {
				confirmMu.Unlock()
				return Response{Text: "Это подтверждение ROOT уже устарело."}, nil
			}
			pending = ""
			confirmMu.Unlock()
			return Response{Text: "Запуск ROOT отменён. Новая сессия не создана."}, nil
		case "interrupt":
			if len(cmd.Args) == 2 && cmd.Args[1] != e.Generation() {
				return Response{Text: "⚠️ ROOT: кнопка относится к завершённой сессии."}, nil
			}
			text := "⚠️ ROOT: активного ответа нет."
			if e.StopAnswer() {
				text = "⚠️ ROOT: остановка ответа запрошена; ожидаю подтверждения. Сессия сохраняется."
			}
			return Response{Text: text, Buttons: controls}, nil
		case "stop":
			if len(cmd.Args) == 2 && cmd.Args[1] != e.Generation() {
				return Response{Text: "⚠️ ROOT: кнопка закрытия относится к завершённой сессии. Используйте /root status."}, nil
			}
			confirmMu.Lock()
			pending = ""
			confirmMu.Unlock()
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if err := e.Close(stopCtx, "owner close"); err != nil {
				return Response{Code: 1, Text: "⚠️ ROOT: закрытие не подтверждено. Обычная маршрутизация не возобновлена. Проверьте Docker и повторите /root stop."}, nil
			}
			return Response{Text: "ROOT закрыт: контейнер удалён. Обычная переписка восстановлена. Выполненные действия на хосте не отменяются."}, nil
		case "claude", "codex":
			if e.Active() {
				return Response{Text: e.Status(), Buttons: controls}, nil
			}
			if !cmd.Confirmed {
				confirmMu.Lock()
				pending = uuid.NewString()
				cmd.Args = []string{action, pending}
				confirmMu.Unlock()
				resp := Confirm(cmd, fmt.Sprintf("⚠️ Запустить ROOT · %s?\nАгент получает полный доступ к хосту: файлам, процессам и контейнерам. Содержимое прочитанных файлов и страниц может повлиять на его действия. Это не песочница.\nЖурнал содержит только docker exec контроллера, не все команды агента, и не защищён от host root.\nОтдельный вход провайдера обязателен; credentials обычного чата не копируются. Новые текстовые сообщения направятся в ROOT до /root stop.", action))
				resp.Buttons[0][0].Label = "Запустить ROOT"
				resp.Buttons[0][1] = Button{Label: "Отмена", Command: "root", Args: []string{"cancel", cmd.Args[1]}}
				return resp, nil
			}
			confirmMu.Lock()
			valid := len(cmd.Args) == 2 && pending != "" && cmd.Args[1] == pending
			pending = ""
			if !valid {
				confirmMu.Unlock()
				return Response{Code: 1, Text: "Подтверждение ROOT устарело. Повторите /root."}, nil
			}
			startCtx, startCancel := context.WithTimeout(ctx, 30*time.Second)
			text, err := e.Start(startCtx, action)
			startCancel()
			confirmMu.Unlock()
			if err != nil {
				return Response{Code: 1, Text: "⚠️ " + err.Error()}, nil
			}
			return Response{Text: text, Buttons: rootControls(e)}, nil
		default:
			return Response{Code: 1, Text: "Неизвестное действие. Используйте /root."}, nil
		}
	})
}

func rootControls(e ElevatedSession) [][]Button {
	generation := e.Generation()
	return [][]Button{{{Label: "Статус", Command: "root", Args: []string{"status"}}, {Label: "Журнал", Command: "root", Args: []string{"log"}}}, {{Label: "Остановить ответ", Command: "root", Args: []string{"interrupt", generation}}, {Label: "Закрыть ROOT", Command: "root", Args: []string{"stop", generation}, Style: StyleDanger}}}
}
