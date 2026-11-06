package telegram

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const testReplyKeyboard = `{"inline_keyboard":[[{"text":"more","callback_data":"mo:1"}]]}`

func publishKeyboard(t *testing.T, f *outboxFixture, kind store.PartKind) store.Delivery {
	t.Helper()
	p := store.DeliveryPart{Kind: kind, Payload: []byte("<b>answer</b>"), ParseMode: "HTML", ReplyMarkup: testReplyKeyboard, ReplyToMessageID: 17}
	if kind == store.PartEdit {
		p.TargetMessageID = 99
		p.ReplyToMessageID = 0
	}
	d, err := f.st.PublishDelivery(t.Context(), store.PublishRequest{Conversation: f.conv, Owner: store.OwnerControllerResult, Parts: []store.DeliveryPart{p}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func keyboardRejected() Outcome {
	return Outcome{Kind: OutcomeRefused, Code: "keyboard_refused", HTTPStatus: 400}
}
func parseRejected() Outcome {
	return Outcome{Kind: OutcomeRefused, Code: "parse_entities", HTTPStatus: 400}
}

func TestKeyboardFallbackCompositionAndPersistedAttempts(t *testing.T) {
	for _, kind := range []store.PartKind{store.PartText, store.PartEdit} {
		for _, order := range []string{"keyboard", "keyboard then parse", "parse then keyboard"} {
			t.Run(string(kind)+"/"+order, func(t *testing.T) {
				f := newOutboxFixture(t)
				var err error
				f.conv, err = f.st.SnapshotConversation(t.Context(), "tg:1", 7, store.VisibilityConversation)
				if err != nil {
					t.Fatal(err)
				}
				delivery := publishKeyboard(t, f, kind)
				tr := &fakeTransport{script: func(n int, r SendRequest) Outcome {
					var open int
					if err := f.st.DB().QueryRow(`SELECT count(*) FROM outbox_attempts WHERE attempt_id=? AND part_id=? AND ended_at IS NULL`, r.AttemptID, r.PartID).Scan(&open); err != nil || open != 1 {
						t.Errorf("HTTP without its own committed attempt: %d %v", open, err)
					}
					if n == 1 {
						if order == "parse then keyboard" {
							return parseRejected()
						}
						return keyboardRejected()
					}
					if n == 2 && order != "keyboard" {
						if order == "parse then keyboard" {
							return keyboardRejected()
						}
						return parseRejected()
					}
					return ack(99)
				}}
				d := stepDispatcher(t, f.owner, tr)
				if _, err := d.step(t.Context(), t.Context()); err != nil {
					t.Fatal(err)
				}
				wantCalls := 3
				if order == "keyboard" {
					wantCalls = 2
				}
				if tr.callCount() != wantCalls {
					t.Fatal(tr.calls)
				}
				last := tr.calls[len(tr.calls)-1]
				if last.ReplyMarkup != bareOutboxKeyboard || last.ChatID != 1 || last.Thread != 7 || last.Kind != kind {
					t.Fatal(last)
				}
				if kind == store.PartEdit && last.TargetMessageID != 99 || kind == store.PartText && last.ReplyToMessageID != 17 {
					t.Fatal("target changed", last)
				}
				if order == "keyboard" {
					if last.Text != "<b>answer</b>" || last.ParseMode != "HTML" {
						t.Fatal(last)
					}
				} else if last.Text != "answer" || last.ParseMode != "" {
					t.Fatal(last)
				}
				diag := f.diag(delivery.ID)
				if diag.State != store.DeliverySent || diag.KeyboardFallbacks != 1 || diag.Parts[0].Attempts != wantCalls {
					t.Fatal(diag)
				}
				stored, err := f.st.GetDelivery(t.Context(), delivery.ID)
				if err != nil || stored.Parts[0].Part.ReplyMarkup != testReplyKeyboard || string(stored.Parts[0].Part.Payload) != "<b>answer</b>" {
					t.Fatal("immutable plan changed", err)
				}
			})
		}
	}
}

func TestKeyboardFallbackConservativeOutcomes(t *testing.T) {
	for _, c := range []struct {
		name          string
		first, second Outcome
		calls         int
		state         string
	}{
		{"ack", ack(1), ack(2), 1, store.DeliverySent},
		{"timeout", Outcome{Kind: OutcomeAmbiguous, Code: "timeout"}, ack(2), 1, store.DeliveryUncertain},
		{"wrong status", Outcome{Kind: OutcomeRefused, Code: "keyboard_refused", HTTPStatus: 403}, ack(2), 1, store.DeliveryFailed},
		{"generic refusal", Outcome{Kind: OutcomeRefused, Code: "bad_request", HTTPStatus: 400}, ack(2), 1, store.DeliveryFailed},
		{"bare timeout", keyboardRejected(), Outcome{Kind: OutcomeAmbiguous, Code: "timeout"}, 2, store.DeliveryUncertain},
		{"bare refused", keyboardRejected(), keyboardRejected(), 2, store.DeliveryFailed},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newOutboxFixture(t)
			delivery := publishKeyboard(t, f, store.PartText)
			tr := &fakeTransport{script: func(n int, _ SendRequest) Outcome {
				if n == 1 {
					return c.first
				}
				return c.second
			}}
			d := stepDispatcher(t, f.owner, tr)
			if _, err := d.step(t.Context(), t.Context()); err != nil {
				t.Fatal(err)
			}
			if diag := f.diag(delivery.ID); diag.State != c.state || tr.callCount() != c.calls {
				t.Fatal(diag, tr.callCount())
			}
			if ok, err := d.step(t.Context(), t.Context()); ok || err != nil {
				t.Fatal("terminal delivery replayed", ok, err)
			}
		})
	}
}

func TestKeyboardFallbackKnownUnsentKeepsBareSelection(t *testing.T) {
	for _, out := range []Outcome{{Kind: OutcomeRateLimited, Code: "rate_limited", RetryAfter: time.Second}, {Kind: OutcomeNotSent, Code: "connect_failed"}} {
		t.Run(out.Code, func(t *testing.T) {
			f := newOutboxFixture(t)
			delivery := publishKeyboard(t, f, store.PartText)
			tr := &fakeTransport{script: func(n int, _ SendRequest) Outcome {
				switch n {
				case 1:
					return keyboardRejected()
				case 2:
					return out
				default:
					return ack(99)
				}
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
			if tr.callCount() != 3 || tr.calls[2].ReplyMarkup != bareOutboxKeyboard || tr.calls[1].AttemptID == tr.calls[2].AttemptID {
				t.Fatal(tr.calls)
			}
			if diag := f.diag(delivery.ID); diag.State != store.DeliverySent {
				t.Fatal(diag)
			}
		})
	}
}

type keyboardBranchFailure struct {
	outboxBackend
	owner  *store.OutboxOwner
	commit bool
}

func (b keyboardBranchFailure) BeginKeyboardFallback(ctx context.Context, c store.OutboxClaim) (store.OutboxClaim, error) {
	if b.commit {
		if _, err := b.owner.BeginKeyboardFallback(ctx, c); err != nil {
			return store.OutboxClaim{}, err
		}
	}
	return store.OutboxClaim{}, errors.New("injected commit acknowledgement loss")
}
func TestKeyboardFallbackUnknownCommitDoesNotSend(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprint(commit), func(t *testing.T) {
			f := newOutboxFixture(t)
			delivery := publishKeyboard(t, f, store.PartText)
			tr := &fakeTransport{script: func(int, SendRequest) Outcome { return keyboardRejected() }}
			d := stepDispatcher(t, keyboardBranchFailure{f.owner, f.owner, commit}, tr)
			if _, err := d.step(t.Context(), t.Context()); err != nil {
				t.Fatal(err)
			}
			if tr.callCount() != 1 {
				t.Fatal("second call after unknown transition")
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
		})
	}
}

func TestKeyboardRefusalRequiresExplicitBadRequest(t *testing.T) {
	for _, err := range []error{fmt.Errorf("%w: BUTTON_DATA_INVALID", bot.ErrorBadRequest), fmt.Errorf("%w: inline keyboard invalid", bot.ErrorBadRequest), errors.New("BUTTON_DATA_INVALID"), fmt.Errorf("%w: BUTTON_DATA_INVALID", bot.ErrorForbidden)} {
		out := classifySend(nil, err, 0)
		want := errors.Is(err, bot.ErrorBadRequest)
		if (out.Code == "keyboard_refused") != want {
			t.Fatal(err, out)
		}
	}
}

// Exercise real transport parsing and API parameters, not just request structs.
type keyboardAPI struct {
	fakeAPI
	t     *testing.T
	f     *outboxFixture
	calls int
}

func (a *keyboardAPI) check(markup models.ReplyMarkup) (*models.Message, error) {
	a.calls++
	if a.calls == 1 {
		return nil, fmt.Errorf("%w: BUTTON_DATA_INVALID", bot.ErrorBadRequest)
	}
	kb, ok := markup.(*models.InlineKeyboardMarkup)
	if !ok || kb == nil || kb.InlineKeyboard == nil || len(kb.InlineKeyboard) != 0 {
		a.t.Errorf("fallback must explicitly clear keyboard: %#v", markup)
	}
	var committed int
	err := a.f.st.DB().QueryRow(`SELECT count(*) FROM outbox_keyboard_fallbacks k JOIN outbox_attempts r ON r.attempt_id=k.refused_attempt_id JOIN outbox_attempts b ON b.attempt_id=k.first_bare_attempt_id WHERE r.outcome='refused' AND r.ended_at IS NOT NULL AND b.ended_at IS NULL`).Scan(&committed)
	if err != nil || committed != 1 {
		a.t.Errorf("second HTTP without committed branch: %d %v", committed, err)
	}
	return &models.Message{ID: 99}, nil
}
func (a *keyboardAPI) SendMessage(_ context.Context, p *bot.SendMessageParams) (*models.Message, error) {
	return a.check(p.ReplyMarkup)
}
func (a *keyboardAPI) EditMessageText(_ context.Context, p *bot.EditMessageTextParams) (*models.Message, error) {
	return a.check(p.ReplyMarkup)
}
func TestKeyboardFallbackRealTransportClearsMarkup(t *testing.T) {
	for _, kind := range []store.PartKind{store.PartText, store.PartEdit} {
		t.Run(string(kind), func(t *testing.T) {
			f := newOutboxFixture(t)
			delivery := publishKeyboard(t, f, kind)
			api := &keyboardAPI{t: t, f: f}
			d := stepDispatcher(t, f.owner, &botTransport{api: api})
			if _, err := d.step(t.Context(), t.Context()); err != nil {
				t.Fatal(err)
			}
			if diag := f.diag(delivery.ID); diag.State != store.DeliverySent || api.calls != 2 {
				t.Fatal(diag, api.calls)
			}
		})
	}
}

type cancelAfterKeyboardSelection struct {
	*store.OutboxOwner
	cancel context.CancelFunc
}

func (b cancelAfterKeyboardSelection) BeginKeyboardFallback(ctx context.Context, c store.OutboxClaim) (store.OutboxClaim, error) {
	next, err := b.OutboxOwner.BeginKeyboardFallback(ctx, c)
	b.cancel()
	return next, err
}
func TestKeyboardFallbackCancellationAfterSelectionResumesBare(t *testing.T) {
	f := newOutboxFixture(t)
	delivery := publishKeyboard(t, f, store.PartText)
	attemptCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tr := &fakeTransport{script: func(n int, _ SendRequest) Outcome {
		if n == 1 {
			return keyboardRejected()
		}
		return ack(99)
	}}
	d := stepDispatcher(t, cancelAfterKeyboardSelection{f.owner, cancel}, tr)
	now := time.Now()
	d.cfg.Now = func() time.Time { return now }
	if _, err := d.step(t.Context(), attemptCtx); err != nil {
		t.Fatal(err)
	}
	if tr.callCount() != 1 {
		t.Fatal("HTTP after canceled selection")
	}
	now = now.Add(time.Minute)
	if _, err := d.step(t.Context(), t.Context()); err != nil {
		t.Fatal(err)
	}
	if tr.callCount() != 2 || tr.calls[1].ReplyMarkup != bareOutboxKeyboard {
		t.Fatal(tr.calls)
	}
	if diag := f.diag(delivery.ID); diag.State != store.DeliverySent {
		t.Fatal(diag)
	}
}

type keyboardAckFailure struct{ *store.OutboxOwner }

func (b keyboardAckFailure) AckPart(context.Context, store.OutboxClaim, int64) error {
	return errors.New("injected ACK persistence failure")
}
func TestKeyboardFallbackAcceptedButUnrecordedNeverReplays(t *testing.T) {
	f := newOutboxFixture(t)
	delivery := publishKeyboard(t, f, store.PartText)
	tr := &fakeTransport{script: func(n int, _ SendRequest) Outcome {
		if n == 1 {
			return keyboardRejected()
		}
		return ack(99)
	}}
	d := stepDispatcher(t, keyboardAckFailure{f.owner}, tr)
	if _, err := d.step(t.Context(), t.Context()); err != nil {
		t.Fatal(err)
	}
	if diag := f.diag(delivery.ID); diag.State != store.DeliveryUncertain {
		t.Fatal(diag)
	}
	if ok, err := d.step(t.Context(), t.Context()); ok || err != nil || tr.callCount() != 2 {
		t.Fatal("replayed accepted result", ok, err, tr.callCount())
	}
}

func TestPlainFallbackEditGuardFailurePreservesPendingBudget(t *testing.T) {
	for _, cause := range []error{errors.New("database unavailable"), context.Canceled, store.ErrTelegramDeleteHeld} {
		t.Run(cause.Error(), func(t *testing.T) {
			f := newOutboxFixture(t)
			delivery := publishKeyboard(t, f, store.PartEdit)
			c, ok, err := f.owner.ClaimNext(t.Context(), time.Now())
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			c, err = f.owner.BeginPlainFallback(t.Context(), c)
			if err != nil {
				t.Fatal(err)
			}
			api := &fakeAPI{}
			tr := &botTransport{api: api, editGuard: func(context.Context, int64, int) (func(), error) { return nil, cause }}
			out := tr.Attempt(t.Context(), SendRequest{Kind: store.PartEdit, ChatID: 1, TargetMessageID: 99})
			if out.Kind != OutcomeNotSent || out.Code != "edit_guard_held" || api.edits != 0 {
				t.Fatal(out, api.edits)
			}
			disp := stepDispatcher(t, f.owner, tr)
			disp.persist(c, out)
			got, err := f.st.GetDelivery(t.Context(), delivery.ID)
			if err != nil || got.Parts[0].State != store.PartPending || got.Parts[0].Attempts != c.Attempt-1 {
				t.Fatal(got, err)
			}
		})
	}
}
