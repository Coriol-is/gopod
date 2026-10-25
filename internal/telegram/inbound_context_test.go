package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"
)

func privateMsg(text string) *models.Message {
	return &models.Message{
		ID:   20,
		Chat: models.Chat{ID: streamChat, Type: models.ChatTypePrivate},
		From: &models.User{ID: 42, FirstName: "Owner"},
		Text: text,
	}
}

func TestInboundContext_PlainMessageHasNone(t *testing.T) {
	if got := inboundContext(privateMsg("hi")); got != "" {
		t.Errorf("plain message header = %q, want empty", got)
	}
	if got := inboundContext(nil); got != "" {
		t.Errorf("nil message header = %q, want empty", got)
	}
}

func TestInboundContext_ReplyToBotAnswer(t *testing.T) {
	m := privateMsg("explain this")
	m.ReplyToMessage = &models.Message{
		ID:   7,
		From: &models.User{ID: 99, FirstName: "gopod", IsBot: true},
		Text: "Use a mutex here.",
	}
	got := inboundContext(m)
	want := `[in reply to gopod (bot): "Use a mutex here."]`
	if got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
}

func TestInboundContext_ReplyToMediaCaption(t *testing.T) {
	m := privateMsg("what is it?")
	m.ReplyToMessage = &models.Message{
		ID: 7, From: &models.User{ID: 5, FirstName: "Ann", LastName: "Lee"},
		Caption: "my cat",
		Photo:   []models.PhotoSize{{FileID: "p"}},
	}
	if got, want := inboundContext(m), `[in reply to Ann Lee: "my cat"]`; got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
	// No text at all: the sender is still named.
	m.ReplyToMessage.Caption = ""
	if got, want := inboundContext(m), `[in reply to Ann Lee]`; got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
}

func TestInboundContext_QuoteWins(t *testing.T) {
	m := privateMsg("why?")
	m.ReplyToMessage = &models.Message{
		ID:   7,
		From: &models.User{ID: 99, FirstName: "gopod", IsBot: true},
		Text: "First paragraph. Second paragraph with the claim.",
	}
	m.Quote = &models.TextQuote{Text: "the claim", Position: 40, IsManual: true}
	if got, want := inboundContext(m), `[in reply to gopod (bot): "the claim"]`; got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
}

func TestInboundContext_QuoteFromOtherChat(t *testing.T) {
	// A reply to a message in another chat carries external_reply and
	// the quote, but no reply_to_message.
	m := privateMsg("thoughts?")
	m.ExternalReply = &models.ExternalReplyInfo{Origin: models.MessageOrigin{
		Type:                 models.MessageOriginTypeChannel,
		MessageOriginChannel: &models.MessageOriginChannel{Chat: models.Chat{ID: -100, Title: "Go News", Username: "gonews"}, MessageID: 3},
	}}
	m.Quote = &models.TextQuote{Text: "Go 1.27 is out"}
	if got, want := inboundContext(m), `[in reply to Go News: "Go 1.27 is out"]`; got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
}

func TestInboundContext_ForwardFromUserChannelHidden(t *testing.T) {
	sig := "Jane Editor"
	for _, tc := range []struct {
		name   string
		origin models.MessageOrigin
		want   string
		absent []string
	}{
		{
			name: "user",
			origin: models.MessageOrigin{Type: models.MessageOriginTypeUser, MessageOriginUser: &models.MessageOriginUser{
				SenderUser: models.User{ID: 777001, FirstName: "Bob", Username: "bobby"},
			}},
			want:   "[forwarded from Bob]",
			absent: []string{"777001", "bobby"},
		},
		{
			name: "user with only a username",
			origin: models.MessageOrigin{Type: models.MessageOriginTypeUser, MessageOriginUser: &models.MessageOriginUser{
				SenderUser: models.User{ID: 777001, Username: "bobby"},
			}},
			want:   "[forwarded from @bobby]",
			absent: []string{"777001"},
		},
		{
			name: "hidden user",
			origin: models.MessageOrigin{Type: models.MessageOriginTypeHiddenUser, MessageOriginHiddenUser: &models.MessageOriginHiddenUser{
				SenderUserName: "Secret Person",
			}},
			want:   "[forwarded from a hidden user]",
			absent: []string{"Secret"},
		},
		{
			name: "channel",
			origin: models.MessageOrigin{Type: models.MessageOriginTypeChannel, MessageOriginChannel: &models.MessageOriginChannel{
				Chat: models.Chat{ID: -1001234, Title: "Go News", Username: "gonews"}, MessageID: 55, AuthorSignature: &sig,
			}},
			want:   "[forwarded from channel Go News]",
			absent: []string{"1234", "gonews", "Jane", "55"},
		},
		{
			name: "anonymous group admin",
			origin: models.MessageOrigin{Type: models.MessageOriginTypeChat, MessageOriginChat: &models.MessageOriginChat{
				SenderChat: models.Chat{ID: -1009, Title: "Team"}, AuthorSignature: &sig,
			}},
			want:   "[forwarded from Team]",
			absent: []string{"1009", "Jane"},
		},
		{
			name:   "unknown origin",
			origin: models.MessageOrigin{Type: "future"},
			want:   "[forwarded]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := privateMsg("look at this")
			m.ForwardOrigin = &tc.origin
			got := inboundContext(m)
			if got != tc.want {
				t.Errorf("header = %q, want %q", got, tc.want)
			}
			for _, s := range tc.absent {
				if strings.Contains(got, s) {
					t.Errorf("header %q leaks %q", got, s)
				}
			}
		})
	}
}

func TestInboundContext_TruncatesRunes(t *testing.T) {
	m := privateMsg("summarize")
	long := strings.Repeat("я", 600) // 2 bytes per rune
	m.ReplyToMessage = &models.Message{ID: 7, From: &models.User{ID: 5, FirstName: "Ann"}, Text: long}
	got := inboundContext(m)
	if !utf8.ValidString(got) {
		t.Fatalf("header is not valid UTF-8: %q", got)
	}
	body := strings.TrimSuffix(strings.TrimPrefix(got, `[in reply to Ann: "`), `"]`)
	if n := utf8.RuneCountInString(body); n != replyContextMax {
		t.Errorf("quoted text has %d runes, want %d (ellipsis included)", n, replyContextMax)
	}
	if !strings.HasSuffix(body, "…") {
		t.Errorf("truncated text does not end with an ellipsis: %q", body[len(body)-10:])
	}
	// Exactly at the limit: untouched.
	m.ReplyToMessage.Text = strings.Repeat("я", replyContextMax)
	if strings.Contains(inboundContext(m), "…") {
		t.Error("text at the limit was truncated")
	}
}

func TestInboundContext_ReplyToTopicRootIgnored(t *testing.T) {
	// A message in a topic without an explicit reply carries the
	// topic's service root as reply_to_message.
	m := privateMsg("hello topic")
	m.MessageThreadID = 30
	m.IsTopicMessage = true
	m.ReplyToMessage = &models.Message{
		ID:                30,
		From:              &models.User{ID: 42, FirstName: "Owner"},
		ForumTopicCreated: &models.ForumTopicCreated{Name: "Ideas"},
	}
	if got := inboundContext(m); got != "" {
		t.Errorf("topic root produced header %q", got)
	}
	// Same, if the root arrives without the service field.
	m.ReplyToMessage.ForumTopicCreated = nil
	if got := inboundContext(m); got != "" {
		t.Errorf("topic root by id produced header %q", got)
	}
	// A real reply inside the topic still counts.
	m.ReplyToMessage = &models.Message{ID: 31, From: &models.User{ID: 5, FirstName: "Ann"}, Text: "earlier"}
	if got, want := inboundContext(m), `[in reply to Ann: "earlier"]`; got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
}

func TestInboundContext_ReplyChainRootInGroupIsAReply(t *testing.T) {
	// In a non-forum group, message_thread_id names a reply chain's
	// root; replying to that root is a real reply.
	m := privateMsg("+1")
	m.Chat = models.Chat{ID: -5, Type: models.ChatTypeSupergroup}
	m.MessageThreadID = 30
	m.ReplyToMessage = &models.Message{ID: 30, From: &models.User{ID: 5, FirstName: "Ann"}, Text: "lunch?"}
	if got, want := inboundContext(m), `[in reply to Ann: "lunch?"]`; got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
}

func TestInboundContext_ForwardedReplyHasBoth(t *testing.T) {
	m := privateMsg("see")
	m.ForwardOrigin = &models.MessageOrigin{Type: models.MessageOriginTypeHiddenUser,
		MessageOriginHiddenUser: &models.MessageOriginHiddenUser{SenderUserName: "X"}}
	m.ReplyToMessage = &models.Message{ID: 7, From: &models.User{ID: 5, FirstName: "Ann"}, Text: "q"}
	want := "[forwarded from a hidden user]\n[in reply to Ann: \"q\"]"
	if got := inboundContext(m); got != want {
		t.Errorf("header = %q, want %q", got, want)
	}
}

func TestWithInboundContext(t *testing.T) {
	m := privateMsg("why?")
	if got := withInboundContext(m, "why?"); got != "why?" {
		t.Errorf("no context: %q", got)
	}
	m.ReplyToMessage = &models.Message{ID: 7, From: &models.User{ID: 5, FirstName: "Ann"}, Text: "x"}
	if got, want := withInboundContext(m, "why?"), "[in reply to Ann: \"x\"]\n\nwhy?"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
