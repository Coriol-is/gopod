package telegram

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/store"
)

const triggerPickerMsgID = 500

// groupSlash is a typed slash command by user in the registered supergroup.
func groupSlash(user int64, text string) *models.Message {
	m := slashMsg(groupChat, 0, text)
	m.Chat.Type = models.ChatTypeSupergroup
	m.From = &models.User{ID: user}
	return m
}

// groupPress is a button press by user on a message in the supergroup.
func groupPress(id string, user int64, data string) *models.Update {
	u := cbUpdate(id, groupChat, 0, triggerPickerMsgID, data)
	u.CallbackQuery.From = models.User{ID: user}
	u.CallbackQuery.Message.Message.Chat.Type = models.ChatTypeSupergroup
	return u
}

func triggerPickerFixture(t *testing.T) (*Bot, *fakeTelegram) {
	t.Helper()
	b, fake, r := newCommandFixture(t)
	control.RegisterTriggerCommand(r, b.store)
	return b, fake
}

func TestTriggerPicker_RendersAllModesWithCurrentMarked(t *testing.T) {
	b, fake := triggerPickerFixture(t)
	b.dispatchSlash(t.Context(), groupSlash(streamChat, "/trigger"), "/trigger")
	btns := buttonsOf(t, lastCall(t, fake, "sendMessage"))
	if len(btns) != 4 {
		t.Fatalf("buttons = %d, want 4", len(btns))
	}
	want := []string{"owner", "✓ mention", "owner+mention", "all"}
	for i, w := range want {
		if btns[i].Text != w {
			t.Errorf("button %d = %q, want %q", i, btns[i].Text, w)
		}
		if len(btns[i].CallbackData) > cbMaxBytes || !strings.HasPrefix(btns[i].CallbackData, "cc:") ||
			strings.Count(btns[i].CallbackData, ":") != 1 {
			t.Errorf("button %d data %q", i, btns[i].CallbackData)
		}
	}
}

func TestTriggerPicker_GroupOwnerPressSetsModeAndEditsMessage(t *testing.T) {
	b, fake := triggerPickerFixture(t)
	b.dispatchSlash(t.Context(), groupSlash(streamChat, "/trigger"), "/trigger")
	data := buttonData(t, lastCall(t, fake, "sendMessage"), "all")

	b.callbackHandler(t.Context(), b.api, groupPress("q1", streamChat, data))

	answeredOnce(t, fake, "q1")
	if mode, _ := b.store.GroupTrigger(t.Context(), buildChatJID(groupChat)); mode != store.TriggerAll {
		t.Fatalf("mode = %q", mode)
	}
	edit := lastCall(t, fake, "editMessageText")
	if edit.Form["message_id"] != "500" || !strings.Contains(edit.Form["text"], "Trigger mode: all") {
		t.Fatalf("edit = %v", edit.Form)
	}

	// A duplicate press of the same button is harmless.
	b.callbackHandler(t.Context(), b.api, groupPress("q2", streamChat, data))
	answeredOnce(t, fake, "q2")
	if mode, _ := b.store.GroupTrigger(t.Context(), buildChatJID(groupChat)); mode != store.TriggerAll {
		t.Fatalf("mode after duplicate = %q", mode)
	}
}

func TestTriggerPicker_MemberPressIsDeniedWithoutChange(t *testing.T) {
	b, fake := triggerPickerFixture(t)
	b.dispatchSlash(t.Context(), groupSlash(streamChat, "/trigger"), "/trigger")
	data := buttonData(t, lastCall(t, fake, "sendMessage"), "all")

	b.callbackHandler(t.Context(), b.api, groupPress("q1", 777, data))

	if toast := answeredOnce(t, fake, "q1"); toast != "Not authorized." {
		t.Errorf("toast = %q", toast)
	}
	if mode, _ := b.store.GroupTrigger(t.Context(), buildChatJID(groupChat)); mode != store.TriggerMention {
		t.Fatalf("mode changed to %q", mode)
	}
	if n := len(fake.callsTo("editMessageText")); n != 0 {
		t.Errorf("message edited %d times after a denial", n)
	}
}

func TestTriggerPicker_StalePressIsExpired(t *testing.T) {
	b, fake := triggerPickerFixture(t)
	b.callbackHandler(t.Context(), b.api, groupPress("q1", streamChat, "cc:deadbeef0000"))
	if toast := answeredOnce(t, fake, "q1"); toast != cbToastExpired {
		t.Errorf("toast = %q", toast)
	}
	if mode, _ := b.store.GroupTrigger(t.Context(), buildChatJID(groupChat)); mode != store.TriggerMention {
		t.Fatalf("mode changed to %q", mode)
	}
}

func TestTriggerPicker_PrivateChatPickerTargetsTheGroup(t *testing.T) {
	b, fake := triggerPickerFixture(t)
	b.dispatchSlash(t.Context(), slashMsg(streamChat, 0, "/trigger -100500"), "/trigger -100500")
	sent := lastCall(t, fake, "sendMessage")
	data := buttonData(t, sent, "owner")
	if strings.Contains(strings.TrimPrefix(data, "cc:"), "-100500") {
		t.Fatalf("group id leaked into callback data %q", data)
	}

	b.callbackHandler(t.Context(), b.api, cbUpdate("q1", streamChat, 0, triggerPickerMsgID, data))

	answeredOnce(t, fake, "q1")
	if mode, _ := b.store.GroupTrigger(t.Context(), buildChatJID(groupChat)); mode != store.TriggerOwner {
		t.Fatalf("mode = %q", mode)
	}
	if edit := lastCall(t, fake, "editMessageText"); !strings.Contains(edit.Form["text"], "Trigger mode: owner") {
		t.Fatalf("edit = %v", edit.Form)
	}
}
