package telegram

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// threadedSendMethods are the Bot API methods that post into a chat and
// so must carry message_thread_id inside a topic. Edits and reactions
// address an existing message and need none.
var threadedSendMethods = map[string]bool{
	"sendMessage":      true,
	"sendMessageDraft": true,
	"sendVoice":        true,
	"sendPhoto":        true,
	"sendDocument":     true,
	"sendChatAction":   true,
}

// threadFake answers sendMessage with a message id and every other
// method with true.
func threadFake() *fakeTelegram {
	return &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			return sentMessage("555")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
}

// threadedMsg is an inbound private-chat message, in topic thread
// (0 = main chat).
func threadedMsg(thread int, text string) *models.Message {
	return &models.Message{
		ID:              10,
		Chat:            models.Chat{ID: streamChat, Type: models.ChatTypePrivate},
		From:            &models.User{ID: 42, FirstName: "Owner"},
		MessageThreadID: thread,
		Text:            text,
	}
}

// threadSendCase drives one outbound path for a conversation.
type threadSendCase struct {
	name string
	// methods the case must have produced at least once.
	want []string
	run  func(t *testing.T, b *Bot, fake *fakeTelegram, item queue.Item)
}

func threadSendCases() []threadSendCase {
	streamErr := func(err error) func(*testing.T, *Bot, *fakeTelegram, queue.Item) {
		return func(_ *testing.T, b *Bot, _ *fakeTelegram, item queue.Item) {
			b.handleStreamError(context.Background(), item, err)
		}
	}
	return []threadSendCase{
		{
			name: "draft and final reply",
			want: []string{"sendMessageDraft", "sendMessage"},
			run: func(t *testing.T, b *Bot, fake *fakeTelegram, item queue.Item) {
				if err := runStreamToEnd(t, b, fake, item, "hello"); err != nil {
					t.Fatalf("streamReply: %v", err)
				}
			},
		},
		{
			name: "placeholder on the edit path",
			want: []string{"sendMessage"},
			run: func(t *testing.T, b *Bot, _ *fakeTelegram, item queue.Item) {
				b.markDraftsUnsupported(item.ChatID)
				chunks := make(chan string)
				result := make(chan runner.RunResult, 1)
				close(chunks)
				result <- runner.RunResult{FullText: "hello"}
				if err := b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result}); err != nil {
					t.Fatalf("streamReply: %v", err)
				}
			},
		},
		{
			name: "reply with plain-text fallback chunks",
			want: []string{"sendMessage"},
			run: func(_ *testing.T, b *Bot, fake *fakeTelegram, item queue.Item) {
				fake.mu.Lock()
				fake.respond = func(method string, form map[string]string) (int, string) {
					if method == "sendMessage" && form["parse_mode"] == "HTML" {
						return apiError(400, "Bad Request: can't parse entities")
					}
					return sentMessage("556")
				}
				fake.mu.Unlock()
				long := strings.Repeat("word ", 1700) // two chunks
				b.replyTo(context.Background(), item.ChatID, item.ThreadID, item.MessageID, long)
			},
		},
		{
			name: "typing action",
			want: []string{"sendChatAction"},
			run: func(_ *testing.T, b *Bot, _ *fakeTelegram, item queue.Item) {
				stop := b.startTyping(context.Background(), item.ChatID, item.ThreadID)
				stop()
			},
		},
		{
			name: "voice reply",
			want: []string{"sendVoice"},
			run: func(_ *testing.T, b *Bot, _ *fakeTelegram, item queue.Item) {
				_ = b.sendVoiceReply(context.Background(), item.ChatID, item.ThreadID, item.MessageID, []byte("OggS"))
			},
		},
		{name: "error message", want: []string{"sendMessage"}, run: streamErr(errors.New("boom"))},
		{name: "session expired message", want: []string{"sendMessage"}, run: streamErr(runner.ErrSessionExpired)},
		{name: "not logged in message", want: []string{"sendMessage"}, run: streamErr(runner.ErrNotLoggedIn)},
		{
			name: "login message",
			want: []string{"sendMessage"},
			run: func(_ *testing.T, b *Bot, _ *fakeTelegram, item queue.Item) {
				// No runner: /login answers that it cannot start.
				b.loginHandler(context.Background(), nil, &models.Update{Message: threadedMsg(item.ThreadID, "/login")})
			},
		},
		{
			name: "crash-loop notice from the done hook",
			want: []string{"sendMessage"},
			run: func(_ *testing.T, b *Bot, _ *fakeTelegram, item queue.Item) {
				b.NewDoneHook()(item, queue.ErrCrashLoop)
			},
		},
		{
			name: "unknown command",
			want: []string{"sendMessage"},
			run: func(_ *testing.T, b *Bot, _ *fakeTelegram, item queue.Item) {
				b.dispatchSlash(context.Background(), threadedMsg(item.ThreadID, "/nope"), "/nope")
			},
		},
	}
}

// runThreadSendCases runs every outbound path for a turn in thread and
// hands each send call to check.
func runThreadSendCases(t *testing.T, thread int, check func(t *testing.T, c apiCall)) {
	for _, tc := range threadSendCases() {
		t.Run(tc.name, func(t *testing.T) {
			fake := threadFake()
			b, item := newStreamFixture(t, fake)
			item.ThreadID = thread
			tc.run(t, b, fake, item)

			for _, m := range tc.want {
				if len(fake.callsTo(m)) == 0 {
					t.Fatalf("no %s call recorded", m)
				}
			}
			fake.mu.Lock()
			calls := append([]apiCall(nil), fake.calls...)
			fake.mu.Unlock()
			for _, c := range calls {
				if threadedSendMethods[c.Method] {
					check(t, c)
				}
			}
		})
	}
}

func TestRepliesCarryThreadID(t *testing.T) {
	const thread = 7
	runThreadSendCases(t, thread, func(t *testing.T, c apiCall) {
		if got := c.Form["message_thread_id"]; got != strconv.Itoa(thread) {
			t.Errorf("%s message_thread_id = %q, want %d (form %v)", c.Method, got, thread, c.Form)
		}
	})
}

func TestMainRepliesCarryNoThreadID(t *testing.T) {
	runThreadSendCases(t, 0, func(t *testing.T, c apiCall) {
		if got, ok := c.Form["message_thread_id"]; ok {
			t.Errorf("%s carries message_thread_id = %q in the main chat", c.Method, got)
		}
	})
}

// TestInboundMediaInThread checks that photos sent in a topic are
// queued with the topic's thread id, and that media failures are
// answered in the topic.
func TestInboundMediaInThread(t *testing.T) {
	t.Chdir(t.TempDir()) // a zero Runner saves uploads under ./owner
	t.Setenv("OPENAI_API_KEY", "")

	setup := func(t *testing.T, getFileOK bool) (*Bot, *fakeTelegram, <-chan queue.Item) {
		t.Helper()
		fake := &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
			switch method {
			case "getFile":
				if !getFileOK {
					return apiError(400, "Bad Request: file is too big")
				}
				return http.StatusOK, `{"ok":true,"result":{"file_id":"f","file_unique_id":"u","file_path":"photos/a.jpg"}}`
			case "sendMessage":
				return sentMessage("555")
			}
			return http.StatusOK, `{"ok":true,"result":true}`
		}}
		b, _ := newStreamFixture(t, fake)
		b.ownerChatID = streamChat
		b.logins = newLoginSessions()
		b.runner = &runner.Runner{}
		got := make(chan queue.Item, 1)
		q := queue.New(context.Background(), func(_ context.Context, items []queue.Item) error {
			got <- items[len(items)-1]
			return nil
		}, 1, nil, b.log)
		t.Cleanup(q.Close)
		b.queue = q
		return b, fake, got
	}
	photoMsg := func() *models.Message {
		m := threadedMsg(7, "")
		m.Caption = "look"
		m.Photo = []models.PhotoSize{{FileID: "f", Width: 1, Height: 1}}
		return m
	}

	t.Run("photo is queued with the thread", func(t *testing.T) {
		b, _, got := setup(t, true)
		b.defaultHandler(context.Background(), nil, &models.Update{Message: photoMsg()})
		select {
		case it := <-got:
			if it.ThreadID != 7 {
				t.Errorf("queued ThreadID = %d, want 7", it.ThreadID)
			}
			if it.FilePath == "" {
				t.Error("queued item has no FilePath")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("photo was not queued")
		}
	})

	for _, tc := range []struct {
		name string
		msg  func() *models.Message
	}{
		{"photo download failure", photoMsg},
		{"voice transcription failure", func() *models.Message {
			m := threadedMsg(7, "")
			m.Voice = &models.Voice{FileID: "v", Duration: 1}
			return m
		}},
	} {
		t.Run(tc.name+" answers in the thread", func(t *testing.T) {
			b, fake, _ := setup(t, false)
			b.defaultHandler(context.Background(), nil, &models.Update{Message: tc.msg()})
			sends := fake.callsTo("sendMessage")
			if len(sends) != 1 {
				t.Fatalf("sendMessage calls = %d, want 1", len(sends))
			}
			if got := sends[0].Form["message_thread_id"]; got != "7" {
				t.Errorf("failure reply message_thread_id = %q, want 7", got)
			}
		})
	}
}
