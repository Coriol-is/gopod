package telegram

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot/models"
)

const handoffGroup int64 = -1003850302017

func handoffFixture(t *testing.T) (*Bot, *fakeTelegram) {
	t.Helper()
	b, fake, _ := diagnosticFixture(t)
	b.registerCallbacks()
	b.registerLoginHandoff()
	if _, ok := b.resolveRegistered(t.Context(), threadedMsg(0, "")); !ok {
		t.Fatal("owner registration")
	}
	if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(handoffGroup), Folder: "chat-3850302017", AddedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	return b, fake
}
func ownerGroupLogin() *models.Message {
	return &models.Message{Chat: models.Chat{ID: handoffGroup, Type: models.ChatTypeSupergroup}, From: &models.User{ID: streamChat}, Text: "/login"}
}
func handoffToken(b *Bot) string {
	b.loginHandoffMu.Lock()
	defer b.loginHandoffMu.Unlock()
	for key := range b.loginHandoffs {
		return key
	}
	return ""
}
func handoffCommand(token string) control.Command {
	return control.Command{Name: groupLoginConfirm, Args: []string{token}, Confirmed: true, Caller: control.Caller{ChatID: streamChat, UserID: streamChat, IsOwner: true, Source: control.SourceTelegram, ChatKind: "private"}}
}
func TestGroupLoginRequiresPrivateOwnerConfirmationAndKeepsTarget(t *testing.T) {
	b, fake := handoffFixture(t)
	type result struct {
		m  *models.Message
		rc store.RegisteredChat
	}
	started := make(chan result, 1)
	b.loginRun = func(ctx context.Context, m *models.Message, _ int, rc store.RegisteredChat) bool {
		started <- result{m, rc}
		b.replyLoginText(ctx, m.Chat.ID, 0, "https://example.invalid/PRIVATE-AUTH-URL")
		return false
	}
	b.loginHandler(t.Context(), b.api, &models.Update{Message: ownerGroupLogin()})
	select {
	case <-started:
		t.Fatal("CLI before private confirmation")
	default:
	}
	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 || sends[0].Form["chat_id"] != fmt.Sprint(streamChat) || strings.Contains(sends[0].Form["text"], "PRIVATE-AUTH-URL") {
		t.Fatal("wrong initial handoff", sends)
	}
	if !strings.Contains(sends[0].Form["text"], fmt.Sprint(handoffGroup)) {
		t.Fatal("target group not named")
	}
	yes := buttonsOf(t, sends[0])[0]
	update := cbUpdate("yes", streamChat, 0, 555, yes.CallbackData)
	update.CallbackQuery.From.ID = streamChat
	b.callbackHandler(t.Context(), b.api, update)
	select {
	case got := <-started:
		if got.m.Chat.ID != streamChat || got.m.Chat.Type != models.ChatTypePrivate || got.rc.Folder != "chat-3850302017" || got.rc.IsOwner || got.rc.JID != buildChatJID(handoffGroup) {
			t.Fatal("display/target scope mixed", got)
		}
	case <-time.After(time.Second):
		t.Fatal("confirmed login not started")
	}
	waitFor(t, "private auth URL", func() bool { return len(fake.callsTo("sendMessage")) == 2 })
	for _, send := range fake.callsTo("sendMessage") {
		if send.Form["chat_id"] != fmt.Sprint(streamChat) {
			t.Fatal("group output", send)
		}
	}
	b.callbackHandler(t.Context(), b.api, update)
	select {
	case <-started:
		t.Fatal("double tap restarted login")
	default:
	}
	if handoffToken(b) != "" {
		t.Fatal("consumed token remains")
	}
	for _, entry := range b.router.List() {
		if entry.Name == groupLoginConfirm && entry.SlashName != "" {
			t.Fatal("hidden confirmation published")
		}
	}
}
func TestGroupLoginHandoffRejectsWrongActorExpiredAndRebound(t *testing.T) {
	for _, mode := range []string{"wrong_actor", "group_context", "cli", "not_confirmed", "expired", "rebound", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			b, _ := handoffFixture(t)
			b.loginRun = func(context.Context, *models.Message, int, store.RegisteredChat) bool {
				t.Error("forbidden handoff started")
				return false
			}
			now := time.Now()
			b.now = func() time.Time { return now }
			if !b.requestLoginHandoff(t.Context(), ownerGroupLogin()) {
				t.Fatal("request failed")
			}
			command := handoffCommand(handoffToken(b))
			switch mode {
			case "wrong_actor":
				command.Caller.UserID = 99
			case "group_context":
				command.Caller.ChatKind = "supergroup"
			case "cli":
				command.Caller.Source = control.SourceCLI
			case "not_confirmed":
				command.Confirmed = false
			case "expired":
				now = now.Add(6 * time.Minute)
			case "rebound":
				registered, err := b.store.GetRegistered(t.Context(), buildChatJID(handoffGroup))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = b.store.RotateGroupEpoch(t.Context(), registered.JID, registered.RegistrationRevision); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				command.Args = append(command.Args, "cancel")
			}
			response, err := b.router.Dispatch(t.Context(), command)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(response.Text, "Starting group authentication") {
				t.Fatal("invalid handoff authorized")
			}
			if b.logins.isStarting(streamChat) {
				t.Fatal("forbidden reservation")
			}
		})
	}
}
func TestGroupHandoffFailureRevokesAndNeverFallsBackPublicly(t *testing.T) {
	b, fake := handoffFixture(t)
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			return http.StatusForbidden, `{"ok":false,"error_code":403,"description":"synthetic-secret"}`
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	if b.requestLoginHandoff(t.Context(), ownerGroupLogin()) {
		t.Fatal("failed delivery retained handoff")
	}
	if handoffToken(b) != "" {
		t.Fatal("failed token retained")
	}
	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 || sends[0].Form["chat_id"] != fmt.Sprint(streamChat) {
		t.Fatal("fallback or public send", sends)
	}
}
func TestGroupHandoffBoundedAndRateLimited(t *testing.T) {
	b, _ := handoffFixture(t)
	if !b.requestLoginHandoff(t.Context(), ownerGroupLogin()) {
		t.Fatal("request failed")
	}
	if b.requestLoginHandoff(t.Context(), ownerGroupLogin()) {
		t.Fatal("rate limit bypass")
	}
	for i := 0; i < groupLoginCap+4; i++ {
		m := ownerGroupLogin()
		m.Chat.ID = handoffGroup - int64(i+1)
		if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(m.Chat.ID), Folder: fmt.Sprintf("group-%d", i), AddedAt: 1}); err != nil {
			t.Fatal(err)
		}
		b.requestLoginHandoff(t.Context(), m)
	}
	b.loginHandoffMu.Lock()
	defer b.loginHandoffMu.Unlock()
	if len(b.loginHandoffs) > groupLoginCap || len(b.loginHandoffRecent) > groupLoginCap {
		t.Fatal("unbounded handoff maps")
	}
}
func TestConcurrentGroupHandoffConfirmsOneLogin(t *testing.T) {
	b, _ := handoffFixture(t)
	calls := make(chan struct{}, 2)
	b.loginRun = func(context.Context, *models.Message, int, store.RegisteredChat) bool {
		calls <- struct{}{}
		return false
	}
	if !b.requestLoginHandoff(t.Context(), ownerGroupLogin()) {
		t.Fatal("request failed")
	}
	command := handoffCommand(handoffToken(b))
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); b.router.Dispatch(t.Context(), command) }()
	}
	wg.Wait()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	select {
	case <-calls:
		t.Fatal("double start")
	default:
	}
}
