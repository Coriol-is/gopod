package telegram

import (
	"context"
	"errors"
	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/go-telegram/bot/models"
	"strings"
)

type elevatedLoginStarter interface {
	BeginLogin(context.Context) (*runner.ElevatedLogin, error)
}

// LoginElevated is registered behind the root command's owner/private fence.
// It reserves the same per-chat slot as ordinary login, so codes cannot cross profiles.
func (b *Bot) LoginElevated(ctx context.Context, cmd control.Command) (control.Response, error) {
	c := cmd.Caller
	if c.Source != control.SourceTelegram || c.ChatKind != "private" || c.ChatID != b.ownerChatID || c.UserID != b.ownerChatID || c.MessageID <= 0 {
		return control.Response{Code: 1, Text: "Вход ROOT доступен только владельцу в личном чате."}, nil
	}
	backend, ok := b.elevated.(elevatedLoginStarter)
	if !ok {
		return control.Response{Code: 1, Text: "ROOT login недоступен."}, nil
	}
	if !b.logins.reserve(c.ChatID) {
		return control.Response{Code: 1, Text: "В этом чате уже идёт вход. Завершите его прежде, чем выбирать другой профиль."}, nil
	}
	handle, err := backend.BeginLogin(context.WithoutCancel(ctx))
	if err != nil {
		b.logins.release(c.ChatID)
		if errors.Is(err, runner.ErrElevatedAlreadyAuthenticated) {
			return control.Response{Text: "⚠️ ROOT: отдельный профиль уже авторизован. Повторный вход не запущен, текущая авторизация сохранена. Можно отправлять запросы."}, nil
		}
		return control.Response{Code: 1, Text: "ROOT: вход не запущен. Сессия должна быть запущена и свободна; /root status."}, nil
	}
	m := &models.Message{ID: c.MessageID, Chat: models.Chat{ID: c.ChatID, Type: models.ChatTypePrivate}, From: &models.User{ID: c.UserID}}
	session := &loginSession{elevated: handle, exec: handle, ctx: handle.Context(), cancel: func() { _ = handle.Close() }, chatID: c.ChatID, thread: c.ThreadID, folder: runner.ElevatedFolder, private: true, actorID: c.UserID, provider: handle.Provider.Name()}
	go func() {
		if !b.publishLogin(context.WithoutCancel(ctx), m, session, handle.Provider) {
			_ = handle.Close()
			b.logins.release(c.ChatID)
		}
	}()
	return control.Response{Text: "⚠️ ROOT · " + handle.Provider.Name() + ": запускаю вход в отдельный профиль. Дождитесь ссылки. /stop — отменить."}, nil
}

// Explicit aliases never reinterpret ordinary /login as an elevated sign-in.
func (b *Bot) dispatchElevatedLoginAlias(ctx context.Context, m *models.Message) bool {
	fields := strings.Fields(m.Text)
	if len(fields) != 2 || fields[1] != "elevated" {
		return false
	}
	b.dispatchSlash(ctx, m, "/root login")
	return true
}
