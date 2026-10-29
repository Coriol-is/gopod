package telegram

import (
	"unicode/utf16"

	"github.com/go-telegram/bot/models"
)

func sharedChatType(t models.ChatType) bool {
	return t == models.ChatTypeGroup || t == models.ChatTypeSupergroup
}

// isGroupOwner reports whether m was sent in a group by the owner's own Telegram
// user id (D048). The id is the From.ID Telegram supplied for the message, never
// text the sender typed. Anonymous admins and channel posts (sender_chat) and
// forwarded messages are not the owner, whoever they claim to be. The result is
// group-scoped identity: it decides the "owner" trigger and group-scoped
// commands, and never grants the owner tier, personal mounts or host commands.
func (b *Bot) isGroupOwner(m *models.Message) bool {
	return m != nil && b.ownerChatID > 0 && sharedChatType(m.Chat.Type) && m.Chat.ID < 0 &&
		m.From != nil && !m.From.IsBot && m.From.ID == b.ownerChatID &&
		m.SenderChat == nil && m.ForwardOrigin == nil
}

// isGroupOwnerUser is isGroupOwner for a pressed button: Telegram supplies the
// pressing user and the chat of the message that carries the button.
func (b *Bot) isGroupOwnerUser(chatKind string, chatID, user int64) bool {
	return b.ownerChatID > 0 && sharedChatType(models.ChatType(chatKind)) && chatID < 0 && user == b.ownerChatID
}

// addressesBot reports whether m addresses this bot: an @username mention or a
// text_mention of the bot in its entities, a command addressed to it, or a reply
// to one of its messages. Forwarded text is somebody else's words and never
// addresses the bot.
func (b *Bot) addressesBot(m *models.Message) bool {
	if m == nil || m.ForwardOrigin != nil {
		return false
	}
	if r := m.ReplyToMessage; r != nil && b.botID > 0 && r.From != nil && r.From.ID == b.botID &&
		!(m.IsTopicMessage && r.ID == m.MessageThreadID) {
		return true
	}
	username := b.botUsername()
	return b.entitiesAddressBot(m.Text, m.Entities, username) || b.entitiesAddressBot(m.Caption, m.CaptionEntities, username)
}

func (b *Bot) entitiesAddressBot(text string, entities []models.MessageEntity, username string) bool {
	for _, e := range entities {
		switch e.Type {
		case models.MessageEntityTypeTextMention:
			if b.botID > 0 && e.User != nil && e.User.ID == b.botID {
				return true
			}
		case models.MessageEntityTypeMention:
			if username != "" && equalFoldASCII(utf16Slice(text, e.Offset, e.Length), "@"+username) {
				return true
			}
		case models.MessageEntityTypeBotCommand:
			if username != "" && commandTargets(utf16Slice(text, e.Offset, e.Length), username) {
				return true
			}
		}
	}
	return false
}

// commandTargets reports whether a "/cmd@bot" entity names username.
func commandTargets(entity, username string) bool {
	for i := 0; i < len(entity); i++ {
		if entity[i] == '@' {
			return equalFoldASCII(entity[i+1:], username)
		}
	}
	return false
}

// utf16Slice cuts a Telegram entity (offset and length in UTF-16 code units).
func utf16Slice(s string, offset, length int) string {
	units := utf16.Encode([]rune(s))
	if offset < 0 || length <= 0 || offset+length > len(units) {
		return ""
	}
	return string(utf16.Decode(units[offset : offset+length]))
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
