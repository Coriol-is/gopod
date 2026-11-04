package telegram

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
)

func ownershipFixture(t *testing.T, fake *fakeTelegram) (*Bot, string, store.ScopeRef) {
	t.Helper()
	b := newFakeBot(t, fake)
	b.ownerChatID = streamChat
	path := filepath.Join(t.TempDir(), "store.sqlite")
	var err error
	b.store, err = store.Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.store.Close() })
	if err = b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(streamChat), Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	scope, err := b.store.SnapshotScope(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	return b, path, scope
}
func reopenOwnership(t *testing.T, b *Bot, path string) {
	t.Helper()
	_ = b.store.Close()
	var err error
	b.store, err = store.Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.topicMessages = nil
	b.topicMessageOrder = nil
	b.topicUncertain = nil
}
func TestTopicOwnershipRestartSourceAndKnownMain(t *testing.T) {
	n := 700
	fake := &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			n++
			return sentMessage(strconv.Itoa(n))
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
	b, path, scope := ownershipFixture(t, fake)
	fallback, err := b.ownedTextSend(t.Context(), streamChat, 7, &bot.SendMessageParams{ChatID: streamChat, Text: "source7"})
	if err != nil {
		t.Fatal(err)
	}
	main, err := b.ownedTextSend(t.Context(), streamChat, 0, &bot.SendMessageParams{ChatID: streamChat, Text: "main"})
	if err != nil {
		t.Fatal(err)
	}
	reopenOwnership(t, b, path)
	// Host cache corruption cannot override persisted source.
	b.rememberTopicMessage(t.Context(), streamChat, 0, fallback.ID)
	calls := 0
	b.onCallback("probe", func(context.Context, cbQuery) (string, error) { calls++; return "ok", nil })
	d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.dispatchCallback(t.Context(), cbUpdate("source", streamChat, 0, fallback.ID, "probe").CallbackQuery, func(string) {}); got != cbToastExpired || calls != 0 {
		t.Fatalf("source lost %q %d", got, calls)
	}
	if got := b.dispatchCallback(t.Context(), cbUpdate("main", streamChat, 0, main.ID, "probe").CallbackQuery, func(string) {}); got != "ok" || calls != 1 {
		t.Fatalf("known main blocked %q %d", got, calls)
	}
	if got := b.dispatchCallback(t.Context(), cbUpdate("old", streamChat, 0, 9999, "probe").CallbackQuery, func(string) {}); got != cbToastExpired {
		t.Fatalf("unknown legacy authority %q", got)
	}
	if got := b.dispatchCallback(t.Context(), cbUpdate("sibling", streamChat, 8, 9998, "probe").CallbackQuery, func(string) {}); got != "ok" {
		t.Fatalf("sibling blocked %q", got)
	}
	edited := false
	mainCtx := context.WithValue(t.Context(), topicOutputOriginKey{}, chatThread{streamChat, 0})
	if err = b.topicEditRequest(mainCtx, streamChat, fallback.ID, func(context.Context) error { edited = true; return nil }); err == nil || edited {
		t.Fatalf("context overrides durable owner %v", err)
	}
	if err = b.store.TopicMessageOwnershipIdle(t.Context(), d); err != nil {
		t.Fatal(err)
	}
}
func TestTopicOwnershipFailureWindowsDoNotReplay(t *testing.T) {
	for _, window := range []string{"preinsert", "unknown_remote", "ack_failure", "definite_refusal"} {
		t.Run(window, func(t *testing.T) {
			fake := &fakeTelegram{}
			b, path, scope := ownershipFixture(t, fake)
			fake.respond = func(method string, _ map[string]string) (int, string) {
				if method != "sendMessage" {
					return http.StatusOK, `{"ok":true,"result":true}`
				}
				if window == "unknown_remote" {
					return apiError(500, "unavailable")
				}
				if window == "definite_refusal" {
					return apiError(400, "Bad Request: rejected")
				}
				if window == "ack_failure" {
					_ = b.store.Close()
				}
				return sentMessage("800")
			}
			if window == "preinsert" {
				_ = b.store.Close()
			}
			_, err := b.ownedTextSend(t.Context(), streamChat, 7, &bot.SendMessageParams{ChatID: streamChat, Text: "sensitive text not stored"})
			if err == nil {
				t.Fatal("expected failure")
			}
			expected := 1
			if window == "preinsert" {
				expected = 0
			}
			if got := len(fake.callsTo("sendMessage")); got != expected {
				t.Fatalf("remote calls %d", got)
			}
			reopenOwnership(t, b, path)
			d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
			if err != nil {
				t.Fatal(err)
			}
			err = b.QuiesceTopic(t.Context(), d)
			if window == "ack_failure" || window == "unknown_remote" {
				if !errors.Is(err, store.ErrMessageOwnership) {
					t.Fatalf("lost durable ambiguity: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(fake.callsTo("sendMessage")) != expected {
				t.Fatal("reopen replayed send")
			}
		})
	}
}
func TestTopicOwnershipLookupFailureDoesNotTrustCallback(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, path, _ := ownershipFixture(t, fake)
	sent, err := b.ownedTextSend(t.Context(), streamChat, 0, &bot.SendMessageParams{ChatID: streamChat, Text: "main"})
	if err != nil {
		t.Fatal(err)
	}
	_ = b.store.Close()
	called := false
	ctx := context.WithValue(t.Context(), topicOutputOriginKey{}, chatThread{streamChat, 0})
	if err = b.topicEditRequest(ctx, streamChat, sent.ID, func(context.Context) error { called = true; return nil }); err == nil || called {
		t.Fatalf("lookup failure bypass %v", err)
	}
	reopenOwnership(t, b, path)
}

func TestTopicOwnershipOuterHelpersDoNotReplaceUnrecordedSend(t *testing.T) {
	for _, kind := range []string{"chunks", "placeholder", "login", "gone_fallback"} {
		t.Run(kind, func(t *testing.T) {
			fake := &fakeTelegram{}
			b, path, scope := ownershipFixture(t, fake)
			fake.respond = func(method string, _ map[string]string) (int, string) {
				if method == "sendMessage" {
					_ = b.store.Close()
					return sentMessage("901")
				}
				return http.StatusOK, `{"ok":true,"result":true}`
			}
			ctx := withTopicOutputOrigin(t.Context(), streamChat, 7)
			switch kind {
			case "chunks":
				id, err := b.sendChunks(ctx, streamChat, 0, 0, "first", markdownToTelegramHTML, nil, 7)
				if id != 901 || !noReplayError(err) {
					t.Fatalf("receipt lost %d %v", id, err)
				}
			case "placeholder":
				if id := b.sendPlaceholderText(ctx, streamChat, 0, 0, "waiting", nil); id != 901 {
					t.Fatalf("placeholder receipt %d", id)
				}
			case "login":
				if _, err := b.sendPrivateLoginText(ctx, streamChat, 0, "private", nil); err == nil {
					t.Fatal("missing failure")
				}
			case "gone_fallback":
				b.markThreadGone(streamChat, 7)
				id, err := b.sendRenderedOpts(ctx, streamChat, 7, 0, "answer", markdownToTelegramHTML, nil, true)
				if id != 901 || !noReplayError(err) {
					t.Fatalf("fallback receipt lost %d %v", id, err)
				}
			}
			reopenOwnership(t, b, path)
			// This operation itself made one call. Independent later output
			// remains sendable; unresolved ownership still blocks retirement.
			if len(fake.callsTo("sendMessage")) != 1 {
				t.Fatal("operation replayed")
			}
			fake.respond = func(method string, _ map[string]string) (int, string) {
				if method == "sendMessage" {
					return sentMessage("902")
				}
				return http.StatusOK, `{"ok":true,"result":true}`
			}
			if _, err := b.sendFormattedErr(ctx, streamChat, 0, 0, "independent later reply"); err != nil {
				t.Fatal(err)
			}
			if len(fake.callsTo("sendMessage")) != 2 {
				t.Fatal("independent reply suppressed")
			}

			d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
			if err != nil {
				t.Fatal(err)
			}
			if err = b.store.TopicMessageOwnershipIdle(t.Context(), d); !errors.Is(err, store.ErrMessageOwnership) {
				t.Fatal("lost blocker", err)
			}
		})
	}
}
func TestTopicOwnershipUnregisteredDiagnosticCompatibility(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, _, _ := ownershipFixture(t, fake)
	sent, err := b.ownedTextSend(t.Context(), 2002, 0, &bot.SendMessageParams{ChatID: 2002, Text: "setup"})
	if err != nil || sent == nil {
		t.Fatalf("unregistered diagnostic %v", err)
	}
}

func TestTopicOwnershipIndependentConcurrentAndLaterOutput(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	fake := &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
		if method != "sendMessage" {
			return http.StatusOK, `{"ok":true,"result":true}`
		}
		switch form["text"] {
		case "first":
			close(entered)
			<-release
			return apiError(500, "unknown outcome")
		case "second":
			return sentMessage("1202")
		case "later":
			return sentMessage("1203")
		default:
			return sentMessage("1204")
		}
	}}
	b, path, scope := ownershipFixture(t, fake)
	done := make(chan error, 1)
	go func() {
		_, err := b.ownedTextSend(t.Context(), streamChat, 7, &bot.SendMessageParams{ChatID: streamChat, Text: "first"})
		done <- err
	}()
	<-entered
	if _, err := b.ownedTextSend(t.Context(), streamChat, 7, &bot.SendMessageParams{ChatID: streamChat, Text: "second"}); err != nil {
		t.Fatalf("independent concurrent suppressed: %v", err)
	}
	close(release)
	if err := <-done; !noReplayError(err) {
		t.Fatal(err)
	}
	if _, err := b.sendChunks(t.Context(), streamChat, 0, 0, "later", markdownToTelegramHTML, nil, 7); err != nil {
		t.Fatalf("later same-process reply suppressed: %v", err)
	}
	reopenOwnership(t, b, path)
	if _, err := b.sendChunks(t.Context(), streamChat, 0, 0, "afterrestart", markdownToTelegramHTML, nil, 7); err != nil {
		t.Fatalf("later restart reply suppressed: %v", err)
	}
	d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.QuiesceTopic(t.Context(), d); !errors.Is(err, store.ErrMessageOwnership) {
		t.Fatalf("unknown first request lost: %v", err)
	}
}
func TestTopicOwnershipErrorReplyFallbackIsDurable(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, path, scope := ownershipFixture(t, fake)
	b.markThreadGone(streamChat, 7)
	b.sendErrorReply(t.Context(), queue.Item{ChatID: streamChat, ThreadID: 7}, "retry", noKeyboard())
	reopenOwnership(t, b, path)
	origin, known, err := b.messageOwner(t.Context(), streamChat, 88)
	if err != nil || !known || origin.thread != 7 {
		t.Fatalf("error controls lost source %+v %v %v", origin, known, err)
	}
	if _, _, err = b.store.BeginTopicDeletion(t.Context(), scope, 7); err != nil {
		t.Fatal(err)
	}
	called := false
	b.onCallback("probe", func(context.Context, cbQuery) (string, error) { called = true; return "ok", nil })
	if got := b.dispatchCallback(t.Context(), cbUpdate("retry", streamChat, 0, 88, "probe").CallbackQuery, func(string) {}); got != cbToastExpired || called {
		t.Fatal("error callback escaped source")
	}
}
func TestTopicOwnershipUnknownPlaceholderDoesNotFallback(t *testing.T) {
	fake := &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			return apiError(500, "unknown")
		}
		return apiError(400, "unsupported")
	}}
	b, _, _ := ownershipFixture(t, fake)
	b.markDraftsUnsupported(streamChat)
	chunks := make(chan string)
	close(chunks)
	result := make(chan runner.RunResult, 1)
	result <- runner.RunResult{FullText: "answer"}
	close(result)
	err := b.streamReply(t.Context(), queue.Item{ChatID: streamChat, Folder: "owner"}, &runner.AgentStream{Chunks: chunks, Result: result})
	if !noReplayError(err) || len(fake.callsTo("sendMessage")) != 1 {
		t.Fatalf("placeholder replay %v calls%d", err, len(fake.callsTo("sendMessage")))
	}
}
