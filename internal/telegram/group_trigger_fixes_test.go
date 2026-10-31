package telegram

import (
	"io"
	"log/slog"

	"context"
	"github.com/Coriol-is/gopod/internal/control"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/store"
)

var allModes = []store.TriggerMode{store.TriggerOwner, store.TriggerMention, store.TriggerOwnerMention, store.TriggerAll}

func TestCommandAddressedToAnotherBotIsIgnoredInEveryMode(t *testing.T) {
	for _, mode := range allModes {
		for _, who := range []int64{streamChat, trigOther} {
			f := triggerFixture(t, mode)
			for _, text := range []string{"/clear@SomeOtherBot", "/ping@someotherbot extra"} {
				m := groupMsg(who, text)
				if !f.b.groupTriggerSkips(t.Context(), m) {
					t.Fatalf("%s %d %q not skipped", mode, who, text)
				}
				f.b.defaultHandler(t.Context(), nil, &models.Update{Message: m})
			}
			if n := len(f.fake.callsTo("sendMessage")); n != 0 {
				t.Fatalf("%s: %d replies to another bot's command", mode, n)
			}
		}
	}
	// Case-insensitive match of our own username.
	f := triggerFixture(t, store.TriggerMention)
	if f.b.groupTriggerSkips(t.Context(), groupMsg(trigOther, "/ping@"+strings.ToUpper(trigBotName))) {
		t.Fatal("own username in another case skipped")
	}
}

func withRouter(f *inboundFixture) *inboundFixture {
	f.b.router = control.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	control.RegisterBuiltins(f.b.router)
	return f
}

func TestGroupCommandsFollowTheTriggerMode(t *testing.T) {
	for _, mode := range []store.TriggerMode{store.TriggerOwner, store.TriggerMention, store.TriggerOwnerMention} {
		t.Run(string(mode), func(t *testing.T) {
			f := withRouter(triggerFixture(t, mode))
			f.b.defaultHandler(t.Context(), nil, &models.Update{Message: groupMsg(trigOther, "/ping")})
			f.b.defaultHandler(t.Context(), nil, &models.Update{Message: groupMsg(trigOther, "/xyz")})
			f.b.defaultHandler(t.Context(), nil, &models.Update{Message: groupMsg(trigOther, "/xyz@"+trigBotName)})
			if n := apiCallCount(f.fake); n != 0 {
				t.Fatalf("member got %d replies %+v: bare commands and unknown commands must be silent", n, f.fake.calls)
			}
			f.b.defaultHandler(t.Context(), nil, &models.Update{Message: groupMsg(trigOther, "/ping@"+trigBotName)})
			f.b.defaultHandler(t.Context(), nil, &models.Update{Message: groupMsg(streamChat, "/ping")})
			want := 2
			if mode == store.TriggerOwner {
				want = 1
			}
			if n := len(f.fake.callsTo("sendMessage")); n != want {
				t.Fatalf("command replies = %d, want %d", n, want)
			}
		})
	}
	f := withRouter(triggerFixture(t, store.TriggerAll))
	f.b.defaultHandler(t.Context(), nil, &models.Update{Message: groupMsg(trigOther, "/xyz")})
	sends := f.fake.callsTo("sendMessage")
	if len(sends) != 1 || !strings.Contains(sends[0].Form["text"], "unknown command") {
		t.Fatalf("all mode must keep today's behaviour: %+v", sends)
	}
}

func albumItem(id int, group, fileID, caption string, entities ...models.MessageEntity) *models.Message {
	m := groupMsg(trigOther, "")
	m.ID, m.MediaGroupID, m.Caption, m.CaptionEntities = id, group, caption, entities
	m.Photo = []models.PhotoSize{{FileID: fileID, Width: 2, Height: 2}}
	return m
}

func TestAlbumIsGatedOncePerAlbum(t *testing.T) {
	mention := models.MessageEntity{Type: models.MessageEntityTypeMention, Offset: 0, Length: len(trigBotName) + 1}
	// The mention sits on a later item: every sibling is still admitted.
	f := triggerFixture(t, store.TriggerMention)
	clock := newManualClock()
	f.b.albumClock = clock
	f.handle(albumItem(21, "a1", "p1", ""))
	f.handle(albumItem(22, "a1", "p2", "@"+trigBotName+" compare", mention))
	f.handle(albumItem(23, "a1", "p3", ""))
	f.b.albumMu.Lock()
	buf := f.b.albums[albumKey{trigGroup, "a1"}]
	whole := buf != nil && buf.matched && len(buf.msgs) == 3
	f.b.albumMu.Unlock()
	if !whole {
		t.Fatal("siblings of the mention caption were not all admitted")
	}
	clock.Advance(albumWindow)
	// A group workspace is not set up in this fixture, so the admitted album
	// reaches enqueueAlbum and answers its first item with a download failure.
	waitFor(t, "album processed", func() bool { return len(f.fake.callsTo("sendMessage")) == 1 })
	if got := f.fake.callsTo("sendMessage")[0].Form["reply_parameters"]; !strings.Contains(got, `"message_id":21`) {
		t.Fatalf("album answered %s", got)
	}

	// An unaddressed album: nothing downloaded, no turn, no traffic.
	f = triggerFixture(t, store.TriggerMention)
	clock = newManualClock()
	f.b.albumClock = clock
	f.handle(albumItem(31, "a2", "q1", "nice"))
	f.handle(albumItem(32, "a2", "q2", ""))
	clock.Advance(albumWindow)
	time.Sleep(50 * time.Millisecond)
	f.none(t)
	if n := apiCallCount(f.fake); n != 0 {
		t.Fatalf("unmatched album caused %d API calls", n)
	}
}

func TestMentionGateFailsOpenWhileUsernameIsUnknown(t *testing.T) {
	f := triggerFixture(t, store.TriggerMention)
	f.b.capabilities.view.Username = "" // getMe has not succeeded yet
	mention := groupMsg(trigOther, "hi @ExampleBot")
	mention.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeMention, Offset: 3, Length: 11}}
	f.handle(mention)
	f.next(t)
	reply := groupMsg(trigOther, "and?")
	reply.ReplyToMessage = &models.Message{ID: 3, From: &models.User{ID: 999, IsBot: true}}
	f.handle(reply)
	f.next(t)
	// Plain text carries no mention: still dropped.
	f.handle(groupMsg(trigOther, "chatter"))
	f.none(t)
	// Recovery: once the username is known the precise rule applies again.
	f.b.capabilities.view.Username = trigBotName
	other := groupMsg(trigOther, "hi @OtherBot")
	other.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeMention, Offset: 3, Length: 9}}
	f.handle(other)
	f.none(t)
}

func TestResolveUsernameRetriesBoundedAndNeverFails(t *testing.T) {
	calls := 0
	c := newCapabilityCache(func(context.Context) capabilityResult {
		calls++
		if calls < 3 {
			return capabilityResult{class: "transport"}
		}
		return capabilityResult{valid: true, username: "late_bot", settings: map[string]string{}}
	}, time.Now)
	if !c.Resolve(t.Context(), 3, time.Second, time.Millisecond) || c.Snapshot().Username != "late_bot" || calls != 3 {
		t.Fatalf("calls=%d username=%q", calls, c.Snapshot().Username)
	}
	never := newCapabilityCache(func(context.Context) capabilityResult { return capabilityResult{class: "transport"} }, time.Now)
	if never.Resolve(t.Context(), 3, time.Second, time.Millisecond) {
		t.Fatal("resolved without a valid result")
	}
}

func TestRegistrationLookupErrorFailsClosed(t *testing.T) {
	f := triggerFixture(t, store.TriggerAll)
	if err := f.b.store.Close(); err != nil {
		t.Fatal(err)
	}
	if !f.b.groupTriggerSkips(t.Context(), groupMsg(trigOther, "hello")) {
		t.Fatal("a store error must skip, not answer")
	}
}
