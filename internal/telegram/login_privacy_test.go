package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot/models"
)

type privateLoginIO struct {
	lines      []string
	writeError error
	written    string
	closed     bool
}

func (e *privateLoginIO) ReadLine() (string, error) {
	if len(e.lines) == 0 {
		return "", io.EOF
	}
	line := e.lines[0]
	e.lines = e.lines[1:]
	return line, nil
}
func (e *privateLoginIO) WriteStdin(p []byte) (int, error) {
	e.written = string(p)
	return len(p), e.writeError
}
func (e *privateLoginIO) DrainAndClose() { e.closed = true }
func (e *privateLoginIO) Close() error   { e.closed = true; return nil }

func TestLoginNonPrivateEntryPointsFailClosed(t *testing.T) {
	for _, kind := range []models.ChatType{models.ChatTypeGroup, models.ChatTypeSupergroup, models.ChatTypeChannel, ""} {
		t.Run(string(kind), func(t *testing.T) {
			b, fake, _ := diagnosticFixture(t)
			calls := 0
			b.loginRun = func(context.Context, *models.Message, int, store.RegisteredChat) bool { calls++; return false }
			m := &models.Message{ID: 44, Chat: models.Chat{ID: -123, Type: kind}, From: &models.User{ID: 999}, Text: "/login"}
			b.loginHandler(t.Context(), b.api, &models.Update{Message: m})
			b.loginHandlerReal(t.Context(), b.api, &models.Update{Message: m})
			b.startLogin(t.Context(), m, 0)
			b.loginReserved(t.Context(), m, 0, store.RegisteredChat{})
			if b.runLogin(t.Context(), m, 0, store.RegisteredChat{}) {
				t.Fatal("group run started")
			}
			b.dispatchSlash(t.Context(), m, "/login@ExampleBot")
			b.defaultHandler(t.Context(), b.api, &models.Update{Message: m})
			toast, err := b.loginCallback(t.Context(), cbQuery{ChatID: -123, ChatKind: string(kind), UserID: 999})
			if err != nil || toast != loginPrivateToast || calls != 0 {
				t.Fatal(toast, err, calls)
			}
			if len(fake.callsTo("sendMessage")) != 0 || b.logins.get(-123) != nil || b.logins.isStarting(-123) {
				t.Fatal("nonprivate login emitted or reserved")
			}
			count, err := b.store.CountMessages(t.Context())
			if err != nil || count != 0 {
				t.Fatal("group login persisted", count, err)
			}
		})
	}
}
func TestOAuthCodeConsumedBeforePersistenceEvenWithoutRunner(t *testing.T) {
	for _, caption := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "media_caption"}[caption], func(t *testing.T) {
			f := newInboundFixture(t, nil)
			f.b.runner = nil
			const secret = "synthetic-OAUTH-secret#state"
			f.b.logins.set(streamChat, &loginSession{chatID: streamChat, private: true, awaitsCode: true, actorID: 42})
			var got string
			f.b.loginCode = func(_ context.Context, _ int64, s string) { got = s }
			m := threadedMsg(0, secret)
			m.ID = 777
			m.ReplyToMessage = &models.Message{Text: "https://example.invalid/login?code_challenge=synthetic-URL-secret"}
			if caption {
				m.Caption = m.Text
				m.Text = ""
				m.Document = &models.Document{FileID: "never_download"}
			}
			before, _ := f.b.store.CountMessages(t.Context())
			f.b.defaultHandler(t.Context(), f.b.api, &models.Update{Message: m})
			after, _ := f.b.store.CountMessages(t.Context())
			if got != secret || before != after || len(f.fake.callsTo("getFile")) != 0 {
				t.Fatal("code leaked to persistence/media", got, before, after)
			}
			f.none(t)
		})
	}
}
func TestOAuthWrongActorAndStaleGroupNeverPersistOrRoute(t *testing.T) {
	for _, group := range []bool{false, true} {
		t.Run(map[bool]string{false: "wrong_actor", true: "stale_group"}[group], func(t *testing.T) {
			f := newInboundFixture(t, nil)
			m := threadedMsg(0, "synthetic-OAUTH-secret")
			if group {
				m.Chat.ID = -123
				m.Chat.Type = models.ChatTypeGroup
			} else {
				m.From.ID = 999
			}
			cancelled := false
			f.b.logins.set(m.Chat.ID, &loginSession{chatID: m.Chat.ID, private: !group, awaitsCode: true, actorID: 42, cancel: func() { cancelled = true }})
			f.b.loginCode = func(context.Context, int64, string) { t.Fatal("wrong login input accepted") }
			f.b.defaultHandler(t.Context(), f.b.api, &models.Update{Message: m})
			count, _ := f.b.store.CountMessages(t.Context())
			if count != 0 || len(f.fake.callsTo("sendMessage")) != 0 {
				t.Fatal("secret persisted or emitted")
			}
			if group && (!cancelled || f.b.logins.get(m.Chat.ID) != nil) {
				t.Fatal("stale group session retained")
			}
			f.none(t)
		})
	}
}
func TestLoginCompletionNeverUsesRawResultAsSuccessOrOutput(t *testing.T) {
	for _, line := range []string{"Not logged in synthetic-secret", "Successfully logged in synthetic-secret https://secret.invalid/", "error token=synthetic-secret"} {
		b, fake, _ := diagnosticFixture(t)
		var logs bytes.Buffer
		b.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		exec := &privateLoginIO{lines: []string{line}}
		session := &loginSession{chatID: streamChat, private: true, exec: exec, ctx: t.Context()}
		b.logins.set(streamChat, session)
		checks := 0
		b.loginAuthCheck = func(context.Context, *loginSession) bool { checks++; return false }
		b.finishLogin(t.Context(), streamChat, session)
		if checks != 1 || !exec.closed {
			t.Fatal("completion skipped authoritative check")
		}
		for _, send := range fake.callsTo("sendMessage") {
			if strings.Contains(send.Form["text"], "synthetic-secret") || strings.Contains(send.Form["text"], "Authentication verified.") {
				t.Fatal("rawresult trusted or exposed")
			}
		}
		if strings.Contains(logs.String(), "synthetic-secret") {
			t.Fatal("rawresult logged")
		}
	}
}
func TestLoginWriteAndAuthErrorsNeverLeakSecrets(t *testing.T) {
	b, fake, _ := diagnosticFixture(t)
	var logs bytes.Buffer
	b.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	secret := errors.New("https://secret.invalid?token=synthetic-secret")
	session := &loginSession{chatID: streamChat, private: true, exec: &privateLoginIO{writeError: secret}}
	b.logins.set(streamChat, session)
	b.handleLoginCode(t.Context(), streamChat, "synthetic-code")
	b.loginFailed(t.Context(), streamChat, 0, "owner", "ensure", secret)
	_, item := newStreamFixture(t, fake)
	item.ChatID = -123
	b.replyError(t.Context(), item, errors.Join(secret, runner.ErrNotLoggedIn))
	if strings.Contains(logs.String(), "synthetic-secret") {
		t.Fatal("auth errors logged raw")
	}
	for _, send := range fake.callsTo("sendMessage") {
		if strings.Contains(send.Form["text"], "synthetic-secret") || strings.Contains(send.Form["reply_markup"], "lg") {
			t.Fatal("secret or group login button emitted")
		}
	}
}

func TestRegisteredLoginHandlerNeverPersistsQuotedCredential(t *testing.T) {
	for _, withRunner := range []bool{false, true} {
		t.Run(fmt.Sprint(withRunner), func(t *testing.T) {
			b, fake, _ := diagnosticFixture(t)
			called := make(chan struct{}, 1)
			if withRunner {
				b.runner = &runner.Runner{}
				b.loginRun = func(context.Context, *models.Message, int, store.RegisteredChat) bool {
					called <- struct{}{}
					return false
				}
			}
			b.registerHandlers(b.api)
			message := threadedMsg(0, "/login")
			message.ID = 800
			message.ReplyToMessage = &models.Message{Text: "https://login.invalid/secret-code_challenge-synthetic-secret"}
			message.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: 6}}
			b.api.ProcessUpdate(t.Context(), &models.Update{Message: message})
			if withRunner {
				select {
				case <-called:
				case <-time.After(time.Second):
					t.Fatal("registered login handler not called")
				}
			} else {
				waitFor(t, "private no-runner response", func() bool { return len(fake.callsTo("sendMessage")) == 1 })
			}
			count, err := b.store.CountMessages(t.Context())
			if err != nil || count != 0 {
				t.Fatal("quoted credential persisted", count, err)
			}
			for _, send := range fake.callsTo("sendMessage") {
				if strings.Contains(send.Form["text"], "synthetic-secret") {
					t.Fatal("quoted credential emitted")
				}
			}
		})
	}
}
func TestActiveDeviceFlowKeepsOrdinaryTextButStripsQuotedLoginSecret(t *testing.T) {
	f := newInboundFixture(t, nil)
	f.b.logins.set(streamChat, &loginSession{chatID: streamChat, private: true, awaitsCode: false})
	message := threadedMsg(0, "hello")
	message.ID = 888
	message.ReplyToMessage = &models.Message{Text: "https://secret.invalid/DEVICE-CODE", From: &models.User{IsBot: true}}
	message.Quote = &models.TextQuote{Text: "DEVICE-CODE"}
	f.b.defaultHandler(t.Context(), f.b.api, &models.Update{Message: message})
	item := f.next(t)
	if strings.Contains(item.Text, "DEVICE-CODE") || strings.Contains(item.Text, "secret.invalid") || item.Text != "hello" {
		t.Fatal("credential quote routed to model", item.Text)
	}
	count, _ := f.b.store.CountMessages(t.Context())
	if count != 0 {
		t.Fatal("active flow metadata persisted")
	}
}

func TestKnownDeviceCodePasteIsNotQueuedOrPersisted(t *testing.T) {
	f := newInboundFixture(t, nil)
	f.b.logins.set(streamChat, &loginSession{chatID: streamChat, private: true, awaitsCode: false, deviceCode: "SYNTHETIC-1234", loginURL: "https://device.invalid/synthetic-secret"})
	m := threadedMsg(0, "SYNTHETIC-1234")
	m.ID = 901
	f.b.defaultHandler(t.Context(), f.b.api, &models.Update{Message: m})
	count, _ := f.b.store.CountMessages(t.Context())
	if count != 0 {
		t.Fatal("device code persisted")
	}
	for _, send := range f.fake.callsTo("sendMessage") {
		if strings.Contains(send.Form["text"], "SYNTHETIC-1234") {
			t.Fatal("device code echoed")
		}
	}
	f.none(t)
}
