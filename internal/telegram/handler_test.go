package telegram

import (
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestBuildChatJID(t *testing.T) {
	cases := map[int64]string{
		123:           "tg:123",
		-1001234:      "tg:-1001234",
		0:             "tg:0",
		1<<62 - 1:     "tg:4611686018427387903",
		-(1<<62 - 1): "tg:-4611686018427387903",
	}
	for in, want := range cases {
		if got := buildChatJID(in); got != want {
			t.Errorf("buildChatJID(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestIsGroupChat(t *testing.T) {
	cases := map[models.ChatType]bool{
		models.ChatTypePrivate:    false,
		models.ChatTypeGroup:      true,
		models.ChatTypeSupergroup: true,
		models.ChatTypeChannel:    true,
	}
	for in, want := range cases {
		if got := isGroupChat(in); got != want {
			t.Errorf("isGroupChat(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestChatDisplayName(t *testing.T) {
	cases := []struct {
		name string
		in   *models.Chat
		want string
	}{
		{"nil", nil, ""},
		{"title wins", &models.Chat{Title: "Group", FirstName: "X"}, "Group"},
		{"first+last", &models.Chat{FirstName: "Anton", LastName: "S"}, "Anton S"},
		{"first only", &models.Chat{FirstName: "Anton"}, "Anton"},
		{"username fallback", &models.Chat{Username: "anton"}, "@anton"},
		{"empty", &models.Chat{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := chatDisplayName(c.in); got != c.want {
				t.Errorf("chatDisplayName = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSenderID(t *testing.T) {
	if got := senderID(nil); got != "" {
		t.Errorf("senderID(nil) = %q, want empty", got)
	}
	if got := senderID(&models.User{ID: 42}); got != "42" {
		t.Errorf("senderID(42) = %q, want %q", got, "42")
	}
}

func TestSenderDisplayName(t *testing.T) {
	cases := []struct {
		name string
		in   *models.User
		want string
	}{
		{"nil", nil, ""},
		{"first+last", &models.User{FirstName: "A", LastName: "B"}, "A B"},
		{"username fallback", &models.User{ID: 42, Username: "anton"}, "@anton"},
		{"id fallback", &models.User{ID: 42}, "42"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := senderDisplayName(c.in); got != c.want {
				t.Errorf("senderDisplayName = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMessageText(t *testing.T) {
	if got := messageText(nil); got != "" {
		t.Errorf("messageText(nil) = %q, want empty", got)
	}
	if got := messageText(&models.Message{Text: "hi"}); got != "hi" {
		t.Errorf("messageText(text) = %q, want %q", got, "hi")
	}
	if got := messageText(&models.Message{Caption: "look"}); got != "look" {
		t.Errorf("messageText(caption) = %q, want %q", got, "look")
	}
	// Text wins over caption when both are set.
	if got := messageText(&models.Message{Text: "t", Caption: "c"}); got != "t" {
		t.Errorf("messageText(text+caption) = %q, want %q", got, "t")
	}
}

func TestNewRejectsEmptyToken(t *testing.T) {
	_, err := New("", Deps{})
	if err == nil {
		t.Fatal("New(\"\", Deps{}): want error")
	}
	if err != ErrEmptyToken {
		t.Errorf("err = %v, want ErrEmptyToken", err)
	}
}
