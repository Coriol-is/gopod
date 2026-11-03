package telegram

import (
	"context"
	"github.com/Coriol-is/gopod/internal/memory"
	"strings"

	"github.com/go-telegram/bot/models"
)

type ElevatedInput interface {
	Active() bool
	FreshAfter() int64
	TakeNotice() string
	Run(context.Context, string) (string, error)
	StopAnswer() bool
}

// handleElevated runs before ordinary persistence/media/queue/recovery paths.
func (b *Bot) handleElevated(ctx context.Context, m *models.Message) bool {
	if b.elevated == nil || m.Chat.Type != models.ChatTypePrivate || m.Chat.ID != b.ownerChatID || m.From == nil || m.From.ID != b.ownerChatID {
		return false
	}
	if notice := b.elevated.TakeNotice(); notice != "" {
		b.replyTo(ctx, m.Chat.ID, inboundThreadID(m), m.ID, notice)
	}
	text := strings.TrimSpace(m.Text)
	rootCommand := strings.HasPrefix(text, "/root")
	if !b.elevated.Active() && !rootCommand {
		return false
	}
	if m.ForwardOrigin != nil || m.ViaBot != nil || m.SenderChat != nil || m.IsAutomaticForward || memory.IsGroupSensitiveText(text) {
		b.replyTo(ctx, m.Chat.ID, inboundThreadID(m), m.ID, "⚠️ ROOT: пересланное, автоматическое или содержащее данные входа сообщение не выполнено. Отправьте собственный текстовый запрос без credentials.")
		return true
	}
	if strings.HasPrefix(text, "/") {
		if text == "/stop" && b.elevated.Active() {
			message := "⚠️ ROOT: активного ответа нет."
			if b.elevated.StopAnswer() {
				message = "⚠️ ROOT: остановка ответа запрошена; ожидаю подтверждения. Сессия сохраняется."
			}
			b.replyTo(ctx, m.Chat.ID, inboundThreadID(m), m.ID, message)
		} else {
			b.dispatchSlash(ctx, m, text)
		}
		return true
	}
	if int64(m.Date) < b.elevated.FreshAfter() {
		b.replyTo(ctx, m.Chat.ID, inboundThreadID(m), m.ID, "⚠️ ROOT: сообщение получено до подтверждённого запуска и не выполнено. Отправьте новый запрос.")
		return true
	}
	if text == "" || m.Document != nil || len(m.Photo) > 0 || m.Voice != nil || m.Audio != nil || m.Video != nil || m.VideoNote != nil {
		b.replyTo(ctx, m.Chat.ID, inboundThreadID(m), m.ID, "⚠️ ROOT: принимаются только новые текстовые запросы. Вложения не импортированы.")
		return true
	}
	b.replyTo(ctx, m.Chat.ID, inboundThreadID(m), m.ID, "⚠️ ROOT: проверяю готовность к запросу; /stop — остановить ответ.")
	reply, err := b.elevated.Run(ctx, text)
	if err != nil {
		reply = err.Error()
	}
	// Every separately delivered chunk has a host-owned marker. Raw tool JSON is never sent.
	runes := []rune(reply)
	if len(runes) == 0 {
		runes = []rune("Ответ пуст. /root log — журнал выполнения.")
	}
	for len(runes) > 0 {
		n := len(runes)
		if n > 1500 {
			n = 1500
		}
		b.replyTo(ctx, m.Chat.ID, inboundThreadID(m), m.ID, "⚠️ ROOT\n"+string(runes[:n]))
		runes = runes[n:]
	}
	return true
}
