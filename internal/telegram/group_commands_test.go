package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/control"
)

type isoLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *isoLog) rec(op string) func(context.Context, string) (string, error) {
	return func(_ context.Context, target string) (string, error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.calls = append(l.calls, op+" "+target)
		return op + " done", nil
	}
}

func (l *isoLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

func groupCommandFixture(t *testing.T) (*Bot, *fakeTelegram, *isoLog, *[]string) {
	t.Helper()
	b, fake := handoffFixture(t)
	log := &isoLog{}
	control.RegisterIsolationCommands(b.router, control.IsolationActions{Status: log.rec("status"), Prepare: log.rec("prepare"), Enable: log.rec("enable"), Disable: log.rec("disable")})
	var switched []string
	lookup := func(id int64) string {
		if id == handoffGroup {
			return "chat-3850302017"
		}
		return "owner"
	}
	control.RegisterProviderCommand(b.router, func(_ context.Context, folder, name string) (string, error) {
		switched = append(switched, folder+"="+name)
		return "switched " + name, nil
	}, func(string) string { return "claude" }, []string{"claude", "codex"}, lookup)
	return b, fake, log, &switched
}

func groupCommand(from int64, text string) *models.Message {
	m := groupMsg(from, text)
	m.Chat.ID = handoffGroup
	return m
}

func TestGroupOwnerIsolationEnableIsConfirmedInPrivateChat(t *testing.T) {
	for _, op := range []string{"enable", "prepare"} {
		t.Run(op, func(t *testing.T) {
			b, fake, log, _ := groupCommandFixture(t)
			b.dispatchSlash(t.Context(), groupCommand(streamChat, "/isolation "+op), "/isolation "+op)
			if len(log.get()) != 0 {
				t.Fatal("ran before confirmation")
			}
			private, group := sendsTo(fake, streamChat), sendsTo(fake, handoffGroup)
			if len(private) != 1 || !strings.Contains(private[0].Form["text"], fmt.Sprint(handoffGroup)) {
				t.Fatalf("private confirmation missing: %+v", private)
			}
			if len(group) != 1 || strings.Contains(group[0].Form["reply_markup"], "callback_data") || !strings.Contains(group[0].Form["text"], "private chat") {
				t.Fatalf("group must get a plain acknowledgement: %+v", group)
			}
			var yes models.InlineKeyboardButton
			for _, btn := range buttonsOf(t, private[0]) {
				if strings.HasPrefix(btn.CallbackData, "cf:") {
					yes = btn
				}
			}
			if yes.CallbackData == "" {
				t.Fatal("no Yes button")
			}
			// The same token pressed in the group is refused: it is bound to the private chat.
			inGroup := cbUpdate("g", handoffGroup, 0, 9, yes.CallbackData)
			inGroup.CallbackQuery.From.ID = streamChat
			inGroup.CallbackQuery.Message.Message.Chat.Type = models.ChatTypeSupergroup
			b.callbackHandler(t.Context(), b.api, inGroup)
			if len(log.get()) != 0 {
				t.Fatal("confirmed from the group")
			}
			update := cbUpdate("p", streamChat, 0, 10, yes.CallbackData)
			update.CallbackQuery.From.ID = streamChat
			b.callbackHandler(t.Context(), b.api, update)
			if got := log.get(); len(got) != 1 || got[0] != fmt.Sprintf("%s tg:%d", op, handoffGroup) {
				t.Fatalf("calls = %v", got)
			}
		})
	}
}

func TestGroupOwnerIsolationStatusAndDisableAreImmediate(t *testing.T) {
	b, fake, log, _ := groupCommandFixture(t)
	for _, text := range []string{"/isolation status", "/isolation disable"} {
		b.dispatchSlash(t.Context(), groupCommand(streamChat, text), text)
	}
	want := fmt.Sprintf("status tg:%d,disable tg:%d", handoffGroup, handoffGroup)
	if strings.Join(log.get(), ",") != want {
		t.Fatalf("calls = %v", log.get())
	}
	if len(sendsTo(fake, streamChat)) != 0 {
		t.Fatal("immediate commands must not message the private chat")
	}
}

func TestGroupIsolationDeniedToSpoofedOwners(t *testing.T) {
	spoofs := map[string]func(*models.Message){
		"member": func(m *models.Message) { m.From = &models.User{ID: 555, FirstName: "Anton"} },
		"sender_chat": func(m *models.Message) {
			m.SenderChat = &models.Chat{ID: handoffGroup, Type: models.ChatTypeSupergroup}
		},
		"forwarded":    func(m *models.Message) { m.ForwardOrigin = forwardedOrigin() },
		"bot":          func(m *models.Message) { m.From.IsBot = true },
		"anonymous":    func(m *models.Message) { m.From = &models.User{ID: 1087968824, IsBot: true}; m.SenderChat = &m.Chat },
		"channel type": func(m *models.Message) { m.Chat.Type = models.ChatTypeChannel },
	}
	for name, mutate := range spoofs {
		t.Run(name, func(t *testing.T) {
			b, fake, log, _ := groupCommandFixture(t)
			m := groupCommand(streamChat, "/isolation disable")
			mutate(m)
			b.dispatchSlash(t.Context(), m, "/isolation disable")
			if len(log.get()) != 0 {
				t.Fatal("isolation ran")
			}
			if len(sendsTo(fake, streamChat)) != 0 {
				t.Fatal("private chat notified for a denied command")
			}
		})
	}
}

func TestGroupOwnerProviderSwitchIsImmediateAndScopedToTheGroup(t *testing.T) {
	b, _, _, switched := groupCommandFixture(t)
	b.dispatchSlash(t.Context(), groupCommand(streamChat, "/provider codex"), "/provider codex")
	if len(*switched) != 1 || (*switched)[0] != "chat-3850302017=codex" {
		t.Fatalf("switched = %v", *switched)
	}
	b.dispatchSlash(t.Context(), groupCommand(555, "/provider claude"), "/provider claude")
	if len(*switched) != 1 {
		t.Fatal("a member switched the provider")
	}
}

func TestGroupOwnerCannotRunHostCommandsFromGroup(t *testing.T) {
	b, fake, _, _ := groupCommandFixture(t)
	b.router.Register("logs", "logs", "(owner) logs", control.PermOwnerOnly, func(context.Context, control.Command) (control.Response, error) {
		t.Error("logs ran from a group")
		return control.Response{}, nil
	})
	b.dispatchSlash(t.Context(), groupCommand(streamChat, "/logs"), "/logs")
	if sends := sendsTo(fake, handoffGroup); len(sends) != 1 || !strings.Contains(sends[0].Form["text"], "Not authorized") {
		t.Fatalf("expected a denial: %+v", sends)
	}
}
