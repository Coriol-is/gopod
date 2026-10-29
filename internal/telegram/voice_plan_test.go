package telegram

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/store"
)

// splitAPI lets the voice upload fail independently of the text.
type splitAPI struct {
	fakeAPI
	voiceErr error
}

func (s *splitAPI) SendVoice(ctx context.Context, p *bot.SendVoiceParams) (*models.Message, error) {
	s.voices++
	s.lastVoice = p
	if up, ok := p.Voice.(*models.InputFileUpload); ok && up.Data != nil {
		s.voiceBytes, _ = io.ReadAll(up.Data)
	}
	if s.voiceErr != nil {
		return nil, s.voiceErr
	}
	return s.msg, nil
}

func (f *speechFixture) plan(t *testing.T, key string, texts ...string) VoiceReplyPlan {
	t.Helper()
	sp, err := f.prep.Prepare(t.Context(), f.req(key, "hello"))
	if err != nil {
		t.Fatal(err)
	}
	var parts []store.DeliveryPart
	for _, s := range texts {
		parts = append(parts, store.DeliveryPart{Kind: store.PartText, Payload: []byte(s), ParseMode: "HTML"})
	}
	return VoiceReplyPlan{Conversation: f.personal, Owner: store.OwnerLegacyAgentIPC, ActorRef: "test", Text: parts, Speech: sp, IntentKey: key}
}

func (f *speechFixture) run(t *testing.T, api outboxAPI) (*OutboxDispatcher, func()) {
	t.Helper()
	opener := &SnapshotOpener{Inventory: f.st, Blobs: f.blobs, MaxBytes: store.DefaultMaxArtifactFileBytes}
	owner, err := f.st.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(owner.Release) }
	t.Cleanup(release)
	disp := NewOutboxDispatcher(owner, &botTransport{api: api, artifacts: opener}, OutboxConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.st.SetPublishNotifier(disp.Notify)
	if err := disp.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return disp, func() { _ = disp.Stop(5 * time.Second); release() }
}

func (f *speechFixture) resolved(t *testing.T, id string) store.Delivery {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		d, err := f.st.GetDelivery(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if !d.Unresolved {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery unresolved: %s %+v", d.State, d.Parts)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestVoicePlanJoinsTextAndAudioInOneGroup(t *testing.T) {
	f := newSpeechFixture(t)
	api := &splitAPI{fakeAPI: fakeAPI{msg: &models.Message{ID: 9}}}
	_, stop := f.run(t, api)
	defer stop()
	plan := f.plan(t, "k", "<b>one</b>", "two")
	d, err := PublishVoiceReply(t.Context(), f.st, plan)
	if err != nil || !d.Created || len(d.Parts) != 3 {
		t.Fatalf("publish = %+v, %v", d, err)
	}
	if d.Parts[2].Part.Kind != store.PartUpload || d.Parts[2].Part.ParseMode != store.UploadPresentationVoice {
		t.Fatalf("last part = %+v", d.Parts[2].Part)
	}
	got := f.resolved(t, d.ID)
	st := DescribeVoiceDelivery(got)
	if got.State != store.DeliverySent || st.Summary != "text and voice delivered" || !st.Complete {
		t.Fatalf("state %s status %+v", got.State, st)
	}
	if api.voices != 1 || api.docs != 0 || string(api.voiceBytes) != "OggS-hello" {
		t.Fatalf("voices=%d docs=%d bytes=%q", api.voices, api.docs, api.voiceBytes)
	}
	if up := api.lastVoice.Voice.(*models.InputFileUpload); up.Filename != "reply.ogg" {
		t.Fatalf("filename %q", up.Filename)
	}
}

func TestVoicePlanAmbiguousVoiceIsUncertainAndNeverResent(t *testing.T) {
	f := newSpeechFixture(t)
	api := &splitAPI{fakeAPI: fakeAPI{msg: &models.Message{ID: 9}}, voiceErr: errors.New("error response from telegram for method sendVoice, 502 Bad Gateway")}
	_, stop := f.run(t, api)
	plan := f.plan(t, "k", "the answer")
	d, err := PublishVoiceReply(t.Context(), f.st, plan)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var got store.Delivery
	for {
		got, _ = f.st.GetDelivery(t.Context(), d.ID)
		if got.State == store.DeliveryUncertain || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := DescribeVoiceDelivery(got)
	if got.State != store.DeliveryUncertain || st.Text != "sent" || st.Voice != "uncertain" || st.Complete {
		t.Fatalf("state %s status %+v", got.State, st)
	}
	if st.Summary != "text delivered; voice uncertain (not resent)" {
		t.Fatalf("summary %q", st.Summary)
	}
	stop()
	// Publishing the same plan again converges: no second delivery, no resend.
	again, err := PublishVoiceReply(t.Context(), f.st, plan)
	if err != nil || again.Created || again.ID != d.ID || len(again.Parts) != 2 {
		t.Fatalf("again = %+v, %v", again, err)
	}
	_, stop2 := f.run(t, api)
	defer stop2()
	time.Sleep(200 * time.Millisecond)
	if api.voices != 1 {
		t.Fatalf("voice uploads = %d, want exactly 1", api.voices)
	}
	if f.backend.calls.Load() != 1 {
		t.Fatalf("tts calls = %d", f.backend.calls.Load())
	}
}

func TestVoicePlanRefusedVoiceIsPartialNotUncertain(t *testing.T) {
	f := newSpeechFixture(t)
	api := &splitAPI{fakeAPI: fakeAPI{msg: &models.Message{ID: 9}}, voiceErr: bot.ErrorBadRequest}
	_, stop := f.run(t, api)
	defer stop()
	d, err := PublishVoiceReply(t.Context(), f.st, f.plan(t, "k", "the answer"))
	if err != nil {
		t.Fatal(err)
	}
	got := f.resolved(t, d.ID)
	st := DescribeVoiceDelivery(got)
	if got.State != store.DeliveryPartial || st.Summary != "text delivered; voice not delivered" {
		t.Fatalf("state %s status %+v", got.State, st)
	}
}

func TestVoicePlanDuplicatePublicationIsOneUpload(t *testing.T) {
	f := newSpeechFixture(t)
	api := &splitAPI{fakeAPI: fakeAPI{msg: &models.Message{ID: 9}}}
	_, stop := f.run(t, api)
	defer stop()
	plan := f.plan(t, "k", "the answer")
	first, err := PublishVoiceReply(t.Context(), f.st, plan)
	if err != nil {
		t.Fatal(err)
	}
	f.resolved(t, first.ID)
	// A retried turn prepares the same intent (reused, no call) and publishes it.
	plan2 := f.plan(t, "k", "the answer")
	if !plan2.Speech.Reused {
		t.Fatal("retry did not reuse the clip")
	}
	second, err := PublishVoiceReply(t.Context(), f.st, plan2)
	if err != nil || second.Created || second.ID != first.ID {
		t.Fatalf("second = %+v, %v", second, err)
	}
	time.Sleep(100 * time.Millisecond)
	if api.voices != 1 || f.backend.calls.Load() != 1 {
		t.Fatalf("voices=%d tts=%d", api.voices, f.backend.calls.Load())
	}
}

func TestVoicePlanSameKeyDifferentContentIsAConflict(t *testing.T) {
	f := newSpeechFixture(t)
	api := &splitAPI{fakeAPI: fakeAPI{msg: &models.Message{ID: 9}}}
	_, stop := f.run(t, api)
	defer stop()
	plan := f.plan(t, "k", "the answer")
	if _, err := PublishVoiceReply(t.Context(), f.st, plan); err != nil {
		t.Fatal(err)
	}
	other := f.plan(t, "k", "a different answer")
	if _, err := PublishVoiceReply(t.Context(), f.st, other); !errors.Is(err, store.ErrRequestConflict) {
		t.Fatalf("err = %v, want a conflict", err)
	}
}

func TestVoicePlanExpiredClipIsNotRegenerated(t *testing.T) {
	f := newSpeechFixture(t)
	f.prep.ReservationTTL = 300 * time.Millisecond
	plan := f.plan(t, "k", "the answer")
	time.Sleep(400 * time.Millisecond)
	if _, err := PublishVoiceReply(t.Context(), f.st, plan); !errors.Is(err, store.ErrPreparationExpired) {
		t.Fatalf("err = %v, want expired", err)
	}
	if f.backend.calls.Load() != 1 {
		t.Fatalf("tts calls = %d", f.backend.calls.Load())
	}
}

func TestVoicePlanRejectsMalformedPlans(t *testing.T) {
	f := newSpeechFixture(t)
	plan := f.plan(t, "k", "x")
	bad := plan
	bad.IntentKey = ""
	if _, err := bad.PublishRequest(); !errors.Is(err, store.ErrInvalidDelivery) {
		t.Fatalf("no key: %v", err)
	}
	bad = plan
	bad.Text = []store.DeliveryPart{{Kind: store.PartUpload, ArtifactRef: "artifact:x"}}
	if _, err := bad.PublishRequest(); !errors.Is(err, store.ErrInvalidDelivery) {
		t.Fatalf("upload as text: %v", err)
	}
}

func TestDescribeVoiceDeliveryNeverReadsUncertainAsDelivered(t *testing.T) {
	mk := func(text, voice string) store.Delivery {
		d := store.Delivery{Unresolved: text == store.PartUncertain || voice == store.PartUncertain || text == store.PartPending}
		if text != "" {
			d.Parts = append(d.Parts, store.OutboxPart{Part: store.DeliveryPart{Kind: store.PartText}, State: text})
		}
		d.Parts = append(d.Parts, store.OutboxPart{Part: store.DeliveryPart{Kind: store.PartUpload}, State: voice})
		return d
	}
	cases := []struct{ text, voice, want string }{
		{"", store.PartSent, "voice delivered"},
		{"", store.PartUncertain, "voice uncertain (not resent)"},
		{store.PartUncertain, store.PartPending, "text uncertain (not resent)"},
		{store.PartSent, store.PartPending, "text delivered; voice pending"},
		{store.PartFailed, store.PartSent, "voice delivered; text not delivered"},
	}
	for _, c := range cases {
		if got := DescribeVoiceDelivery(mk(c.text, c.voice)).Summary; got != c.want {
			t.Errorf("%s/%s: %q, want %q", c.text, c.voice, got, c.want)
		}
	}
}
