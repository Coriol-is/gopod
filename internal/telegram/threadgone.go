package telegram

// A private-chat topic can be deleted (or closed) while one of its turns
// is running. Every later send into it fails with a 400, and none of the
// usual responses fit: it is not a draft capability problem (drafts
// still work in the chat's other conversations), not transient (a retry
// of the whole turn reruns the agent and its tool side effects for
// nothing), and the reply must not be lost. So a "thread gone" error is
// permanent for the thread: gopod stops sending into it (drafts, typing,
// placeholders) and delivers replies to the main chat with a one-line
// note. A message arriving in the thread again clears the mark.

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/go-telegram/bot/models"
)

// threadGoneHints are lowercase substrings of the Bot API error
// description for a send into a topic that no longer exists. Telegram
// documents no error texts for Bot API methods; these are the ones seen
// in practice: the Bot API's own "Bad Request: message thread not
// found", and the MTProto errors it passes through for messages.send*
// ("Bad Request: TOPIC_DELETED", "Bad Request: TOPIC_CLOSED", see
// core.telegram.org/method/messages.sendMessage). Keep the list here.
var threadGoneHints = []string{
	"message thread not found",
	"topic_deleted",
	"topic_closed",
}

// errThreadGone marks a send that was refused, or skipped, because the
// target topic no longer exists.
var errThreadGone = errors.New("telegram: message thread gone")

// threadGoneNote prefixes a reply redirected from a gone topic to the
// main chat.
const threadGoneNote = "[The topic this conversation was in no longer exists, so the reply is here.]\n\n"

// isThreadGone reports whether err says the target topic no longer
// exists. Matched on the description, whatever the status code, since
// the library maps only some codes to sentinel errors.
func isThreadGone(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errThreadGone) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, h := range threadGoneHints {
		if strings.Contains(msg, h) {
			return true
		}
	}
	return false
}

// chatThread names one conversation of a chat.
type chatThread struct {
	chat   int64
	thread int
}

// threadGone reports whether (chatID, thread) is known gone. The main
// chat (thread 0) never is.
func (b *Bot) threadGone(chatID int64, thread int) bool {
	if thread == 0 {
		return false
	}
	b.threadsMu.Lock()
	defer b.threadsMu.Unlock()
	return b.goneThreads[chatThread{chatID, thread}]
}

// markThreadGone records that sends into (chatID, thread) fail because
// the topic no longer exists.
func (b *Bot) markThreadGone(chatID int64, thread int) {
	if thread == 0 {
		return
	}
	b.threadsMu.Lock()
	defer b.threadsMu.Unlock()
	if b.goneThreads == nil {
		b.goneThreads = make(map[chatThread]bool)
	}
	if !b.goneThreads[chatThread{chatID, thread}] {
		b.log.Warn("topic no longer exists, replying in the main chat",
			slog.Int64("chat_id", chatID), slog.Int("thread", thread))
	}
	b.goneThreads[chatThread{chatID, thread}] = true
}

// noteInboundThread clears the gone mark for the thread an inbound
// message arrived in: a message there proves the topic exists.
func (b *Bot) noteInboundThread(m *models.Message) {
	thread := inboundThreadID(m)
	if thread == 0 {
		return
	}
	b.threadsMu.Lock()
	defer b.threadsMu.Unlock()
	delete(b.goneThreads, chatThread{m.Chat.ID, thread})
}

// checkThreadGone marks (chatID, thread) gone when err says so and
// reports whether it did.
func (b *Bot) checkThreadGone(chatID int64, thread int, err error) bool {
	if thread == 0 || !isThreadGone(err) {
		return false
	}
	b.markThreadGone(chatID, thread)
	return true
}
