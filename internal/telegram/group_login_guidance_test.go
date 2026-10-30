package telegram

import (
	"context"
	"strings"
	"testing"
)

func TestGroupLoginExplainsTheRealStepPrivatelyAndStaysNeutralInTheGroup(t *testing.T) {
	b, fake := handoffFixture(t)
	b.groupReason = func(context.Context, string) string { return "" } // admitted
	if !b.requestLoginHandoff(t.Context(), ownerGroupLogin()) {
		t.Fatal("redirect not sent")
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

func TestGroupLoginGuidanceDoesNotBlameTheAccountWhenGroupIsNotAdmitted(t *testing.T) {
	b, fake := handoffFixture(t)
	b.groupReason = func(context.Context, string) string { return "disabled by owner" }
	target, _ := b.groupLoginTarget(t.Context(), handoffGroup)
	b.groupLoginGuidance(t.Context(), streamChat, 0, target)
	owner := sendsTo(fake, streamChat)
	if len(owner) != 1 || strings.Contains(owner[0].Form["text"], "your account") || !strings.Contains(owner[0].Form["text"], "disabled by owner") {
		t.Fatalf("owner note: %+v", owner)
	}
}
