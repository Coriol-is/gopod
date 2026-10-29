package telegram

import (
	"fmt"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/store"
)

const (
	trigBotID   int64 = 123
	trigBotName       = "ExampleBot"
	trigOther   int64 = 555
	trigGroup   int64 = -1003850302017
)

func triggerFixture(t *testing.T, mode store.TriggerMode) *inboundFixture {
	t.Helper()
	f := newInboundFixture(t, nil)
	f.b.botID = trigBotID
	f.b.capabilities = newCapabilityCache(nil, time.Now)
	f.b.capabilities.view.Username = trigBotName
	if err := f.b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(trigGroup), Folder: "chat-3850302017", AddedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := f.b.store.SetGroupTrigger(t.Context(), buildChatJID(trigGroup), mode, 1); err != nil {
		t.Fatal(err)
	}
	return f
}

var msgSeq = 1000

func groupMsg(from int64, text string) *models.Message {
	msgSeq++
	return &models.Message{ID: msgSeq, Date: int(time.Now().Unix()), Chat: models.Chat{ID: trigGroup, Type: models.ChatTypeSupergroup}, From: &models.User{ID: from, FirstName: "Anton"}, Text: text}
}

type shape struct {
	name  string
	build func(from int64) *models.Message
	addr  bool
}

func shapes() []shape {
	return []shape{
		{"plain", func(from int64) *models.Message { return groupMsg(from, "hello there") }, false},
		{"mention", func(from int64) *models.Message {
			m := groupMsg(from, "hi @"+trigBotName+" please")
			m.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeMention, Offset: 3, Length: len(trigBotName) + 1}}
			return m
		}, true},
		{"mention-other-bot", func(from int64) *models.Message {
			m := groupMsg(from, "hi @OtherBot please")
			m.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeMention, Offset: 3, Length: 9}}
			return m
		}, false},
		{"text-mention", func(from int64) *models.Message {
			m := groupMsg(from, "hi bot")
			m.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeTextMention, Offset: 3, Length: 3, User: &models.User{ID: trigBotID, IsBot: true}}}
			return m
		}, true},
		{"text-mention-other", func(from int64) *models.Message {
			m := groupMsg(from, "hi bob")
			m.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeTextMention, Offset: 3, Length: 3, User: &models.User{ID: 999}}}
			return m
		}, false},
		{"reply-to-bot", func(from int64) *models.Message {
			m := groupMsg(from, "and then?")
			m.ReplyToMessage = &models.Message{ID: 7, From: &models.User{ID: trigBotID, IsBot: true}}
			return m
		}, true},
		{"reply-to-human", func(from int64) *models.Message {
			m := groupMsg(from, "and then?")
			m.ReplyToMessage = &models.Message{ID: 7, From: &models.User{ID: 999}}
			return m
		}, false},
		{"caption-mention", func(from int64) *models.Message {
			m := groupMsg(from, "")
			m.Caption = "look @" + trigBotName
			m.CaptionEntities = []models.MessageEntity{{Type: models.MessageEntityTypeMention, Offset: 5, Length: len(trigBotName) + 1}}
			m.Photo = []models.PhotoSize{{FileID: "p"}}
			return m
		}, true},
		{"mention-after-emoji", func(from int64) *models.Message {
			// "😀" is two UTF-16 code units: offsets count code units, not bytes or runes.
			m := groupMsg(from, "😀 @"+trigBotName)
			m.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeMention, Offset: 3, Length: len(trigBotName) + 1}}
			return m
		}, true},
		{"forwarded-mention", func(from int64) *models.Message {
			m := groupMsg(from, "hi @"+trigBotName)
			m.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeMention, Offset: 3, Length: len(trigBotName) + 1}}
			m.ForwardOrigin = forwardedOrigin()
			return m
		}, false},
	}
}

func wantAnswered(mode store.TriggerMode, owner, addressed bool) bool {
	switch mode {
	case store.TriggerOwner:
		return owner
	case store.TriggerMention:
		return addressed
	case store.TriggerOwnerMention:
		return owner || addressed
	}
	return true
}

func TestGroupTriggerMatrix(t *testing.T) {
	for _, mode := range []store.TriggerMode{store.TriggerOwner, store.TriggerMention, store.TriggerOwnerMention, store.TriggerAll} {
		for _, s := range shapes() {
			for _, who := range []struct {
				name string
				id   int64
			}{{"owner", streamChat}, {"member", trigOther}} {
				name := fmt.Sprintf("%s/%s/%s", mode, s.name, who.name)
				t.Run(name, func(t *testing.T) {
					f := triggerFixture(t, mode)
					m := s.build(who.id)
					// A forwarded message is never the owner's, whoever forwarded it.
					want := wantAnswered(mode, who.id == streamChat && s.name != "forwarded-mention", s.addr)
					if s.name == "caption-mention" {
						// media in a group needs a download; the gate runs before it.
						if got := f.b.groupTriggerSkips(t.Context(), m); got == want {
							t.Fatalf("skips=%v want answered=%v", got, want)
						}
						return
					}
					f.handle(m)
					if want {
						if it := f.next(t); it.ChatID != trigGroup || it.IsOwner {
							t.Fatalf("queued %+v", it)
						}
					} else {
						f.none(t)
					}
					if !want && apiCallCount(f.fake) != 0 {
						t.Fatalf("telegram traffic for an ignored message: %d calls", apiCallCount(f.fake))
					}
				})
			}
		}
	}
}

func TestNonMatchingGroupMessageIsStoredWithoutTurnOrNotice(t *testing.T) {
	f := triggerFixture(t, store.TriggerMention)
	before, _ := f.b.store.CountMessages(t.Context())
	f.handle(groupMsg(trigOther, "just chatting"))
	f.none(t)
	after, _ := f.b.store.CountMessages(t.Context())
	if after != before+1 {
		t.Fatalf("messages %d -> %d: context not stored", before, after)
	}
	if n := apiCallCount(f.fake); n != 0 {
		t.Fatalf("telegram traffic for an ignored message: %d calls", n)
	}
}

func TestOwnerIdentitySpoofsAreNotTheOwnerInOwnerMode(t *testing.T) {
	spoofs := map[string]func(m *models.Message){
		"sender_chat (anonymous admin)": func(m *models.Message) {
			m.From = &models.User{ID: 1087968824, IsBot: true, FirstName: "Group"}
			m.SenderChat = &models.Chat{ID: trigGroup, Type: models.ChatTypeSupergroup}
		},
		"owner id with sender_chat": func(m *models.Message) { m.SenderChat = &models.Chat{ID: -1009, Type: models.ChatTypeChannel} },
		"forwarded from owner":      func(m *models.Message) { m.ForwardOrigin = forwardedOrigin() },
		"same display name": func(m *models.Message) {
			m.From = &models.User{ID: trigOther, FirstName: "Anton", Username: "owner"}
		},
		"bot with owner id": func(m *models.Message) { m.From.IsBot = true },
		"no sender":         func(m *models.Message) { m.From = nil },
	}
	for name, mutate := range spoofs {
		t.Run(name, func(t *testing.T) {
			f := triggerFixture(t, store.TriggerOwner)
			m := groupMsg(streamChat, "do things")
			mutate(m)
			if f.b.isGroupOwner(m) {
				t.Fatal("treated as owner")
			}
			f.handle(m)
			f.none(t)
		})
	}
	f := triggerFixture(t, store.TriggerOwner)
	if !f.b.isGroupOwner(groupMsg(streamChat, "x")) {
		t.Fatal("genuine owner message rejected")
	}
}

func TestTriggerGateDoesNotTouchPrivateOrUnregisteredChats(t *testing.T) {
	f := triggerFixture(t, store.TriggerOwner)
	// Owner private chat: auto-registered and answered whatever any group says.
	private := &models.Message{ID: 1, Chat: models.Chat{ID: streamChat, Type: models.ChatTypePrivate}, From: &models.User{ID: streamChat}, Text: "hello"}
	f.handle(private)
	if it := f.next(t); it.ChatID != streamChat || !it.IsOwner {
		t.Fatalf("private owner turn: %+v", it)
	}
	// Unregistered group: not gated here (and answered by nobody).
	other := &models.Message{ID: 2, Chat: models.Chat{ID: -42, Type: models.ChatTypeSupergroup}, From: &models.User{ID: trigOther}, Text: "hi @" + trigBotName}
	if f.b.groupTriggerSkips(t.Context(), other) {
		t.Fatal("unregistered group gated")
	}
	f.handle(other)
	f.none(t)
	// Commands in a group are never gated: they are not turns.
	if f.b.groupTriggerSkips(t.Context(), groupMsg(trigOther, "/ping")) {
		t.Fatal("command gated")
	}
	if f.b.groupTriggerSkips(t.Context(), groupMsg(trigOther, "/ping@"+trigBotName)) {
		t.Fatal("addressed command gated")
	}
}

func TestBotCommandEntityAddressedToBotCountsAsMention(t *testing.T) {
	f := triggerFixture(t, store.TriggerMention)
	m := groupMsg(trigOther, "")
	m.Caption = "/describe@" + trigBotName
	m.CaptionEntities = []models.MessageEntity{{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: len(m.Caption)}}
	m.Photo = []models.PhotoSize{{FileID: "p"}}
	// A command caption is a command, not a turn, so the gate lets it through;
	// the entity logic itself must still recognise the address.
	if !f.b.addressesBot(m) {
		t.Fatal("command addressed to the bot not recognised")
	}
	m.CaptionEntities[0].Length = len("/describe@OtherBot")
	m.Caption = "/describe@OtherBot"
	if f.b.addressesBot(m) {
		t.Fatal("command to another bot recognised")
	}
}

func apiCallCount(fake *fakeTelegram) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.calls)
}

func forwardedOrigin() *models.MessageOrigin {
	return &models.MessageOrigin{Type: models.MessageOriginTypeUser, MessageOriginUser: &models.MessageOriginUser{SenderUser: models.User{ID: streamChat}}}
}
