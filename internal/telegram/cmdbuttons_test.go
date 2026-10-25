package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/store"
)

// groupChat is a registered chat that is not the owner chat.
const groupChat = int64(-100500)

// newCommandFixture is a callback fixture (owner chat = streamChat) with
// a Router, a registered non-owner chat groupChat, and sendMessage and
// edit answers carrying ids (seqFake).
func newCommandFixture(t *testing.T) (*Bot, *fakeTelegram, *control.Router) {
	t.Helper()
	fake := seqFake()
	b := newCallbackFixture(t, fake)
	r := control.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.router = r
	b.registerCallbacks()
	if err := b.store.RegisterChat(context.Background(), store.RegisteredChat{
		JID: buildChatJID(groupChat), Folder: "grp", AddedAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("RegisterChat: %v", err)
	}
	return b, fake, r
}

// slashMsg is a typed slash command in chat (topic thread, message 300).
func slashMsg(chat int64, thread int, text string) *models.Message {
	return &models.Message{
		ID:              300,
		Chat:            models.Chat{ID: chat, Type: models.ChatTypePrivate},
		MessageThreadID: thread,
		From:            &models.User{ID: 42},
		Text:            text,
	}
}

// buttonData returns the callback data of the button labelled label in
// the reply_markup of c, failing when there is none.
func buttonData(t *testing.T, c apiCall, label string) string {
	t.Helper()
	for _, d := range buttonsOf(t, c) {
		if d.Text == label {
			if len(d.CallbackData) > cbMaxBytes {
				t.Fatalf("button %q data %d bytes", label, len(d.CallbackData))
			}
			return d.CallbackData
		}
	}
	t.Fatalf("no button %q in %s", label, c.Form["reply_markup"])
	return ""
}

func buttonsOf(t *testing.T, c apiCall) []models.InlineKeyboardButton {
	t.Helper()
	raw := c.Form["reply_markup"]
	if raw == "" {
		return nil
	}
	var kb models.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(raw), &kb); err != nil {
		t.Fatalf("reply_markup %q: %v", raw, err)
	}
	var out []models.InlineKeyboardButton
	for _, row := range kb.InlineKeyboard {
		out = append(out, row...)
	}
	return out
}

// lastCall returns the newest call to method.
func lastCall(t *testing.T, fake *fakeTelegram, method string) apiCall {
	t.Helper()
	c := fake.callsTo(method)
	if len(c) == 0 {
		t.Fatalf("no %s call", method)
	}
	return c[len(c)-1]
}

func TestCommandButton_DispatchesThroughRouter(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	var got []control.Command
	r.Register("pick", "pick", "test picker", control.PermChatLocal,
		func(_ context.Context, cmd control.Command) (control.Response, error) {
			got = append(got, cmd)
			if len(cmd.Args) == 0 {
				return control.Response{Text: "pick one", Buttons: [][]control.Button{{
					{Label: "A", Command: "pick", Args: []string{"a"}, Style: control.StylePrimary},
				}}}, nil
			}
			return control.Response{Text: "picked " + cmd.Args[0]}, nil
		})
	ctx := context.Background()

	b.dispatchSlash(ctx, slashMsg(streamChat, 7, "/pick"), "/pick")
	sent := lastCall(t, fake, "sendMessage")
	if sent.Form["message_thread_id"] != "7" {
		t.Fatalf("picker not sent in the topic: %v", sent.Form)
	}
	data := buttonData(t, sent, "A")
	if !strings.HasPrefix(data, "cc:") {
		t.Fatalf("button data %q", data)
	}
	if btn := buttonsOf(t, sent)[0]; btn.Style != "primary" {
		t.Errorf("style = %q", btn.Style)
	}
	// The data holds a token, never the command or its args.
	if strings.Contains(data, "pick") || strings.Contains(data, ":a") {
		t.Errorf("command leaked into callback data %q", data)
	}

	b.callbackHandler(ctx, b.api, cbUpdate("q1", streamChat, 7, 500, data))
	answeredOnce(t, fake, "q1")
	if len(got) != 2 {
		t.Fatalf("handler calls = %d, want 2", len(got))
	}
	c := got[1]
	if c.Name != "pick" || len(c.Args) != 1 || c.Args[0] != "a" || c.Confirmed {
		t.Fatalf("dispatched %+v", c)
	}
	if c.Caller.ChatID != streamChat || c.Caller.ThreadID != 7 || c.Caller.UserID != 42 ||
		c.Caller.Source != control.SourceTelegram || !c.Caller.IsOwner {
		t.Fatalf("caller = %+v", c.Caller)
	}
	edit := lastCall(t, fake, "editMessageText")
	if edit.Form["message_id"] != "500" || !strings.Contains(edit.Form["text"], "picked a") {
		t.Fatalf("result edit = %v", edit.Form)
	}
	if len(buttonsOf(t, edit)) != 0 {
		t.Errorf("result keeps buttons: %s", edit.Form["reply_markup"])
	}
}

func TestCommandButton_OwnerOnlyRefusedForNonOwner(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	ran := 0
	r.Register("danger", "danger", "owner thing", control.PermOwnerOnly,
		func(context.Context, control.Command) (control.Response, error) {
			ran++
			return control.Response{Text: "done"}, nil
		})
	// A button for an owner-only command shown in a non-owner chat (for
	// example offered by a chat-local command's response).
	rows := b.commandRows(groupChat, [][]control.Button{{{Label: "X", Command: "danger"}}})
	data := rows[0][0].CallbackData

	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", groupChat, 0, 900, data))

	if toast := answeredOnce(t, fake, "q1"); toast != "Not authorized." {
		t.Errorf("toast = %q", toast)
	}
	if ran != 0 {
		t.Fatal("owner-only handler ran for a non-owner chat")
	}
	if n := len(fake.callsTo("editMessageText")); n != 0 {
		t.Errorf("message edited %d times after a refusal", n)
	}
}

func TestCommandButton_ExpiredToken(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	ran := 0
	r.Register("pick", "pick", "", control.PermPublic,
		func(context.Context, control.Command) (control.Response, error) {
			ran++
			return control.Response{Text: "ok"}, nil
		})
	at := time.Now()
	fakeClock(b, &at)
	ctx := context.Background()

	// Unknown token (e.g. after a restart).
	b.callbackHandler(ctx, b.api, cbUpdate("q1", streamChat, 0, 5, "cc:deadbeef0000"))
	if toast := answeredOnce(t, fake, "q1"); toast != cbToastExpired {
		t.Errorf("unknown token toast = %q", toast)
	}

	// A token past its TTL.
	data := b.commandRows(streamChat, [][]control.Button{{{Label: "A", Command: "pick"}}})[0][0].CallbackData
	at = at.Add(cmdButtonTTL + time.Second)
	b.callbackHandler(ctx, b.api, cbUpdate("q2", streamChat, 0, 5, data))
	if toast := answeredOnce(t, fake, "q2"); toast != cbToastExpired {
		t.Errorf("expired token toast = %q", toast)
	}

	// A live token replayed from another chat.
	data = b.commandRows(streamChat, [][]control.Button{{{Label: "A", Command: "pick"}}})[0][0].CallbackData
	b.callbackHandler(ctx, b.api, cbUpdate("q3", groupChat, 0, 5, data))
	if toast := answeredOnce(t, fake, "q3"); toast != cbToastNotYours {
		t.Errorf("foreign chat toast = %q", toast)
	}
	if ran != 0 {
		t.Fatalf("handler ran %d times", ran)
	}
}

func TestCommandRows_DataUnder64Bytes(t *testing.T) {
	b, _, _ := newCommandFixture(t)
	long := strings.Repeat("x", 300) // args never reach callback data
	rows := b.commandRows(streamChat, [][]control.Button{{
		{Label: "a", Command: "model", Args: []string{long}},
		{Label: "b", Command: "provider", Args: []string{long}, Confirmed: true},
		{Label: "c"},
	}})
	for _, btn := range rows[0] {
		if btn.CallbackData == "" || len(btn.CallbackData) > cbMaxBytes {
			t.Errorf("button %q data %q", btn.Text, btn.CallbackData)
		}
	}
}
