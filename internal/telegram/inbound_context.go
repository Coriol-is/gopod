package telegram

import (
	"context"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/store"
)

// albumWindow is how long an album waits for its next item. Telegram
// delivers an album as one message per item, all with one
// media_group_id, usually within a few hundred milliseconds.
const albumWindow = time.Second

// albumKey names one album being collected.
type albumKey struct {
	chat  int64
	group string
}

// albumBuf is an album being collected: its messages so far, the chat
// it belongs to, and the timer that flushes it.
type albumBuf struct {
	ctx  context.Context
	rc   store.RegisteredChat
	msgs []*models.Message
	stop func() bool
}

// collectAlbum adds m, one item of an album, to its album, and
// (re)starts the album's window: one albumWindow after the last item
// arrives the album becomes one turn (flushAlbum). Unregistered chats
// are dropped here, like single photos.
func (b *Bot) collectAlbum(ctx context.Context, m *models.Message) {
	rc, ok := b.resolveRegistered(ctx, m)
	if !ok {
		return
	}
	clock := b.albumClock
	if clock == nil {
		clock = realClock{}
	}
	k := albumKey{m.Chat.ID, m.MediaGroupID}

	b.albumMu.Lock()
	defer b.albumMu.Unlock()
	if b.albums == nil {
		b.albums = make(map[albumKey]*albumBuf)
	}
	a := b.albums[k]
	if a == nil {
		a = &albumBuf{ctx: ctx, rc: rc}
		b.albums[k] = a
	} else if a.stop != nil {
		a.stop()
	}
	a.msgs = append(a.msgs, m)
	a.stop = clock.AfterFunc(albumWindow, func() { b.flushAlbum(k, a) })
}

// flushAlbum takes album a out of collection and queues it. A timer
// that fires after another item restarted the window finds the album
// gone or already taken and does nothing.
func (b *Bot) flushAlbum(k albumKey, a *albumBuf) {
	b.albumMu.Lock()
	if b.albums[k] != a {
		b.albumMu.Unlock()
		return
	}
	delete(b.albums, k)
	msgs := a.msgs
	b.albumMu.Unlock()

	sort.Slice(msgs, func(i, j int) bool { return msgs[i].ID < msgs[j].ID })
	b.enqueueAlbum(a.ctx, a.rc, msgs)
}

// flushPendingAlbums queues every album still in its window at once.
// Run calls it after the long poll stops: Telegram's offset already
// covers those items, so an album left in memory would be lost; queued,
// it is a turn row that Recover replays (D020).
func (b *Bot) flushPendingAlbums() {
	b.albumMu.Lock()
	pending := make(map[albumKey]*albumBuf, len(b.albums))
	for k, a := range b.albums {
		pending[k] = a
	}
	b.albumMu.Unlock()
	for k, a := range pending {
		b.flushAlbum(k, a) // skips one its own timer took meanwhile
	}
}

// enqueueAlbum downloads every photo and document of an album and
// queues one turn for it, answering the first item. The prompt is the
// album's caption (Telegram puts it on one item) followed by the list
// of uploaded paths. Items of other kinds (video, audio) are skipped.
func (b *Bot) enqueueAlbum(ctx context.Context, rc store.RegisteredChat, msgs []*models.Message) {
	// The album is flushed by a timer or at shutdown, when the bot's
	// context may already be cancelled: download and enqueue detached.
	ctx, cancel := downloadContext(ctx)
	defer cancel()
	first := msgs[0]
	thread := inboundThreadID(first)
	var caption string
	var paths []string
	failed := 0
	for _, m := range msgs {
		if caption == "" {
			caption = strings.TrimSpace(messageText(m))
		}
		var p string
		var err error
		switch {
		case len(m.Photo) > 0:
			_, p, err = b.downloadPhoto(ctx, rc.Folder, m.Photo)
		case m.Document != nil:
			_, p, err = b.downloadDocument(ctx, rc.Folder, m.Document)
		default:
			b.log.Debug("album item of unsupported kind skipped",
				slog.Int64("chat_id", m.Chat.ID), slog.Int("msg_id", m.ID))
			continue
		}
		if err != nil {
			failed++
			b.log.Error("download album item failed",
				slog.Int64("chat_id", m.Chat.ID), slog.Int("msg_id", m.ID), slog.Any("err", err))
			continue
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		if failed > 0 {
			b.replyTo(ctx, first.Chat.ID, thread, first.ID, "Failed to download album.")
			return
		}
		if caption == "" {
			return // nothing the agent could use
		}
	}

	text := caption
	if len(paths) > 0 {
		if text == "" {
			text = "The user sent an album. Look at the attached files."
		}
		text += "\n\n[Album attached: " + strings.Join(paths, ", ") + "]"
		if failed > 0 {
			text += "\n[" + strconv.Itoa(failed) + " more item(s) could not be downloaded]"
		}
	}
	filePath := ""
	if len(paths) > 0 {
		filePath = paths[0]
	}
	b.enqueueTurn(ctx, first, rc, text, filePath, false)
}

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
	name, text = headerSafe(name), headerSafe(text)
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
	if o.Type == models.MessageOriginTypeChannel && o.MessageOriginChannel != nil {
		if title := headerSafe(o.MessageOriginChannel.Chat.Title); title != "" {
			return " from channel " + title
		}
	}
	if name := headerSafe(originName(o)); name != "" {
		return " from " + name
	}
	return ""
}

// headerSafe makes third-party text (a replied message, a quote, a
// sender or chat name) safe inside a header line: line breaks and
// other control characters become single spaces, and the characters
// that delimit the header (`"`, `[`, `]`) are replaced, so the text
// can neither end the header early nor start a fake line of its own.
func headerSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '"':
			return '\''
		case r == '[':
			return '('
		case r == ']':
			return ')'
		case unicode.IsControl(r), r == ' ', r == ' ':
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
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
