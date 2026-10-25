package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const groupLoginConfirm = "login.group.confirm"
const groupLoginTTL = 5 * time.Minute
const groupLoginCooldown = 30 * time.Second
const groupLoginCap = 8

type groupLoginRequest struct {
	ChatID, ActorID int64
	Folder          string
	Expires         time.Time
}

// Group login is a two-phase owner-only operation. The group never receives a
// login URL, device code, error text or a fallback notification.
func (b *Bot) requestLoginHandoff(ctx context.Context, m *models.Message) bool {
	if m == nil {
		return false
	}
	b.cancelUnsafeLogin(m.Chat.ID)
	if (m.Chat.Type != models.ChatTypeGroup && m.Chat.Type != models.ChatTypeSupergroup) || m.Chat.ID >= 0 || b.ownerChatID <= 0 || userID(m.From) != b.ownerChatID || m.From.IsBot || b.router == nil || b.api == nil {
		return false
	}
	rc, ok := b.groupLoginTarget(ctx, m.Chat.ID)
	if !ok {
		return false
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return false
	}
	token := hex.EncodeToString(nonce[:])
	now := b.clock()
	b.loginHandoffMu.Lock()
	if b.loginHandoffs == nil {
		b.loginHandoffs = make(map[string]groupLoginRequest)
		b.loginHandoffRecent = make(map[int64]time.Time)
	}
	for key, request := range b.loginHandoffs {
		if !now.Before(request.Expires) {
			delete(b.loginHandoffs, key)
		}
	}
	for chat, last := range b.loginHandoffRecent {
		if now.Sub(last) >= groupLoginCooldown {
			delete(b.loginHandoffRecent, chat)
		}
	}
	last, recent := b.loginHandoffRecent[m.Chat.ID]
	if recent && now.Sub(last) < groupLoginCooldown || len(b.loginHandoffs) >= groupLoginCap || !recent && len(b.loginHandoffRecent) >= groupLoginCap {
		b.loginHandoffMu.Unlock()
		return false
	}
	b.loginHandoffs[token] = groupLoginRequest{ChatID: m.Chat.ID, ActorID: b.ownerChatID, Folder: rc.Folder, Expires: now.Add(groupLoginTTL)}
	b.loginHandoffRecent[m.Chat.ID] = now
	b.loginHandoffMu.Unlock()
	response := control.Response{Text: fmt.Sprintf("Authenticate the separate runtime for Telegram group `%d`? Its members will use that group's authenticated runtime. The sign-in URL and code will appear only in this private chat after confirmation.", m.Chat.ID), Code: control.CodeConfirm, Buttons: [][]control.Button{{
		{Label: "Authenticate group", Command: groupLoginConfirm, Args: []string{token}, Confirmed: true, Style: control.StyleDanger},
		{Label: "Cancel", Command: groupLoginConfirm, Args: []string{token, "cancel"}, Confirmed: true},
	}}}
	text, markup := b.commandMessage(b.ownerChatID, response)
	id, err := b.sendPrivateLoginText(ctx, b.ownerChatID, 0, text, markup)
	if err != nil || id == 0 {
		b.loginHandoffMu.Lock()
		delete(b.loginHandoffs, token)
		b.loginHandoffMu.Unlock()
		b.log.Warn("private login handoff not delivered", slog.String("error_class", "private_delivery_failed"))
		return false
	}
	return true
}

func (b *Bot) groupLoginTarget(ctx context.Context, chatID int64) (store.RegisteredChat, bool) {
	if b.store == nil || chatID >= 0 {
		return store.RegisteredChat{}, false
	}
	target, err := b.store.GetRegistered(ctx, buildChatJID(chatID))
	if err != nil || target.IsOwner || target.Folder == "owner" {
		return store.RegisteredChat{}, false
	}
	owner, err := b.store.GetRegistered(ctx, buildChatJID(b.ownerChatID))
	if err == nil && owner.Folder == target.Folder {
		return store.RegisteredChat{}, false
	}
	if err != nil && !errors.Is(err, store.ErrChatNotRegistered) {
		return store.RegisteredChat{}, false
	}
	return target, true
}

func (b *Bot) registerLoginHandoff() {
	if b.router == nil {
		return
	}
	b.router.Register(groupLoginConfirm, "", "confirm private group authentication", control.PermOwnerOnly, func(ctx context.Context, cmd control.Command) (control.Response, error) {
		c := cmd.Caller
		if c.Source != control.SourceTelegram || c.ChatKind != "private" || c.ChatID != b.ownerChatID || c.UserID != b.ownerChatID || !c.IsOwner || !cmd.Confirmed {
			return control.Response{Text: loginPrivateToast, Code: 1}, nil
		}
		if len(cmd.Args) < 1 || len(cmd.Args) > 2 || len(cmd.Args) == 2 && cmd.Args[1] != "cancel" {
			return control.Response{Text: "This sign-in confirmation has expired.", Code: 1}, nil
		}
		token := cmd.Args[0]
		b.loginHandoffMu.Lock()
		request, exists := b.loginHandoffs[token]
		if !exists || !b.clock().Before(request.Expires) || request.ActorID != c.UserID {
			delete(b.loginHandoffs, token)
			b.loginHandoffMu.Unlock()
			return control.Response{Text: "This sign-in confirmation has expired.", Code: 1}, nil
		}
		if len(cmd.Args) == 2 {
			delete(b.loginHandoffs, token)
			b.loginHandoffMu.Unlock()
			return control.Response{Text: "Group sign-in cancelled."}, nil
		}
		target, ok := b.groupLoginTarget(ctx, request.ChatID)
		if !ok || target.Folder != request.Folder {
			delete(b.loginHandoffs, token)
			b.loginHandoffMu.Unlock()
			return control.Response{Text: "Group registration changed. Request sign-in again from the group.", Code: 1}, nil
		}
		if !b.logins.reserve(c.ChatID) {
			b.loginHandoffMu.Unlock()
			return control.Response{Text: loginTextAlready, Code: 1}, nil
		}
		delete(b.loginHandoffs, token)
		b.loginHandoffMu.Unlock()
		message := &models.Message{Chat: models.Chat{ID: c.ChatID, Type: models.ChatTypePrivate}, From: &models.User{ID: c.UserID}}
		// Registration and actor are resolved from trusted sources, not callback data.
		// The display chat is private; the runtime and its tier remain group-scoped.
		go b.loginReservedGroup(ctx, message, c.ThreadID, target)
		return control.Response{Text: "Starting group authentication in this private chat."}, nil
	})
}

// Only the consumed, authorized private confirmation calls this path.
func (b *Bot) loginReservedGroup(ctx context.Context, m *models.Message, thread int, target store.RegisteredChat) {
	stored := false
	defer func() {
		if !stored {
			b.logins.release(m.Chat.ID)
		}
	}()
	if !privateLoginMessage(m) || m.Chat.ID != b.ownerChatID || userID(m.From) != b.ownerChatID {
		return
	}
	if b.loginRun != nil {
		stored = b.loginRun(ctx, m, thread, target)
	} else {
		stored = b.runLoginTarget(ctx, m, thread, target, true)
	}
}

// Credential-bearing login messages never use generic send fallbacks or raw
// transport error logs. Every caller already established a private recipient.
func (b *Bot) sendPrivateLoginText(ctx context.Context, chatID int64, thread int, text string, markup models.ReplyMarkup) (int, error) {
	if chatID <= 0 || b.api == nil {
		return 0, errors.New("private login delivery unavailable")
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sent, err := b.api.SendMessage(sendCtx, &bot.SendMessageParams{ChatID: chatID, MessageThreadID: thread, Text: markdownToTelegramHTML(text), ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
	if err != nil || sent == nil || sent.ID == 0 {
		return 0, errors.New("private login delivery failed")
	}
	return sent.ID, nil
}
func (b *Bot) replyLoginText(ctx context.Context, chatID int64, thread int, text string) {
	if _, err := b.sendPrivateLoginText(ctx, chatID, thread, text, nil); err != nil {
		b.log.Warn("private login message not delivered", slog.String("error_class", "private_delivery_failed"))
	}
}
