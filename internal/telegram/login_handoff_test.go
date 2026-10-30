package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot/models"
)

const handoffGroup int64 = -1003850302017

func handoffFixture(t *testing.T) (*Bot, *fakeTelegram) {
	t.Helper()
	b, fake, _ := diagnosticFixture(t)
	b.registerCallbacks()
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

// An owner /login in a group asks nothing: the owner gets the real step in the
// private chat, the group a neutral line, and no login CLI starts.
func TestGroupLoginRedirectsWithoutPromptAndStartsNoCLI(t *testing.T) {
	b, fake := handoffFixture(t)
	b.groupReason = func(context.Context, string) string { return "" }
	b.loginRun = func(context.Context, *models.Message, int, store.RegisteredChat) bool {
		t.Error("group login started a CLI")
		return false
	}
	b.loginHandler(t.Context(), b.api, &models.Update{Message: ownerGroupLogin()})
	owner, group := sendsTo(fake, streamChat), sendsTo(fake, handoffGroup)
	if len(owner) != 1 || owner[0].Form["reply_markup"] != "" || !strings.Contains(owner[0].Form["text"], "/provider") {
		t.Fatalf("owner note: %+v", owner)
	}
	if len(group) != 1 || group[0].Form["text"] != groupLoginNeutral {
		t.Fatalf("group reply: %+v", group)
	}
}

func TestGroupLoginIgnoresNonOwnerAndUnregisteredGroups(t *testing.T) {
	b, fake := handoffFixture(t)
	other := ownerGroupLogin()
	other.From = &models.User{ID: streamChat + 1}
	unregistered := ownerGroupLogin()
	unregistered.Chat.ID = handoffGroup - 1
	for name, m := range map[string]*models.Message{"non-owner": other, "unregistered": unregistered} {
		if b.requestLoginHandoff(t.Context(), m) {
			t.Fatalf("%s handled", name)
		}
	}
	if n := len(fake.callsTo("sendMessage")); n != 0 {
		t.Fatalf("unexpected sends: %d", n)
	}
}

func TestGroupLoginRedirectIsRateLimitedPerGroup(t *testing.T) {
	b, fake := handoffFixture(t)
	b.groupReason = func(context.Context, string) string { return "" }
	if !b.requestLoginHandoff(t.Context(), ownerGroupLogin()) {
		t.Fatal("first redirect refused")
	}
	if b.requestLoginHandoff(t.Context(), ownerGroupLogin()) {
		t.Fatal("repeat inside the cooldown was not limited")
	}
	if n := len(sendsTo(fake, handoffGroup)); n != 1 {
		t.Fatalf("group got %d lines", n)
	}
}

func TestGroupLoginAddressedToAnotherBotIsIgnored(t *testing.T) {
	b, fake := handoffFixture(t)
	m := ownerGroupLogin()
	m.Text = "/login@SomeOtherBot"
	if b.requestLoginHandoff(t.Context(), m) {
		t.Fatal("command for another bot handled")
	}
	if n := len(fake.callsTo("sendMessage")); n != 0 {
		t.Fatalf("unexpected sends: %d", n)
	}
}
