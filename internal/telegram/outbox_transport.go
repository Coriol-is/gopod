package telegram

// Durable-outbox transport (CP-002 PR 2).
//
// A transport makes one attempt (one DB attempt row) for one immutable part
// and classifies the result. That is one API call, except an upload with the
// photo presentation, which makes at most two: a document of the same bytes
// only after Telegram proved the photo itself was refused (outbox_photo.go). It is deliberately separate from the interactive
// send helpers (sendFormattedErr, sendFinal, sendDocument): those retry, fall
// back to plain text after a formatting error, and redirect a gone topic to the
// main chat. None of that is allowed here, because every one of those moves
// can send a second message or reveal a conversation's reply in the wrong place.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/store"
)

// OutcomeKind is what one API attempt proved.
type OutcomeKind int

const (
	// OutcomeAmbiguous: the request may have been accepted (timeout, 5xx,
	// connection loss, malformed reply). Never retried; the part becomes uncertain.
	OutcomeAmbiguous OutcomeKind = iota
	// OutcomeAck: Telegram accepted the part and returned a remote message id.
	OutcomeAck
	// OutcomeRateLimited: an explicit 429; known not accepted, retry_after applies.
	OutcomeRateLimited
	// OutcomeNotSent: proven pre-dispatch failure; nothing reached the server.
	OutcomeNotSent
	// OutcomeRefused: an explicit terminal refusal (or a deterministic local
	// failure before dispatch). Never retried, never rerouted.
	OutcomeRefused
)

func (k OutcomeKind) String() string {
	switch k {
	case OutcomeAck:
		return "ack"
	case OutcomeRateLimited:
		return "rate_limited"
	case OutcomeNotSent:
		return "not_sent"
	case OutcomeRefused:
		return "refused"
	}
	return "ambiguous"
}

// Outcome is the sanitized result of one attempt. Code is a fixed machine code,
// never a raw error text: the dispatcher persists it as is.
type Outcome struct {
	Kind            OutcomeKind
	RemoteMessageID int64
	RetryAfter      time.Duration
	Code            string
	HTTPStatus      int
}

// SendRequest is the immutable, fully rendered operation of one part. ChatID
// and Thread are the stored conversation destination; there is no field through
// which a transport could pick another topic.
type SendRequest struct {
	Kind            store.PartKind
	ChatID          int64
	Thread          int
	Text            string
	ParseMode       string
	ReplyMarkup     string // JSON of an inline keyboard, as frozen at publication
	TargetMessageID int64
	ArtifactRef     string
	// PartID and AttemptID identify the stored attempt; they are used only for
	// logging a second API call.
	PartID    string
	AttemptID int64
	// NamespaceID and NamespaceEpoch are the delivery's scope; an artifact of
	// any other namespace or epoch is never uploaded.
	NamespaceID    string
	NamespaceEpoch int64
}

// outboxAPI is the subset of *bot.Bot the real transport calls.
type outboxAPI interface {
	SendMessage(ctx context.Context, p *bot.SendMessageParams) (*models.Message, error)
	EditMessageText(ctx context.Context, p *bot.EditMessageTextParams) (*models.Message, error)
	SendDocument(ctx context.Context, p *bot.SendDocumentParams) (*models.Message, error)
	SendVoice(ctx context.Context, p *bot.SendVoiceParams) (*models.Message, error)
	SendPhoto(ctx context.Context, p *bot.SendPhotoParams) (*models.Message, error)
}

// ArtifactOpener resolves an immutable artifact reference to the exact bytes to
// upload. The bytes are verified against the inventory before they are returned,
// so the transport never reopens a mutable pathname. Without an opener uploads
// are refused (artifact delivery is off when GOPOD_OUTBOX_ARTIFACTS=0).
type ArtifactOpener interface {
	OpenArtifact(ctx context.Context, ref, namespaceID string, epoch int64) (name string, data io.Reader, err error)
}

// botTransport is the Telegram implementation.
type botTransport struct {
	api       outboxAPI
	artifacts ArtifactOpener
	log       *slog.Logger // optional; nil logs nothing
}

// NewOutboxTransport returns the single-attempt transport over the bot API.
func (b *Bot) NewOutboxTransport(artifacts ArtifactOpener) *botTransport {
	return &botTransport{api: b.api, artifacts: artifacts, log: b.log}
}

func refused(code string) Outcome { return Outcome{Kind: OutcomeRefused, Code: code} }

// Attempt performs one API call. It never retries and never falls back.
func (t *botTransport) Attempt(ctx context.Context, req SendRequest) Outcome {
	if ctx.Err() != nil {
		return Outcome{Kind: OutcomeNotSent, Code: "canceled_before_dispatch"}
	}
	markup, err := parseOutboxMarkup(req.ReplyMarkup)
	if err != nil {
		return refused("invalid_markup")
	}
	switch req.Kind {
	case store.PartText:
		msg, err := t.api.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: req.ChatID, MessageThreadID: req.Thread, Text: req.Text,
			ParseMode: models.ParseMode(req.ParseMode), ReplyMarkup: markup,
		})
		return classifySend(msg, err, 0)
	case store.PartEdit:
		if req.TargetMessageID <= 0 || req.TargetMessageID > int64(^uint32(0)>>1) {
			return refused("invalid_target")
		}
		msg, err := t.api.EditMessageText(ctx, &bot.EditMessageTextParams{
			ChatID: req.ChatID, MessageID: int(req.TargetMessageID), Text: req.Text,
			ParseMode: models.ParseMode(req.ParseMode), ReplyMarkup: markup,
		})
		return classifySend(msg, err, req.TargetMessageID)
	case store.PartUpload:
		if t.artifacts == nil {
			return refused("upload_unavailable")
		}
		switch req.ParseMode {
		case "", store.UploadPresentationVoice, store.UploadPresentationPhoto:
		default:
			return refused("invalid_presentation")
		}
		name, data, err := t.artifacts.OpenArtifact(ctx, req.ArtifactRef, req.NamespaceID, req.NamespaceEpoch)
		if c, ok := data.(io.Closer); ok && c != nil {
			defer c.Close()
		}
		if err != nil {
			return refused("artifact_unavailable")
		}
		if req.ParseMode == store.UploadPresentationPhoto {
			return t.attemptPhoto(ctx, req, name, data)
		}
		if req.ParseMode == store.UploadPresentationVoice {
			// Host-generated audio only (the V1 agent request cannot set this).
			msg, err := t.api.SendVoice(ctx, &bot.SendVoiceParams{
				ChatID: req.ChatID, MessageThreadID: req.Thread,
				Voice:   &models.InputFileUpload{Filename: name, Data: data},
				Caption: req.Text,
			})
			return classifySend(msg, err, 0)
		}
		msg, err := t.api.SendDocument(ctx, &bot.SendDocumentParams{
			ChatID: req.ChatID, MessageThreadID: req.Thread,
			Document: &models.InputFileUpload{Filename: name, Data: data},
			Caption:  req.Text, // plain text; an upload has no parse mode
		})
		return classifySend(msg, err, 0)
	}
	return refused("unsupported_kind")
}

// parseOutboxMarkup decodes the frozen inline keyboard. An empty string means none.
func parseOutboxMarkup(raw string) (models.ReplyMarkup, error) {
	if raw == "" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var m models.InlineKeyboardMarkup
	if err := dec.Decode(&m); err != nil || len(m.InlineKeyboard) == 0 {
		return nil, errors.New("invalid inline keyboard")
	}
	return &m, nil
}

// classifySend maps one API result to an Outcome. wantID, when non-zero, is the
// id an edit must report (Telegram returns the edited message).
func classifySend(msg *models.Message, err error, wantID int64) Outcome {
	if err == nil {
		switch {
		case wantID != 0:
			return Outcome{Kind: OutcomeAck, RemoteMessageID: wantID}
		case msg != nil && msg.ID > 0:
			return Outcome{Kind: OutcomeAck, RemoteMessageID: int64(msg.ID)}
		}
		// ok:true without a usable message id: the request was accepted but the
		// reply cannot be trusted, so it must not become a clean ack or a retry.
		return Outcome{Kind: OutcomeAmbiguous, Code: "malformed_reply"}
	}
	var tm *bot.TooManyRequestsError
	var mig *bot.MigrateError
	var op *net.OpError
	var dns *net.DNSError
	switch {
	case errors.As(err, &tm):
		return Outcome{Kind: OutcomeRateLimited, RetryAfter: time.Duration(max(tm.RetryAfter, 1)) * time.Second, Code: "rate_limited", HTTPStatus: 429}
	case errors.As(err, &mig):
		return Outcome{Kind: OutcomeRefused, Code: "chat_migrated", HTTPStatus: 400}
	case errors.Is(err, bot.ErrorForbidden):
		return Outcome{Kind: OutcomeRefused, Code: "forbidden", HTTPStatus: 403}
	case errors.Is(err, bot.ErrorBadRequest):
		if isThreadGone(err) {
			return Outcome{Kind: OutcomeRefused, Code: "thread_gone", HTTPStatus: 400}
		}
		if strings.Contains(strings.ToLower(err.Error()), "can't parse entities") {
			return Outcome{Kind: OutcomeRefused, Code: "parse_entities", HTTPStatus: 400}
		}
		return Outcome{Kind: OutcomeRefused, Code: "bad_request", HTTPStatus: 400}
	case errors.Is(err, bot.ErrorUnauthorized):
		return Outcome{Kind: OutcomeRefused, Code: "denied_credentials", HTTPStatus: 401}
	case errors.Is(err, bot.ErrorNotFound):
		return Outcome{Kind: OutcomeRefused, Code: "not_found", HTTPStatus: 404}
	case errors.Is(err, bot.ErrorConflict):
		return Outcome{Kind: OutcomeRefused, Code: "conflict", HTTPStatus: 409}
	case errors.As(err, &dns), errors.As(err, &op) && op.Op == "dial":
		// The connection was never established, so no request bytes were sent.
		return Outcome{Kind: OutcomeNotSent, Code: "connect_failed"}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return Outcome{Kind: OutcomeAmbiguous, Code: "timeout"}
	case strings.Contains(err.Error(), "error response from telegram"):
		// A status the library does not map; 5xx is not proof of non-delivery.
		return Outcome{Kind: OutcomeAmbiguous, Code: "server_error", HTTPStatus: parseTelegramStatus(err.Error())}
	}
	return Outcome{Kind: OutcomeAmbiguous, Code: "transport_error"}
}

// parseTelegramStatus extracts the numeric status from the library's
// "error response from telegram for method M, 502 text" message; 0 if absent.
func parseTelegramStatus(msg string) int {
	_, rest, ok := strings.Cut(msg, "error response from telegram for method ")
	if !ok {
		return 0
	}
	_, rest, ok = strings.Cut(rest, ", ")
	if !ok {
		return 0
	}
	field, _, _ := strings.Cut(rest, " ")
	n, err := strconv.Atoi(field)
	if err != nil || n < 100 || n > 599 {
		return 0
	}
	return n
}

// diagnostic renders the fixed-field diagnostic persisted with an attempt.
func (o Outcome) diagnostic() string {
	s := "class=" + o.Kind.String()
	if o.Code != "" {
		s += " code=" + o.Code
	}
	if o.HTTPStatus != 0 {
		s += fmt.Sprintf(" http=%d", o.HTTPStatus)
	}
	if o.RetryAfter > 0 {
		s += fmt.Sprintf(" retry_after=%d", int64(o.RetryAfter/time.Second))
	}
	return s
}
