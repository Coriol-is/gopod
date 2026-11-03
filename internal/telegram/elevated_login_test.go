package telegram

import (
	"context"
	"errors"
	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/go-telegram/bot/models"
	"strings"
	"testing"
)

type elevatedVerifier struct{ valid bool }

func (v elevatedVerifier) Verified(context.Context) bool { return v.valid }

func TestElevatedLoginCompletionUsesDedicatedVerification(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "verified", false: "unverified"}[valid], func(t *testing.T) {
			f := newInboundFixture(t, nil)
			s := &loginSession{chatID: streamChat, private: true, provider: "codex", ctx: t.Context(), elevated: elevatedVerifier{valid}, exec: &privateLoginIO{lines: strings.Split(strings.Repeat("success synthetic-secret\n", 80), "\n")}}
			f.b.logins.set(streamChat, s)
			f.b.loginAuthCheck = func(context.Context, *loginSession) bool { t.Fatal("ordinary auth queried"); return true }
			f.b.finishLogin(t.Context(), streamChat, s)
			if len(s.exec.(*privateLoginIO).lines) != 0 {
				t.Fatal("elevated completion output not drained")
			}
			calls := f.fake.callsTo("sendMessage")
			if len(calls) != 1 {
				t.Fatal(len(calls))
			}
			text := calls[0].Form["text"]
			if strings.Contains(text, "synthetic-secret") || !strings.Contains(text, "ROOT") {
				t.Fatal(text)
			}
			if valid != strings.Contains(text, "вход в отдельный elevated-профиль подтверждён") {
				t.Fatal(text)
			}
		})
	}
}

func TestLoginElevatedAliasDispatchesThroughRouter(t *testing.T) {
	f := newInboundFixture(t, nil)
	router := control.New(nil)
	calls := 0
	router.Register("root", "root", "", control.PermOwnerOnly, func(_ context.Context, c control.Command) (control.Response, error) {
		calls++
		if len(c.Args) != 1 || c.Args[0] != "login" {
			t.Fatal(c.Args)
		}
		return control.Response{Text: "ROOT login"}, nil
	})
	f.b.router = router
	f.b.ownerChatID = streamChat
	m := threadedMsg(0, "/login elevated")
	m.From = &models.User{ID: streamChat}
	f.b.loginHandler(t.Context(), f.b.api, &models.Update{Message: m})
	if calls != 1 {
		t.Fatal(calls)
	}
	m.Chat.ID = 555
	m.From.ID = 555
	f.b.loginHandler(t.Context(), f.b.api, &models.Update{Message: m})
	if calls != 1 {
		t.Fatal("non-owner bypassed router")
	}
}

func TestElevatedOAuthStopNeverFallsThroughToOrdinary(t *testing.T) {
	b, _, _ := diagnosticFixture(t)
	p := &rootInputProbe{active: true}
	b.elevated = p
	b.logins = newLoginSessions()
	b.logins.set(b.ownerChatID, &loginSession{chatID: b.ownerChatID, private: true, actorID: b.ownerChatID, awaitsCode: true, elevated: elevatedVerifier{}})
	m := &models.Message{ID: 20, Text: "/stop", Chat: models.Chat{ID: b.ownerChatID, Type: models.ChatTypePrivate}, From: &models.User{ID: b.ownerChatID}, ReplyToMessage: &models.Message{Text: "https://login.invalid/?code=synthetic-secret"}}
	b.defaultHandler(t.Context(), nil, &models.Update{Message: m})
	if p.stops != 1 || p.calls != 0 {
		t.Fatal("wrong stop target", p.stops, p.calls)
	}
}

func TestElevatedCancelledLoginNeverPublishesBufferedURL(t *testing.T) {
	f := newInboundFixture(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m := threadedMsg(0, "/root login")
	s := &loginSession{chatID: streamChat, private: true, actorID: streamChat, ctx: ctx, cancel: func() {}, elevated: elevatedVerifier{}, exec: &privateLoginIO{lines: []string{"https://auth.openai.com/codex/device", "ABCD-EFGHI"}}}
	if f.b.publishLogin(t.Context(), m, s, runner.NewCodexProvider("test")) {
		t.Fatal("cancelled login stored")
	}
	if len(f.fake.callsTo("sendMessage")) != 0 || f.b.logins.get(streamChat) != nil {
		t.Fatal("cancelled URL published")
	}
}

type brokenElevatedLoginIO struct{ privateLoginIO }

func (e *brokenElevatedLoginIO) ReadLine() (string, error) {
	return "", errors.New("synthetic-reader-failure")
}
func TestElevatedReaderFailureCancelsBeforeVerification(t *testing.T) {
	f := newInboundFixture(t, nil)
	exec := &brokenElevatedLoginIO{}
	s := &loginSession{chatID: streamChat, private: true, ctx: t.Context(), elevated: elevatedVerifier{true}, exec: exec}
	f.b.logins.set(streamChat, s)
	f.b.finishLogin(t.Context(), streamChat, s)
	if !exec.closed || f.b.logins.get(streamChat) != nil {
		t.Fatal("broken reader not cancelled")
	}
	calls := f.fake.callsTo("sendMessage")
	if len(calls) != 1 || !strings.Contains(calls[0].Form["text"], "вход отменён") {
		t.Fatal(calls)
	}
}
