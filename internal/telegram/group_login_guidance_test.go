package telegram

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/runner"
)

func TestConfirmedGroupLoginExplainsTheRealStepPrivatelyAndStaysNeutralInTheGroup(t *testing.T) {
	b, fake := handoffFixture(t)
	b.runner = &runner.Runner{} // a group profile is never available: Ensure refuses
	target, ok := b.groupLoginTarget(t.Context(), handoffGroup)
	if !ok {
		t.Fatal("group target")
	}
	private := &models.Message{Chat: models.Chat{ID: streamChat, Type: models.ChatTypePrivate}, From: &models.User{ID: streamChat}}
	if b.runLoginTarget(t.Context(), private, 0, target, true) {
		t.Fatal("no session may be stored")
	}
	owner, group := sendsTo(fake, streamChat), sendsTo(fake, handoffGroup)
	if len(owner) != 1 || !strings.Contains(owner[0].Form["text"], "/provider") || !strings.Contains(owner[0].Form["text"], "/login") || strings.Contains(owner[0].Form["text"], loginTextFailed) {
		t.Fatalf("owner note: %+v", owner)
	}
	if len(group) != 1 || group[0].Form["text"] != groupLoginNeutral || group[0].Form["reply_markup"] != "" {
		t.Fatalf("group reply: %+v", group)
	}
	for _, bad := range []string{"claude", "codex", "account", "/provider", "http", "token"} {
		if strings.Contains(strings.ToLower(group[0].Form["text"]), bad) {
			t.Fatalf("group text leaks %q", bad)
		}
	}
}

// The private confirmation is unchanged by D048: the group /login still asks
// the owner in the private chat first and starts nothing in the group.
func TestGroupLoginStillRequiresPrivateConfirmation(t *testing.T) {
	b, fake := handoffFixture(t)
	if !b.requestLoginHandoff(t.Context(), ownerGroupLogin()) {
		t.Fatal("handoff not requested")
	}
	if len(sendsTo(fake, handoffGroup)) != 0 {
		t.Fatal("group got output before confirmation")
	}
	sends := sendsTo(fake, streamChat)
	if len(sends) != 1 || len(buttonsOf(t, sends[0])) != 2 {
		t.Fatalf("private confirmation missing: %+v", sends)
	}
}
