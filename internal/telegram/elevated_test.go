package telegram

import (
	"context"
	"github.com/go-telegram/bot/models"
	"strings"
	"testing"
)

type rootInputProbe struct {
	calls  int
	active bool
}

func (p *rootInputProbe) Active() bool       { return p.active }
func (p *rootInputProbe) FreshAfter() int64  { return 100 }
func (p *rootInputProbe) TakeNotice() string { return "" }
func (p *rootInputProbe) StopAnswer() bool   { return true }
func (p *rootInputProbe) Run(context.Context, string) (string, error) {
	p.calls++
	return strings.Repeat("x", 3500), nil
}
func TestElevatedOnlyFreshOwnerTextAndEveryChunkMarked(t *testing.T) {
	b, fake, _ := diagnosticFixture(t)
	p := &rootInputProbe{active: true}
	b.elevated = p
	m := &models.Message{ID: 20, Date: 101, Text: "hello", Chat: models.Chat{ID: b.ownerChatID, Type: models.ChatTypePrivate}, From: &models.User{ID: b.ownerChatID}}
	for _, mutate := range []func(*models.Message){func(m *models.Message) { m.Chat.Type = models.ChatTypeGroup }, func(m *models.Message) { m.Chat.ID++ }, func(m *models.Message) { m.From = &models.User{ID: 999} }} {
		messageCopy := *m
		mutate(&messageCopy)
		if b.handleElevated(t.Context(), &messageCopy) {
			t.Fatal("consumed non-owner private input")
		}
	}
	old := *m
	old.Date = 99
	if !b.handleElevated(t.Context(), &old) || p.calls != 0 {
		t.Fatal("old input executed")
	}
	media := *m
	media.Photo = []models.PhotoSize{{FileID: "photo"}}
	b.handleElevated(t.Context(), &media)
	if p.calls != 0 {
		t.Fatal("media executed")
	}
	if !b.handleElevated(t.Context(), m) || p.calls != 1 {
		t.Fatal("fresh owner input not routed")
	}
	for _, call := range fake.callsTo("sendMessage") {
		if !strings.Contains(call.Form["text"], "ROOT") {
			t.Fatal("unmarked elevated chunk", call)
		}
	}
}

func TestElevatedNeverInterceptsPrivateLoginCode(t *testing.T) {
	b, _, _ := diagnosticFixture(t)
	p := &rootInputProbe{active: true}
	b.elevated = p
	b.logins = &loginSessions{sessions: map[int64]*loginSession{b.ownerChatID: {private: true, actorID: b.ownerChatID, awaitsCode: true}}}
	var code string
	b.loginCode = func(_ context.Context, _ int64, s string) { code = s }
	b.defaultHandler(t.Context(), nil, &models.Update{Message: &models.Message{ID: 1, Date: 101, Text: "code-for-login", Chat: models.Chat{ID: b.ownerChatID, Type: models.ChatTypePrivate}, From: &models.User{ID: b.ownerChatID}}})
	if p.calls != 0 || code != "code-for-login" {
		t.Fatal("code reached model or wrong auth path")
	}
}
