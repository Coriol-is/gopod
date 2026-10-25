package telegram

import (
	"strings"

	"github.com/go-telegram/bot/models"
)

// replyContextMax is how many runes of a replied-to or quoted message
// go into the prompt header.
const replyContextMax = 500

// inboundContext returns the header that tells the agent what a message
// points at, one line per fact, or "" when it points at nothing:
//
//	[forwarded from <origin>]
//	[in reply to <sender>: "<at most 500 runes>"]
//
// The reply line quotes Quote.Text when the user quoted a part of the
// message, else the replied message's text or caption. Replying to one
// of gopod's own answers marks the sender "(bot)", so "explain this"
// is about that answer.
//
// Privacy: only what the user's client shows them goes in. No user,
// chat or message ids, no usernames of named senders, no channel author
// signatures, and nothing about a hidden forward origin but the fact.
func inboundContext(m *models.Message) string {
	if m == nil {
		return ""
	}
	var lines []string
	if m.ForwardOrigin != nil {
		lines = append(lines, "[forwarded"+forwardFrom(m.ForwardOrigin)+"]")
	}
	if line := replyContextLine(m); line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// withInboundContext puts m's header above text, if m has one.
func withInboundContext(m *models.Message, text string) string {
	h := inboundContext(m)
	if h == "" {
		return text
	}
	return h + "\n\n" + text
}

// replyContextLine is the "[in reply to …]" line of m, or "".
func replyContextLine(m *models.Message) string {
	var name, text string
	switch r := m.ReplyToMessage; {
	case r != nil:
		if isTopicRoot(m, r) {
			return ""
		}
		name = messageSender(r)
		text = messageText(r)
	case m.ExternalReply != nil:
		// A reply to a message in another chat (or a quote of it).
		name = originName(&m.ExternalReply.Origin)
	default:
		return ""
	}
	if m.Quote != nil && m.Quote.Text != "" {
		text = m.Quote.Text
	}
	if name == "" {
		name = "a message"
	}
	if text == "" {
		return "[in reply to " + name + "]"
	}
	return "[in reply to " + name + `: "` + truncateRunes(text, replyContextMax) + `"]`
}

// isTopicRoot reports whether r, the reply_to_message of m, is only the
// service message that opened m's topic. Telegram sets it on every
// message in a topic that does not reply to anything; it is not a
// reply. In a group without topics message_thread_id names a reply
// chain instead (is_topic_message unset), and replying to its root is
// a real reply.
func isTopicRoot(m, r *models.Message) bool {
	if r.ForumTopicCreated != nil {
		return true
	}
	return m.IsTopicMessage && m.MessageThreadID != 0 && r.ID == m.MessageThreadID
}

// messageSender names who sent r: the chat for a message sent on behalf
// of a chat (channel post, anonymous admin), else the user.
func messageSender(r *models.Message) string {
	if r.SenderChat != nil && r.SenderChat.Title != "" {
		return r.SenderChat.Title
	}
	if r.From == nil {
		return ""
	}
	name := userName(r.From)
	if r.From.IsBot && name != "" {
		name += " (bot)"
	}
	return name
}

// forwardFrom is " from <origin>" for a forward origin, or "" when
// there is nothing to name.
func forwardFrom(o *models.MessageOrigin) string {
	if o.Type == models.MessageOriginTypeChannel && o.MessageOriginChannel != nil && o.MessageOriginChannel.Chat.Title != "" {
		return " from channel " + o.MessageOriginChannel.Chat.Title
	}
	if name := originName(o); name != "" {
		return " from " + name
	}
	return ""
}

// originName names a message origin the way the client shows it.
func originName(o *models.MessageOrigin) string {
	switch o.Type {
	case models.MessageOriginTypeUser:
		if o.MessageOriginUser != nil {
			return userName(&o.MessageOriginUser.SenderUser)
		}
	case models.MessageOriginTypeHiddenUser:
		// The user hid their account; the name they left is not
		// passed on either.
		return "a hidden user"
	case models.MessageOriginTypeChat:
		if o.MessageOriginChat != nil {
			return o.MessageOriginChat.SenderChat.Title
		}
	case models.MessageOriginTypeChannel:
		if o.MessageOriginChannel != nil {
			return o.MessageOriginChannel.Chat.Title
		}
	}
	return ""
}

// userName is a user's display name, or @username when they have none.
// Unlike senderDisplayName it never falls back to the numeric id.
func userName(u *models.User) string {
	if name := strings.TrimSpace(u.FirstName + " " + u.LastName); name != "" {
		return name
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	return ""
}
