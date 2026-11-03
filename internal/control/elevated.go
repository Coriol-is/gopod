package control

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
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

type rootControl struct {
	mu      sync.Mutex
	pending string
	owner   int64
	session ElevatedSession
	login   Handler
}

func RegisterElevated(r *Router, owner int64, e ElevatedSession, login ...Handler) func(Handler) {
	handler := &rootControl{owner: owner, session: e}
	if len(login) > 0 {
		handler.login = login[0]
	}
	r.Register("root", "root", "(owner, private) host-root session", PermOwnerOnly, handler.dispatch)
	return func(login Handler) { handler.mu.Lock(); defer handler.mu.Unlock(); handler.login = login }
}

func (r *rootControl) dispatch(ctx context.Context, cmd Command) (Response, error) {
	c := cmd.Caller
	if c.Source != SourceTelegram || c.ChatKind != "private" || c.ChatID != r.owner || c.UserID != r.owner || c.MessageID <= 0 {
		return Response{Code: 1, Text: "ROOT доступен только владельцу в личном Telegram-чате."}, nil
	}
	if len(cmd.Args) > 2 {
		return Response{Code: 1, Text: "Используйте /root, /root status, /root log или /root stop."}, nil
	}
	// Serialize generation checks with every mutating command, including Start.
	r.mu.Lock()
	defer r.mu.Unlock()
	action := ""
	if len(cmd.Args) > 0 {
		action = cmd.Args[0]
	}
	switch action {
	case "":
		return r.picker(), nil
	case "login":
		if len(cmd.Args) == 2 && cmd.Args[1] != r.session.Generation() {
			return Response{Code: 1, Text: "Кнопка ROOT устарела; используйте /root login."}, nil
		}
		if !r.session.Active() {
			return Response{Code: 1, Text: "Сначала /root: выберите провайдера и подтвердите запуск. Затем /root login."}, nil
		}
		if r.login == nil {
			return Response{Code: 1, Text: "ROOT login недоступен."}, nil
		}
		return r.login(ctx, cmd)
	case "status":
		return r.status(), nil
	case "log":
		return r.log(ctx, cmd.Args)
	case "cancel":
		return r.cancel(cmd.Args), nil
	case "stop", "interrupt":
		return r.stop(ctx, action, cmd.Args), nil
	case "claude", "codex":
		return r.start(ctx, cmd, action), nil
	default:
		return Response{Code: 1, Text: "Неизвестное действие. Используйте /root."}, nil
	}
}
func (r *rootControl) status() Response {
	return Response{Text: r.session.Status(), Buttons: rootControls(r.session)}
}
func (r *rootControl) picker() Response {
	if r.session.Active() {
		return r.status()
	}
	return Response{Text: "Выберите провайдера для отдельной ROOT-сессии:", Buttons: pickerButtons("root", []string{"claude", "codex"}, "")}
}
func (r *rootControl) log(ctx context.Context, args []string) (Response, error) {
	limit := 20
	if len(args) == 2 {
		var err error
		limit, err = strconv.Atoi(args[1])
		if err != nil || limit < 1 || limit > 100 {
			return Response{Code: 1, Text: "/root log [1..100]"}, nil
		}
	}
	text, err := r.session.Log(ctx, limit)
	return Response{Text: text}, err
}
func (r *rootControl) cancel(args []string) Response {
	if r.session.Active() {
		res := r.status()
		res.Text = "ROOT уже запущен. Для закрытия используйте /root stop.\n" + res.Text
		return res
	}
	if len(args) == 2 && args[1] != r.pending {
		return Response{Text: "Это подтверждение ROOT уже устарело."}
	}
	r.pending = ""
	return Response{Text: "Запуск ROOT отменён. Новая сессия не создана."}
}
func (r *rootControl) stop(ctx context.Context, action string, args []string) Response {
	if len(args) == 2 && args[1] != r.session.Generation() {
		return Response{Text: "⚠️ ROOT: кнопка относится к завершённой сессии. Используйте /root status."}
	}
	if action == "interrupt" {
		text := "⚠️ ROOT: активного ответа нет."
		if r.session.StopAnswer() {
			text = "⚠️ ROOT: остановка ответа запрошена; ожидаю подтверждения. Сессия сохраняется."
		}
		return Response{Text: text, Buttons: rootControls(r.session)}
	}
	r.pending = ""
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := r.session.Close(closeCtx, "owner close"); err != nil {
		return Response{Code: 1, Text: "⚠️ ROOT: закрытие не подтверждено. Обычная маршрутизация не возобновлена. Проверьте Docker и повторите /root stop."}
	}
	return Response{Text: "ROOT закрыт: контейнер удалён. Обычная переписка восстановлена. Выполненные действия на хосте не отменяются."}
}
func (r *rootControl) start(ctx context.Context, cmd Command, provider string) Response {
	if r.session.Active() {
		return r.status()
	}
	if !cmd.Confirmed {
		return r.confirm(cmd, provider)
	}
	valid := len(cmd.Args) == 2 && r.pending != "" && cmd.Args[1] == r.pending
	r.pending = ""
	if !valid {
		return Response{Code: 1, Text: "Подтверждение ROOT устарело. Повторите /root."}
	}
	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	text, err := r.session.Start(startCtx, provider)
	if err != nil {
		return Response{Code: 1, Text: "⚠️ " + err.Error()}
	}
	return Response{Text: text, Buttons: rootControls(r.session)}
}
func (r *rootControl) confirm(cmd Command, provider string) Response {
	r.pending = uuid.NewString()
	cmd.Args = []string{provider, r.pending}
	res := Confirm(cmd, fmt.Sprintf("⚠️ Запустить ROOT · %s?\nАгент получает полный доступ к хосту: файлам, процессам и контейнерам. Содержимое прочитанных файлов и страниц может повлиять на его действия. Это не песочница.\nЖурнал содержит только docker exec контроллера, не все команды агента, и не защищён от host root.\nОтдельный вход провайдера обязателен; credentials обычного чата не копируются. Новые текстовые сообщения направятся в ROOT до /root stop.", provider))
	res.Buttons[0][0].Label = "Запустить ROOT"
	res.Buttons[0][1] = Button{Label: "Отмена", Command: "root", Args: []string{"cancel", r.pending}}
	return res
}
func rootControls(e ElevatedSession) [][]Button {
	generation := e.Generation()
	return [][]Button{{{Label: "Войти в ROOT-профиль", Command: "root", Args: []string{"login", generation}}}, {{Label: "Статус", Command: "root", Args: []string{"status"}}, {Label: "Журнал", Command: "root", Args: []string{"log"}}}, {{Label: "Остановить ответ", Command: "root", Args: []string{"interrupt", generation}}, {Label: "Закрыть ROOT", Command: "root", Args: []string{"stop", generation}, Style: StyleDanger}}}
}
