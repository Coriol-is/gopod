package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot"
)

// apiCall is one Bot API request seen by fakeTelegram.
type apiCall struct {
	Method string
	Form   map[string]string
}

// fakeTelegram is an httptest Bot API server. Each method is answered
// by respond; calls are recorded in order for assertions.
type fakeTelegram struct {
	mu      sync.Mutex
	calls   []apiCall
	respond func(method string, form map[string]string) (status int, body string)
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	form := map[string]string{}
	if err := r.ParseMultipartForm(1 << 20); err == nil && r.MultipartForm != nil {
		for k, v := range r.MultipartForm.Value {
			if len(v) > 0 {
				form[k] = v[0]
			}
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, apiCall{Method: method, Form: form})
	respond := f.respond
	f.mu.Unlock()

	status, body := http.StatusOK, `{"ok":true,"result":true}`
	if respond != nil {
		status, body = respond(method, form)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// callsTo returns the recorded calls to method, in order.
func (f *fakeTelegram) callsTo(method string) []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []apiCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// newFakeBot returns a Bot whose API talks to fake.
func newFakeBot(t *testing.T, fake *fakeTelegram) *Bot {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	api, err := bot.New("123:test", bot.WithServerURL(srv.URL), bot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("bot.New: %v", err)
	}
	return &Bot{api: api, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// apiError renders a Bot API error body.
func apiError(code int, desc string) (int, string) {
	b, _ := json.Marshal(map[string]any{"ok": false, "error_code": code, "description": desc})
	return code, string(b)
}

func TestSendDraft_UnsupportedErrorMarksChatSticky(t *testing.T) {
	const chatA, chatB = int64(1001), int64(1002)
	fake := &fakeTelegram{}
	b := newFakeBot(t, fake)
	ctx := context.Background()

	if !b.draftsSupported(chatA) || !b.draftsSupported(chatB) {
		t.Fatal("drafts must start supported for private chats")
	}

	// Transient failures leave the flag alone.
	fake.respond = func(string, map[string]string) (int, string) {
		return apiError(http.StatusTooManyRequests, "Too Many Requests: retry after 1")
	}
	if err := b.sendDraft(ctx, chatA, 0, 1, "hi"); err == nil {
		t.Fatal("429: want error")
	}
	if !b.draftsSupported(chatA) {
		t.Fatal("429 must not mark drafts unsupported")
	}
	fake.respond = func(string, map[string]string) (int, string) {
		return apiError(http.StatusInternalServerError, "Internal Server Error")
	}
	if err := b.sendDraft(ctx, chatA, 0, 1, "hi"); err == nil {
		t.Fatal("500: want error")
	}
	if !b.draftsSupported(chatA) {
		t.Fatal("5xx must not mark drafts unsupported")
	}

	// The Bot API refusing the method flips the chat, and only that chat.
	fake.respond = func(string, map[string]string) (int, string) {
		return apiError(http.StatusBadRequest, "Bad Request: method is not available")
	}
	if err := b.sendDraft(ctx, chatA, 0, 1, "hi"); err == nil {
		t.Fatal("400: want error")
	}
	if b.draftsSupported(chatA) {
		t.Fatal("400 must mark drafts unsupported for the chat")
	}
	if !b.draftsSupported(chatB) {
		t.Fatal("another chat must be unaffected")
	}

	// Sticky: a later success elsewhere does not clear it.
	fake.respond = nil
	if err := b.sendDraft(ctx, chatB, 0, 2, "hi"); err != nil {
		t.Fatalf("chat B draft: %v", err)
	}
	if b.draftsSupported(chatA) {
		t.Fatal("unsupported flag must be sticky")
	}

	calls := fake.callsTo("sendMessageDraft")
	last := calls[len(calls)-1]
	if last.Form["chat_id"] != "1002" || last.Form["draft_id"] != "2" || last.Form["text"] != "hi" {
		t.Errorf("draft params = %v", last.Form)
	}
	if _, ok := last.Form["parse_mode"]; ok {
		t.Errorf("partial drafts must be plain text, got parse_mode %q", last.Form["parse_mode"])
	}
}

func TestDraftsSupported_GroupChatsNever(t *testing.T) {
	b := newFakeBot(t, &fakeTelegram{})
	// sendMessageDraft targets private chats only; group ids are negative.
	if b.draftsSupported(-1001234) {
		t.Error("group chat must not use drafts")
	}
}
