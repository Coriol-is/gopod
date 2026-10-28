package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/store"
)

type fakeAPI struct {
	sends, edits, docs int
	lastSend           *bot.SendMessageParams
	lastEdit           *bot.EditMessageTextParams
	msg                *models.Message
	err                error
}

func (f *fakeAPI) SendMessage(_ context.Context, p *bot.SendMessageParams) (*models.Message, error) {
	f.sends++
	f.lastSend = p
	return f.msg, f.err
}

func (f *fakeAPI) EditMessageText(_ context.Context, p *bot.EditMessageTextParams) (*models.Message, error) {
	f.edits++
	f.lastEdit = p
	return f.msg, f.err
}

func (f *fakeAPI) SendDocument(context.Context, *bot.SendDocumentParams) (*models.Message, error) {
	f.docs++
	return f.msg, f.err
}

func textReq() SendRequest {
	return SendRequest{Kind: store.PartText, ChatID: 42, Thread: 7, Text: "hi", ParseMode: "HTML"}
}

func TestBotTransportMakesExactlyOneCallAndKeepsTheDestination(t *testing.T) {
	api := &fakeAPI{msg: &models.Message{ID: 99}}
	out := (&botTransport{api: api}).Attempt(t.Context(), textReq())
	if out.Kind != OutcomeAck || out.RemoteMessageID != 99 {
		t.Fatalf("outcome %+v", out)
	}
	if api.sends != 1 || api.lastSend.ChatID != int64(42) || api.lastSend.MessageThreadID != 7 || api.lastSend.Text != "hi" || api.lastSend.ParseMode != models.ParseModeHTML {
		t.Fatalf("calls %d params %+v", api.sends, api.lastSend)
	}
}

func TestBotTransportNeverRetriesOrFallsBackAfterAnError(t *testing.T) {
	for name, err := range map[string]error{
		"formatting": fmt.Errorf("%w, can't parse entities", bot.ErrorBadRequest),
		"topic gone": fmt.Errorf("%w, message thread not found", bot.ErrorBadRequest),
		"429":        &bot.TooManyRequestsError{Message: "slow", RetryAfter: 3},
		"timeout":    context.DeadlineExceeded,
	} {
		api := &fakeAPI{err: err}
		(&botTransport{api: api}).Attempt(t.Context(), textReq())
		if api.sends != 1 || api.edits != 0 || api.docs != 0 {
			t.Errorf("%s: sends=%d edits=%d docs=%d (hidden second API call)", name, api.sends, api.edits, api.docs)
		}
		if api.lastSend.MessageThreadID != 7 {
			t.Errorf("%s: left the stored topic", name)
		}
	}
}

func TestClassifySend(t *testing.T) {
	cases := []struct {
		name string
		msg  *models.Message
		err  error
		kind OutcomeKind
		code string
	}{
		{"429", nil, &bot.TooManyRequestsError{Message: "x", RetryAfter: 4}, OutcomeRateLimited, "rate_limited"},
		{"429 zero retry_after", nil, &bot.TooManyRequestsError{Message: "x"}, OutcomeRateLimited, "rate_limited"},
		{"forbidden", nil, fmt.Errorf("%w, bot was blocked", bot.ErrorForbidden), OutcomeRefused, "forbidden"},
		{"bad request", nil, fmt.Errorf("%w, can't parse entities", bot.ErrorBadRequest), OutcomeRefused, "bad_request"},
		{"thread gone", nil, fmt.Errorf("%w, Bad Request: message thread not found", bot.ErrorBadRequest), OutcomeRefused, "thread_gone"},
		{"credentials", nil, fmt.Errorf("%w, Unauthorized", bot.ErrorUnauthorized), OutcomeRefused, "denied_credentials"},
		{"migrated", nil, &bot.MigrateError{Message: "m", MigrateToChatID: 5}, OutcomeRefused, "chat_migrated"},
		{"dial", nil, fmt.Errorf("error do request: %w", &net.OpError{Op: "dial", Err: errors.New("refused")}), OutcomeNotSent, "connect_failed"},
		{"dns", nil, fmt.Errorf("error do request: %w", &net.DNSError{Err: "no such host"}), OutcomeNotSent, "connect_failed"},
		{"read after write", nil, fmt.Errorf("error do request: %w", &net.OpError{Op: "read", Err: errors.New("reset")}), OutcomeAmbiguous, "transport_error"},
		{"deadline", nil, fmt.Errorf("error do request: %w", context.DeadlineExceeded), OutcomeAmbiguous, "timeout"},
		{"5xx", nil, errors.New("error response from telegram for method sendMessage, 502 Bad Gateway"), OutcomeAmbiguous, "server_error"},
		{"undecodable", nil, errors.New("error decode response body for method sendMessage, <html>, invalid character"), OutcomeAmbiguous, "transport_error"},
		{"ok without message", nil, nil, OutcomeAmbiguous, "malformed_reply"},
		{"ok with zero id", &models.Message{}, nil, OutcomeAmbiguous, "malformed_reply"},
	}
	for _, c := range cases {
		got := classifySend(c.msg, c.err, 0)
		if got.Kind != c.kind || got.Code != c.code {
			t.Errorf("%s: got %+v want %v/%s", c.name, got, c.kind, c.code)
		}
	}
	if got := classifySend(nil, &bot.TooManyRequestsError{RetryAfter: 4}, 0); got.RetryAfter != 4*time.Second || got.HTTPStatus != 429 {
		t.Errorf("retry_after %+v", got)
	}
	if got := classifySend(nil, &bot.TooManyRequestsError{}, 0); got.RetryAfter != time.Second {
		t.Errorf("minimum retry_after %+v", got)
	}
	if got := parseTelegramStatus("error response from telegram for method sendMessage, 502 Bad Gateway"); got != 502 {
		t.Errorf("status %d", got)
	}
}

func TestUnknownErrorsAreAmbiguousByDefault(t *testing.T) {
	// The zero Outcome kind must be the safe one.
	if (Outcome{}).Kind != OutcomeAmbiguous {
		t.Fatal("zero outcome is not ambiguous")
	}
}

func TestBotTransportEditUsesTargetAndReportsIt(t *testing.T) {
	api := &fakeAPI{msg: &models.Message{ID: 55}}
	out := (&botTransport{api: api}).Attempt(t.Context(), SendRequest{Kind: store.PartEdit, ChatID: 42, Text: "new", TargetMessageID: 55})
	if out.Kind != OutcomeAck || out.RemoteMessageID != 55 || api.edits != 1 || api.sends != 0 || api.lastEdit.MessageID != 55 {
		t.Fatalf("outcome %+v api %+v", out, api)
	}
	if out := (&botTransport{api: api}).Attempt(t.Context(), SendRequest{Kind: store.PartEdit, ChatID: 42, Text: "x"}); out.Kind != OutcomeRefused || api.edits != 1 {
		t.Fatalf("edit without target: %+v", out)
	}
}

func TestBotTransportRefusesBeforeDispatch(t *testing.T) {
	api := &fakeAPI{msg: &models.Message{ID: 1}}
	tr := &botTransport{api: api}
	bad := textReq()
	bad.ReplyMarkup = `{"nope":1}`
	if out := tr.Attempt(t.Context(), bad); out.Kind != OutcomeRefused || out.Code != "invalid_markup" {
		t.Fatalf("bad markup: %+v", out)
	}
	up := SendRequest{Kind: store.PartUpload, ChatID: 42, ArtifactRef: "a"}
	if out := tr.Attempt(t.Context(), up); out.Kind != OutcomeRefused || out.Code != "upload_unavailable" {
		t.Fatalf("upload without opener: %+v", out)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if out := tr.Attempt(ctx, textReq()); out.Kind != OutcomeNotSent || out.Code != "canceled_before_dispatch" {
		t.Fatalf("canceled: %+v", out)
	}
	if api.sends+api.edits+api.docs != 0 {
		t.Fatal("an API call was made")
	}
	good := textReq()
	good.ReplyMarkup = `{"inline_keyboard":[[{"text":"a","callback_data":"b"}]]}`
	if out := tr.Attempt(t.Context(), good); out.Kind != OutcomeAck {
		t.Fatalf("valid markup: %+v", out)
	}
}

type fakeOpener struct {
	err    error
	closed *int
}

type closeCounter struct {
	io.Reader
	n *int
}

func (c closeCounter) Close() error { *c.n++; return nil }

func (o fakeOpener) OpenArtifact(context.Context, string) (string, io.Reader, error) {
	r := io.Reader(strings.NewReader("x"))
	if o.closed != nil {
		r = closeCounter{Reader: r, n: o.closed}
	}
	return "f.txt", r, o.err
}

func TestBotTransportClosesTheArtifactReaderOnEveryPath(t *testing.T) {
	up := SendRequest{Kind: store.PartUpload, ChatID: 42, ArtifactRef: "a"}
	for name, c := range map[string]struct {
		openErr, apiErr error
	}{"success": {}, "api error": {apiErr: context.DeadlineExceeded}, "open error": {openErr: errors.New("gone")}} {
		closed := 0
		api := &fakeAPI{msg: &models.Message{ID: 8}, err: c.apiErr}
		(&botTransport{api: api, artifacts: fakeOpener{err: c.openErr, closed: &closed}}).Attempt(t.Context(), up)
		if closed != 1 {
			t.Errorf("%s: reader closed %d times", name, closed)
		}
	}
}

func TestBotTransportUploadUsesOpenedArtifact(t *testing.T) {
	api := &fakeAPI{msg: &models.Message{ID: 8}}
	up := SendRequest{Kind: store.PartUpload, ChatID: 42, Thread: 3, ArtifactRef: "a"}
	if out := (&botTransport{api: api, artifacts: fakeOpener{}}).Attempt(t.Context(), up); out.Kind != OutcomeAck || api.docs != 1 {
		t.Fatalf("upload: %+v", out)
	}
	if out := (&botTransport{api: api, artifacts: fakeOpener{err: errors.New("gone")}}).Attempt(t.Context(), up); out.Kind != OutcomeRefused || api.docs != 1 {
		t.Fatalf("missing artifact: %+v", out)
	}
}

func TestOutcomeDiagnosticIsFixedFieldsOnly(t *testing.T) {
	d := Outcome{Kind: OutcomeRateLimited, Code: "rate_limited", HTTPStatus: 429, RetryAfter: 3 * time.Second}.diagnostic()
	if d != "class=rate_limited code=rate_limited http=429 retry_after=3" {
		t.Fatal(d)
	}
}
