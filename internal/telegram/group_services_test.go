package telegram

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot/models"
)

func TestSharedSpeechNeverDownloadsOrCallsPersonalWhisper(t *testing.T) {
	for _, kind := range []string{"voice", "audio", "video_note", "album_audio"} {
		t.Run(kind, func(t *testing.T) {
			f := newInboundFixture(t, nil)
			whisper := newFakeWhisper(t, f.b, "must never become a group transcript")
			if err := f.b.store.RegisterChat(context.Background(), store.RegisteredChat{JID: "tg:-2002", Folder: "group", AddedAt: time.Now().UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			m := threadedMsg(7, "")
			m.Chat = models.Chat{ID: -2002, Type: models.ChatTypeSupergroup}
			switch kind {
			case "voice":
				m.Voice = &models.Voice{FileID: "voice"}
			case "audio":
				m.Audio = &models.Audio{FileID: "audio"}
			case "video_note":
				m.VideoNote = &models.VideoNote{FileID: "note"}
			case "album_audio":
				m.Audio = &models.Audio{FileID: "album"}
				m.MediaGroupID = "album"
			}
			f.handle(m)
			if len(whisper.uploaded()) != 0 {
				t.Fatal("group inherited personal Whisper")
			}
			if len(f.fake.callsTo("getFile")) != 0 {
				t.Fatal("group audio downloaded before service admission")
			}
			messages := f.fake.callsTo("sendMessage")
			if len(messages) != 1 || !strings.Contains(messages[0].Form["text"], "unavailable in shared chats") {
				t.Fatal("missing neutral service hint")
			}
			select {
			case <-f.items:
				t.Fatal("unsupported group speech queued as inference")
			default:
			}
		})
	}
}
func TestSharedSpeechRequiresTrustedPrivateContext(t *testing.T) {
	for _, chat := range []models.Chat{{ID: -1, Type: models.ChatTypeSupergroup}, {ID: streamChat, Type: models.ChatTypeGroup}, {ID: streamChat}, {ID: 0, Type: models.ChatTypePrivate}} {
		if !sharedSpeechInput(&models.Message{Chat: chat, Voice: &models.Voice{FileID: "v"}}) {
			t.Error("untrusted context inherited personal speech")
		}
	}
	if sharedSpeechInput(&models.Message{Chat: models.Chat{ID: streamChat, Type: models.ChatTypePrivate}, Voice: &models.Voice{FileID: "v"}}) {
		t.Fatal("private speech behavior changed")
	}
}

type speechServiceRoundTrip func(*http.Request) (*http.Response, error)

func (f speechServiceRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSharedReplyVoicePreferenceCannotInvokePersonalTTS(t *testing.T) {
	fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) { return sentMessage("555") }}
	b, _ := newStreamFixture(t, fake)
	old := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = old })
	calls := 0
	http.DefaultClient = &http.Client{Transport: speechServiceRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/v1/audio/speech" {
			t.Errorf("unexpected speech route %s", r.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("synthetic opus")), Header: make(http.Header)}, nil
	})}
	t.Setenv("OPENAI_API_KEY", "synthetic-personal-key")
	for _, id := range []int64{-2002, 0} {
		SetReplyMode(id, "voice")
		t.Cleanup(func() { SetReplyMode(id, "auto") })
		if b.getReplyMode(id, true) != "text" {
			t.Fatal("group voice-only preference suppresses text")
		}
		b.maybeVoiceReply(context.Background(), queue.Item{ChatID: id, IsVoice: true, IsOwner: true}, "group response")
	}
	if calls != 0 || len(fake.callsTo("sendVoice")) != 0 {
		t.Fatal("group used global TTS key")
	}
	// A positive control proves the personal path is still active under the same
	// configured key and fake transport; a broken TTS harness cannot claim denial.
	b.maybeVoiceReply(context.Background(), queue.Item{ChatID: streamChat, IsVoice: true}, "personal response")
	if calls != 1 || len(fake.callsTo("sendVoice")) != 1 {
		t.Fatal("personal voice behavior regressed")
	}
}

func TestSharedKnownAuthMaterialNeverPersistsOrQueues(t *testing.T) {
	for _, text := range []string{
		"sk-syntheticproviderkey0123456789",
		"https://auth.example/oauth/authorize?code=synthetic",
		"eyJhbGciOiJub25lIn0.eyJzdWIiOiJzeW50aGV0aWMifQ.c2lnbmF0dXJl",
		"/tasks create token=syntheticsecret",
	} {
		for _, caption := range []bool{false, true} {
			f := newInboundFixture(t, nil)
			m := threadedMsg(7, text)
			m.Chat = models.Chat{ID: -2002, Type: models.ChatTypeSupergroup}
			if caption {
				m.Text = ""
				m.Caption = text
				m.Document = &models.Document{FileID: "secret"}
			}
			f.handle(m)
			n, err := f.b.store.CountMessages(context.Background())
			if err != nil || n != 0 {
				t.Fatalf("known auth material persisted: %d %v", n, err)
			}
			select {
			case <-f.items:
				t.Fatal("known auth queued")
			default:
			}
			calls := f.fake.callsTo("sendMessage")
			if len(calls) != 1 || strings.Contains(calls[0].Form["text"], text) {
				t.Fatal("missing neutral rejection or echoed auth")
			}
			if len(f.fake.callsTo("getFile")) != 0 {
				t.Fatal("download before secret admission")
			}
		}
	}
}

func TestSharedOrdinaryTextAndPrivateBehaviorRemain(t *testing.T) {
	for _, tc := range []struct {
		chat models.Chat
		text string
	}{
		{models.Chat{ID: -2002, Type: models.ChatTypeSupergroup}, "ordinary public https://example.com/page"},
		{models.Chat{ID: streamChat, Type: models.ChatTypePrivate}, "sk-syntheticproviderkey0123456789"},
	} {
		f := newInboundFixture(t, nil)
		m := threadedMsg(7, tc.text)
		m.Chat = tc.chat
		f.handle(m)
		n, err := f.b.store.CountMessages(context.Background())
		if err != nil || n != 1 {
			t.Fatalf("existing message persistence changed: %d %v", n, err)
		}
	}
}

func TestSharedAuthMetadataRejectedBeforeHistory(t *testing.T) {
	secret := "https://auth.example/oauth/authorize?code=synthetic"
	cases := map[string]func(*models.Message){
		"reply_button": func(m *models.Message) {
			m.ReplyToMessage = &models.Message{ID: 1, Text: "ordinary", ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Open", URL: secret}}}}}
		},
		"reply_text":    func(m *models.Message) { m.ReplyToMessage = &models.Message{ID: 1, Text: secret} },
		"reply_caption": func(m *models.Message) { m.ReplyToMessage = &models.Message{ID: 1, Caption: secret} },
		"quote": func(m *models.Message) {
			m.ReplyToMessage = &models.Message{ID: 1, Text: "ordinary"}
			m.Quote = &models.TextQuote{Text: secret}
		},
		"truncated_reply": func(m *models.Message) {
			m.ReplyToMessage = &models.Message{ID: 1, Text: strings.Repeat("ordinary ", 100) + secret}
			m.Quote = &models.TextQuote{Text: "ordinary"}
		},
		"entity": func(m *models.Message) {
			m.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeTextLink, Offset: 0, Length: 7, URL: secret}}
		},
		"caption_entity": func(m *models.Message) {
			m.CaptionEntities = []models.MessageEntity{{Type: models.MessageEntityTypeTextLink, Offset: 0, Length: 7, URL: secret}}
		},
		"quote_entity": func(m *models.Message) {
			m.Quote = &models.TextQuote{Text: "ordinary", Entities: []models.MessageEntity{{Type: models.MessageEntityTypeTextLink, URL: secret}}}
		},
	}
	for name, decorate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newInboundFixture(t, nil)
			m := threadedMsg(7, "ordinary")
			m.Chat = models.Chat{ID: -2002, Type: models.ChatTypeSupergroup}
			decorate(m)
			f.handle(m)
			n, err := f.b.store.CountMessages(context.Background())
			if err != nil || n != 0 {
				t.Fatalf("sensitive metadata persisted: %d %v", n, err)
			}
			select {
			case <-f.items:
				t.Fatal("sensitive metadata queued")
			default:
			}
			calls := f.fake.callsTo("sendMessage")
			if len(calls) != 1 || strings.Contains(calls[0].Form["text"], secret) {
				t.Fatal("missing neutral rejection")
			}
			m.Chat = models.Chat{ID: streamChat, Type: models.ChatTypePrivate}
			if sharedSensitiveInput(m) {
				t.Fatal("private metadata behavior changed")
			}
		})
	}
	public := threadedMsg(7, "ordinary")
	public.Chat = models.Chat{ID: -2002, Type: models.ChatTypeSupergroup}
	public.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeTextLink, URL: "https://example.com/public"}}
	if sharedSensitiveInput(public) {
		t.Fatal("ordinary public metadata rejected")
	}
}
