package telegram

import (
	"context"
	"github.com/go-telegram/bot"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/go-telegram/bot/models"
)

func diagnosticFixture(t *testing.T) (*Bot, *fakeTelegram, *int) {
	t.Helper()
	fake := threadFake()
	b, _ := newStreamFixture(t, fake)
	b.ownerChatID = streamChat
	b.router = control.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	control.RegisterBuiltins(b.router)
	refreshes := new(int)
	control.RegisterTelegramCommands(b.router, b.TelegramSnapshot, func(context.Context) control.TelegramSnapshot { *refreshes++; return b.TelegramSnapshot() }, func(context.Context, int64) bool { return false })
	b.capabilities = newCapabilityCache(func(context.Context) capabilityResult { return capabilityResult{} }, time.Now)
	b.capabilities.view.Username = "ExampleBot"
	return b, fake, refreshes
}

func TestDiagnosticsDefaultHandlerBeforeRunnerAndMedia(t *testing.T) {
	for _, command := range []string{"/telegram", "/telegram doctor", "/telegram doctor refresh", "/help guest", "/help inline", "/help topics"} {
		t.Run(command, func(t *testing.T) {
			b, fake, _ := diagnosticFixture(t)
			m := threadedMsg(7, "")
			m.Caption = command
			m.MediaGroupID = "album"
			m.Voice = &models.Voice{FileID: "never-download"}
			b.defaultHandler(t.Context(), b.api, &models.Update{Message: m})
			if _, err := b.store.GetRegistered(t.Context(), buildChatJID(streamChat)); err != nil {
				t.Fatalf("owner not registered: %v", err)
			}
			calls := fake.callsTo("sendMessage")
			if len(calls) != 1 {
				t.Fatalf("sends=%d want 1", len(calls))
			}
			if calls[0].Form["message_thread_id"] != "7" {
				t.Fatal("lost topic")
			}
			for _, method := range []string{"getFile", "sendChatAction", "sendMessageDraft"} {
				if len(fake.callsTo(method)) != 0 {
					t.Fatalf("unexpected %s", method)
				}
			}
			if strings.Contains(calls[0].Form["reply_markup"], "callback_data") {
				t.Fatal("diagnostic produced callbacks")
			}
		})
	}
}

func TestDiagnosticsNonPrivateNeverRespondOrRefresh(t *testing.T) {
	for _, kind := range []models.ChatType{models.ChatTypeGroup, models.ChatTypeSupergroup, models.ChatTypeChannel, ""} {
		for _, text := range []string{"/telegram", "/telegram doctor refresh", "/telegram wrong", "/telegram@ExampleBot doctor refresh", "/help guest", "/help inline extra", "/help topics"} {
			t.Run(string(kind)+text, func(t *testing.T) {
				b, fake, refreshes := diagnosticFixture(t)
				m := threadedMsg(0, text)
				m.Chat.Type = kind
				b.defaultHandler(t.Context(), b.api, &models.Update{Message: m})
				if len(fake.callsTo("sendMessage")) != 0 || *refreshes != 0 {
					t.Fatal("public diagnostic output or fetch")
				}
				b.router = nil
				b.dispatchSlash(t.Context(), m, text)
				if len(fake.callsTo("sendMessage")) != 0 {
					t.Fatal("router-nil error leaked")
				}
			})
		}
	}
}

func TestDiagnosticsSuffixFailsClosed(t *testing.T) {
	for _, item := range []struct {
		text, username string
		sends          int
	}{
		{"/telegram@eXampleBot", "ExampleBot", 1},
		{"/telegram@otherbot", "ExampleBot", 0},
		{"/telegram@ExampleBot", "", 0},
		{"/telegram@ExampleBot@other", "ExampleBot", 0},
		{"/help@otherbot inline", "ExampleBot", 0},
		{"/telegram", "", 1},
	} {
		t.Run(item.text+item.username, func(t *testing.T) {
			b, fake, _ := diagnosticFixture(t)
			b.capabilities.view.Username = item.username
			b.defaultHandler(t.Context(), b.api, &models.Update{Message: threadedMsg(0, item.text)})
			if got := len(fake.callsTo("sendMessage")); got != item.sends {
				t.Fatalf("sends=%d want %d", got, item.sends)
			}
		})
	}
}

func TestDiagnosticsMenusScopedAndReportNotPaged(t *testing.T) {
	b, fake, _ := diagnosticFixture(t)
	b.publishCommands(t.Context())
	calls := fake.callsTo("setMyCommands")
	if len(calls) != 2 {
		t.Fatalf("menus=%d", len(calls))
	}
	if strings.Contains(calls[0].Form["commands"], `"command":"telegram"`) {
		t.Fatal("diagnostics published globally")
	}
	if !strings.Contains(calls[1].Form["scope"], "all_private_chats") || !strings.Contains(calls[1].Form["commands"], `"command":"telegram"`) {
		t.Fatal("private menu missing diagnostics")
	}
	b.dispatchSlash(t.Context(), threadedMsg(0, "/telegram doctor"), "/telegram doctor")
	call := lastCall(t, fake, "sendMessage")
	if len(call.Form["text"]) > pageMaxHTMLBytes || strings.Contains(call.Form["reply_markup"], "callback_data") {
		t.Fatal("doctor paginated")
	}
	b.sendDiagnosticResponse(t.Context(), threadedMsg(0, ""), strings.Repeat("<", 5000))
	if got := lastCall(t, fake, "sendMessage").Form["text"]; !strings.Contains(got, "safe message limit") {
		t.Fatal("missing bounded fallback")
	}
}

func TestDiagnosticNewDoesNotFetchAndRejectsMalformedToken(t *testing.T) {
	b, _, _ := diagnosticFixture(t)
	fresh, err := New("123:test", Deps{Store: b.store, Log: b.log})
	if err != nil || fresh.capabilities == nil {
		t.Fatalf("New: %v", err)
	}
	if !fresh.TelegramSnapshot().LastAttempt.IsZero() {
		t.Fatal("constructor fetched getMe")
	}
	for _, token := range []string{"invalid", "0:secret", "123:", "123:bad/secret"} {
		if _, err := New(token, Deps{Store: b.store}); err == nil {
			t.Fatalf("accepted %q", token)
		}
	}
}

func TestDiagnosticsPreserveOrdinaryCommand(t *testing.T) {
	b, fake, _ := diagnosticFixture(t)
	b.dispatchSlash(t.Context(), threadedMsg(0, "/ping"), "/ping")
	if !strings.Contains(lastCall(t, fake, "sendMessage").Form["text"], "pong") {
		t.Fatal("ordinary command changed")
	}
	// No getMe HTTP request is required for an unsuffixed private command.
}

func TestDiagnosticsSlowGetMeDoesNotBlockPollingAndJoins(t *testing.T) {
	b, fake, _ := diagnosticFixture(t)
	started, joined := make(chan struct{}), make(chan struct{})
	b.capabilities = newCapabilityCache(func(ctx context.Context) capabilityResult {
		close(started)
		<-ctx.Done()
		close(joined)
		return capabilityResult{class: "timeout"}
	}, time.Now)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	polled := make(chan struct{})
	var once sync.Once
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "getUpdates" {
			once.Do(func() { close(polled); cancel() })
			return http.StatusOK, `{"ok":true,"result":[]}`
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	api, err := bot.New("123:test", append(b.botOptions(), bot.WithServerURL(srv.URL), bot.WithSkipGetMe())...)
	if err != nil {
		t.Fatal(err)
	}
	b.api = api
	done := make(chan struct{})
	go func() { _ = b.Run(ctx); close(done) }()
	select {
	case <-polled:
	case <-time.After(2 * time.Second):
		t.Fatal("getMe blocked polling")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not join getMe")
	}
	select {
	case <-started:
	default:
		t.Fatal("no initial observation")
	}
	select {
	case <-joined:
	default:
		t.Fatal("getMe survived shutdown")
	}
}

func TestDoctorRenderedTechnicalNamesAndRuntimeFitSingleMessage(t *testing.T) {
	b, fake, _ := diagnosticFixture(t)
	s := control.TelegramSnapshot{Runtime: "configured", Freshness: "fresh", RefreshState: "in_flight", ErrorClass: "api_rejection", AllowedUpdates: []string{"message", "callback_query", "stopped_message_generation"}, Startup: &control.TelegramRuntimeObservation{CheckedAt: time.Now().Add(-time.Hour), Provider: "claude", CLIVersion: "2.1.310", Container: "selection_unavailable", CLI: "cleanup_failed", Auth: "not_authenticated"}}
	b.router = control.New(b.log)
	control.RegisterTelegramCommands(b.router, func() control.TelegramSnapshot { return s }, nil, func(context.Context, int64) bool { return true })
	b.dispatchSlash(t.Context(), threadedMsg(7, "/telegram doctor"), "/telegram doctor")
	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 {
		t.Fatalf("sends=%d", len(sends))
	}
	rendered := sends[0].Form["text"]
	for _, want := range []string{"<code>callback_query</code>", "<code>stopped_message_generation</code>", "<code>inline_query</code>", "<code>guest_message</code>", "<code>business_message</code>", "<code>in_flight</code>", "<code>api_rejection</code>", "code_unsupported,telegram_unknown,not_subscribed,context_rights_unknown", "<code>cleanup_failed</code>", "<code>not_authenticated</code>", "Refresh permitted after:", "Current health unverified"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing exact %q in %s", want, rendered)
		}
	}
	if len(rendered) > pageMaxHTMLBytes || strings.Contains(sends[0].Form["reply_markup"], "callback_data") {
		t.Fatal("report paginated")
	}
}

func TestDoctorWorstCaseRuntimeAndUnknownReasonsRemainSingleMessage(t *testing.T) {
	b, fake, _ := diagnosticFixture(t)
	s := control.TelegramSnapshot{Runtime: "absent", Freshness: "unknown", RefreshState: "in_flight", ErrorClass: "api_rejection", CheckedAt: time.Now().Add(-time.Hour), LastAttempt: time.Now(), NextRefresh: time.Now().Add(time.Minute), Startup: &control.TelegramRuntimeObservation{CheckedAt: time.Now().Add(-time.Hour), Provider: "claude", CLIVersion: "123456789.123456789.123456789", Container: "selection_unavailable", CLI: "cleanup_failed", Auth: "not_authenticated"}}
	b.router = control.New(b.log)
	control.RegisterTelegramCommands(b.router, func() control.TelegramSnapshot { return s }, nil, func(context.Context, int64) bool { return true })
	b.dispatchSlash(t.Context(), threadedMsg(0, "/telegram doctor"), "/telegram doctor")
	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 {
		t.Fatalf("sends=%d", len(sends))
	}
	rendered := sends[0].Form["text"]
	if len(rendered) > pageMaxHTMLBytes || strings.Contains(rendered, "safe message limit") || strings.Contains(sends[0].Form["reply_markup"], "callback_data") {
		t.Fatalf("worst-case report invalid (%dbytes): %s", len(rendered), rendered)
	}
}
