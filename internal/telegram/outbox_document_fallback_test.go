package telegram

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (f *outboxFixture) publishPhoto(data []byte) store.Delivery {
	f.t.Helper()
	ctx := f.t.Context()
	prep, err := f.st.ReserveArtifacts(ctx, store.ArtifactReservation{Scope: f.conv.Scope, Files: 1, Bytes: int64(len(data))})
	if err != nil {
		f.t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	if err = f.st.MarkArtifactsReady(ctx, f.conv.Scope, prep.ID, []store.ArtifactFacts{{ArtifactID: prep.ArtifactIDs[0], SHA256: hash, Size: int64(len(data)), MediaType: "image/png", Filename: "chart.png"}}); err != nil {
		f.t.Fatal(err)
	}
	d, err := f.st.PublishDeliveryWithArtifacts(ctx, store.PublishRequest{Conversation: f.conv, Owner: store.OwnerLegacyAgentIPC, Parts: []store.DeliveryPart{{Kind: store.PartUpload, ArtifactRef: store.ArtifactRef(prep.ArtifactIDs[0]), ParseMode: store.UploadPresentationPhoto, Payload: []byte("exact caption"), ReplyToMessageID: 17}}}, prep.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}
func stepDispatcher(t *testing.T, backend outboxBackend, tr OutboxTransport) *OutboxDispatcher {
	t.Helper()
	return NewOutboxDispatcher(backend, tr, fastCfg(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type checkedDocumentAPI struct {
	fakeAPI
	beforeDocument func()
}

func (a *checkedDocumentAPI) SendDocument(ctx context.Context, p *bot.SendDocumentParams) (*models.Message, error) {
	a.beforeDocument()
	return a.fakeAPI.SendDocument(ctx, p)
}
func TestDocumentFallbackPersistsSelectionBeforeSecondHTTP(t *testing.T) {
	f := newOutboxFixture(t)
	var err error
	f.conv, err = f.st.SnapshotConversation(t.Context(), "tg:1", 7, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	data := realPNG(t, 40, 40)
	delivery := f.publishPhoto(data)
	api := &checkedDocumentAPI{fakeAPI: fakeAPI{msg: &models.Message{ID: 99}, photoErr: fmt.Errorf("%w, Bad Request: PHOTO_INVALID_DIMENSIONS", bot.ErrorBadRequest)}}
	api.beforeDocument = func() {
		var n int
		if err := f.st.DB().QueryRow(`SELECT count(*) FROM outbox_document_fallbacks b JOIN outbox_attempts r ON r.attempt_id=b.refused_attempt_id JOIN outbox_attempts d ON d.attempt_id=b.first_document_attempt_id WHERE r.outcome='refused' AND r.ended_at IS NOT NULL AND d.ended_at IS NULL`).Scan(&n); err != nil || n != 1 {
			t.Errorf("HTTP2 before committed branch: %d %v", n, err)
		}
	}
	d := stepDispatcher(t, f.owner, photoTransport(api, data))
	if ok, err := d.step(t.Context(), t.Context()); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if api.photos != 1 || api.docs != 1 || !bytes.Equal(api.photoBytes, api.docBytes) || !bytes.Equal(api.docBytes, data) {
		t.Fatal("wrong calls or bytes")
	}
	p, q := api.lastPhoto, api.lastDoc
	if p.ChatID != q.ChatID || p.MessageThreadID != q.MessageThreadID || p.Caption != q.Caption || q.Caption != "exact caption" || q.MessageThreadID != 7 || p.ReplyParameters.MessageID != q.ReplyParameters.MessageID || q.ReplyParameters.MessageID != 17 {
		t.Fatalf("route drift: %+v %+v", p, q)
	}
	diag := f.diag(delivery.ID)
	if diag.State != store.DeliverySent || diag.Parts[0].Attempts != 2 || diag.DocumentFallbacks != 1 {
		t.Fatal(diag)
	}
}
func TestDocumentFallbackDispatcherOutcomeMatrix(t *testing.T) {
	cases := []struct {
		name          string
		first, second Outcome
		calls         int
		state         string
	}{
		{"ack", ack(1), ack(2), 1, store.DeliverySent},
		{"known refusal", Outcome{Kind: OutcomeRefused, Code: "photo_refused", HTTPStatus: 400}, ack(2), 2, store.DeliverySent},
		{"ambiguous photo", Outcome{Kind: OutcomeAmbiguous, Code: "timeout"}, ack(2), 1, store.DeliveryUncertain},
		{"wrong status", Outcome{Kind: OutcomeRefused, Code: "photo_refused", HTTPStatus: 403}, ack(2), 1, store.DeliveryFailed},
		{"wrong code", Outcome{Kind: OutcomeRefused, Code: "bad_request", HTTPStatus: 400}, ack(2), 1, store.DeliveryFailed},
		{"ambiguous document", Outcome{Kind: OutcomeRefused, Code: "photo_refused", HTTPStatus: 400}, Outcome{Kind: OutcomeAmbiguous, Code: "timeout"}, 2, store.DeliveryUncertain},
		{"refused document", Outcome{Kind: OutcomeRefused, Code: "photo_refused", HTTPStatus: 400}, Outcome{Kind: OutcomeRefused, Code: "bad_request", HTTPStatus: 400}, 2, store.DeliveryFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newOutboxFixture(t)
			delivery := f.publishPhoto(realPNG(t, 10, 10))
			tr := &fakeTransport{script: func(n int, r SendRequest) Outcome {
				if n == 1 {
					return c.first
				}
				return c.second
			}}
			d := stepDispatcher(t, f.owner, tr)
			if _, err := d.step(t.Context(), t.Context()); err != nil {
				t.Fatal(err)
			}
			diag := f.diag(delivery.ID)
			if diag.State != c.state || tr.callCount() != c.calls {
				t.Fatal(diag, tr.callCount())
			}
			if c.calls == 2 {
				a, b := tr.calls[0], tr.calls[1]
				if a.AttemptID == b.AttemptID || a.ParseMode != store.UploadPresentationPhoto || b.ParseMode != "" || a.ArtifactRef != b.ArtifactRef || a.PartID != b.PartID || a.Text != b.Text {
					t.Fatal("incorrect alternative requests")
				}
			}
			if ok, err := d.step(t.Context(), t.Context()); ok || err != nil {
				t.Fatal("replayed terminal result", ok, err)
			}
		})
	}
}
func TestDocumentFallbackKnownUnsentRetryNeverReturnsToPhoto(t *testing.T) {
	for _, out := range []Outcome{{Kind: OutcomeRateLimited, Code: "rate_limited", RetryAfter: time.Second}, {Kind: OutcomeNotSent, Code: "connect_failed"}} {
		t.Run(out.Kind.String(), func(t *testing.T) {
			f := newOutboxFixture(t)
			delivery := f.publishPhoto(realPNG(t, 10, 10))
			tr := &fakeTransport{script: func(n int, r SendRequest) Outcome {
				if n == 1 {
					return Outcome{Kind: OutcomeRefused, Code: "photo_refused", HTTPStatus: 400}
				}
				if n == 2 {
					return out
				}
				return ack(n)
			}}
			d := stepDispatcher(t, f.owner, tr)
			now := time.Now()
			d.cfg.Now = func() time.Time { return now }
			if _, err := d.step(t.Context(), t.Context()); err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Minute)
			if _, err := d.step(t.Context(), t.Context()); err != nil {
				t.Fatal(err)
			}
			if tr.callCount() != 3 || tr.calls[1].ParseMode != "" || tr.calls[2].ParseMode != "" || tr.calls[1].AttemptID == tr.calls[2].AttemptID {
				t.Fatal(tr.calls)
			}
			if diag := f.diag(delivery.ID); diag.State != store.DeliverySent || diag.Parts[0].Attempts != 3 {
				t.Fatal(diag)
			}
		})
	}
}

type documentBranchFailure struct {
	outboxBackend
	owner  *store.OutboxOwner
	commit bool
}

func (b documentBranchFailure) BeginDocumentFallback(ctx context.Context, c store.OutboxClaim) (store.OutboxClaim, error) {
	if b.commit {
		if _, err := b.owner.BeginDocumentFallback(ctx, c); err != nil {
			return store.OutboxClaim{}, err
		}
	}
	return store.OutboxClaim{}, errors.New("injected commit acknowledgement failure")
}
func TestDocumentFallbackUnknownSelectionNeverMakesSecondCall(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprint(commit), func(t *testing.T) {
			f := newOutboxFixture(t)
			delivery := f.publishPhoto(realPNG(t, 10, 10))
			tr := &fakeTransport{script: func(int, SendRequest) Outcome {
				return Outcome{Kind: OutcomeRefused, Code: "photo_refused", HTTPStatus: 400}
			}}
			d := stepDispatcher(t, documentBranchFailure{outboxBackend: f.owner, owner: f.owner, commit: commit}, tr)
			if _, err := d.step(t.Context(), t.Context()); err != nil {
				t.Fatal(err)
			}
			if tr.callCount() != 1 {
				t.Fatal("second HTTP after unknown selection")
			}
			if _, err := f.owner.SweepSending(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := store.DeliveryFailed
			if commit {
				want = store.DeliveryUncertain
			}
			if diag := f.diag(delivery.ID); diag.State != want {
				t.Fatal(diag)
			}
			if ok, err := d.step(t.Context(), t.Context()); ok || err != nil {
				t.Fatal("replayed unknown selection", ok, err)
			}
		})
	}
}

type cancelAfterDocumentSelection struct {
	*store.OutboxOwner
	cancel context.CancelFunc
}

func (b cancelAfterDocumentSelection) BeginDocumentFallback(ctx context.Context, c store.OutboxClaim) (store.OutboxClaim, error) {
	next, err := b.OutboxOwner.BeginDocumentFallback(ctx, c)
	b.cancel()
	return next, err
}
func TestDocumentFallbackCancellationAfterSelectionResumesDocumentOnly(t *testing.T) {
	f := newOutboxFixture(t)
	delivery := f.publishPhoto(realPNG(t, 10, 10))
	attemptCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tr := &fakeTransport{script: func(n int, req SendRequest) Outcome {
		if n == 1 {
			return Outcome{Kind: OutcomeRefused, Code: "photo_refused", HTTPStatus: 400}
		}
		return ack(n)
	}}
	d := stepDispatcher(t, cancelAfterDocumentSelection{f.owner, cancel}, tr)
	now := time.Now()
	d.cfg.Now = func() time.Time { return now }
	if _, err := d.step(t.Context(), attemptCtx); err != nil {
		t.Fatal(err)
	}
	if tr.callCount() != 1 {
		t.Fatal("HTTP2 despite canceled context")
	}
	now = now.Add(time.Minute)
	if _, err := d.step(t.Context(), t.Context()); err != nil {
		t.Fatal(err)
	}
	if tr.callCount() != 2 || tr.calls[1].ParseMode != "" {
		t.Fatal(tr.calls)
	}
	if diag := f.diag(delivery.ID); diag.State != store.DeliverySent {
		t.Fatal(diag)
	}
}

type documentAckFailure struct{ *store.OutboxOwner }

func (b documentAckFailure) AckPart(context.Context, store.OutboxClaim, int64) error {
	return errors.New("injected ack write failure")
}
func TestDocumentFallbackAcceptedButUnrecordedNeverReplays(t *testing.T) {
	f := newOutboxFixture(t)
	delivery := f.publishPhoto(realPNG(t, 10, 10))
	tr := &fakeTransport{script: func(n int, req SendRequest) Outcome {
		if n == 1 {
			return Outcome{Kind: OutcomeRefused, Code: "photo_refused", HTTPStatus: 400}
		}
		return ack(n)
	}}
	d := stepDispatcher(t, documentAckFailure{f.owner}, tr)
	if _, err := d.step(t.Context(), t.Context()); err != nil {
		t.Fatal(err)
	}
	if diag := f.diag(delivery.ID); diag.State != store.DeliveryUncertain {
		t.Fatal(diag)
	}
	if ok, err := d.step(t.Context(), t.Context()); ok || err != nil || tr.callCount() != 2 {
		t.Fatal("replayed unrecorded acceptance", ok, err, tr.callCount())
	}
}
