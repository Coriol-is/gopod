package telegram

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

func codeOutcomeFake(mask int) *fakeTelegram {
	var ids atomic.Int64
	return &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
		if method == "sendDocument" {
			i := 0
			if strings.Contains(form["document.filename"], "-2") {
				i = 1
			}
			if mask&(1<<i) == 0 {
				return apiError(400, "Bad Request: file must be non-empty")
			}
		}
		return sentMessage(strconv.FormatInt(500+ids.Add(1), 10))
	}}
}
func drainFrozenCode(t *testing.T, b *Bot, d store.Delivery) store.Delivery {
	t.Helper()
	o, err := b.store.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer o.Release()
	transport := b.NewOutboxTransport(&SnapshotOpener{Inventory: b.store, Blobs: b.prepared.Blobs, MaxBytes: store.DefaultMaxArtifactFileBytes})
	dispatch := NewOutboxDispatcher(o, transport, OutboxConfig{}, b.log)
	for i := 0; i < store.MaxDeliveryParts+1; i++ {
		ok, e := dispatch.step(t.Context(), t.Context())
		if e != nil {
			t.Fatal(e)
		}
		if !ok {
			break
		}
	}
	got, err := b.store.GetDelivery(t.Context(), d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func TestConditionalCodeMatchesDirectAllMasks(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for mask := 0; mask < 4; mask++ {
			t.Run(fmt.Sprintf("stream=%v/mask=%d", stream, mask), func(t *testing.T) {
				t.Setenv("OPENAI_API_KEY", "fake")
				SetReplyMode(streamChat, "voice+text")
				t.Cleanup(func() { SetReplyMode(streamChat, "") })
				source := "Before 🌍 **bold**\n" + fence("go", 100) + "\nBetween\n" + fence("py", 90) + "\nAfter."
				df, ff := codeOutcomeFake(mask), codeOutcomeFake(mask)
				direct, item := newStreamFixture(t, df)
				dr := &ttsRecorder{audio: []byte("OggS-audio")}
				enablePrepared(t, direct, recSpeech{dr})
				fr := &ttsRecorder{audio: []byte("OggS-audio")}
				frozen, id := frozenReplyFixture(t, source, fr)
				frozen.api = newFakeBot(t, ff).api
				// Empty markup isolates delivery parity from action generation.
				if stream {
					_, _ = direct.sendFinal(t.Context(), item.ChatID, 0, item.MessageID, source, nil)
					direct.maybeVoiceReply(t.Context(), item, source)
				} else {
					direct.maybeVoiceReply(t.Context(), item, source)
					_, _ = direct.sendFinal(t.Context(), item.ChatID, 0, item.MessageID, source, nil)
				}
				d, err := frozen.PrepareFrozenFinalReply(t.Context(), id, FinalReplyPresentation{Mode: "voice+text", Streaming: stream, ReplyToMessageID: int64(item.MessageID)})
				if err != nil {
					t.Fatal(err)
				}
				d = drainFrozenCode(t, frozen, d)
				if d.State != store.DeliverySent || d.Unresolved {
					t.Fatal(d.State, d.Unresolved)
				}
				got, want := directPresentation(t, ff), directPresentation(t, df)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("presentation differs: got %d operations want %d", len(got), len(want))
				}
				if fr.calls() != 1 || dr.calls() != 1 {
					t.Fatal("speech regenerated")
				}
			})
		}
	}
}
func TestConditionalCodeEditOverflowAndKeyboard(t *testing.T) {
	source := "Before\n```go\n" + strings.Repeat("line <&> 🌍\n", 600) + "```\nAfter"
	for _, mask := range []int{0, 1} {
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			b, id := frozenReplyFixture(t, source, &ttsRecorder{})
			fake := codeOutcomeFake(mask)
			b.api = newFakeBot(t, fake).api
			p := FinalReplyPresentation{Mode: "text", EditMessageID: 42, ReplyToMessageID: 9, ReplyMarkup: `{"inline_keyboard":[[{"text":"OK","callback_data":"ok"}]]}`}
			d, err := b.PrepareFrozenFinalReply(t.Context(), id, p)
			if err != nil {
				t.Fatal(err)
			}
			d = drainFrozenCode(t, b, d)
			if d.State != store.DeliverySent {
				t.Fatal(d.State)
			}
			messages := fake.callsTo("sendMessage")
			edits := fake.callsTo("editMessageText")
			if mask == 1 {
				if len(edits) != 1 || len(messages) != 0 || edits[0].Form["message_id"] != "42" {
					t.Fatal("single suffix lost edit")
				}
			} else {
				if len(edits) != 0 || len(messages) < 2 {
					t.Fatal("overflow should send chunks")
				}
				for i, m := range messages {
					if (m.Form["reply_markup"] != "") != (i == len(messages)-1) {
						t.Fatal("wrong keyboard")
					}
					if (m.Form["reply_parameters"] != "") != (i == 0) {
						t.Fatal("wrong reply")
					}
					if strings.Contains(m.Form["text"], "📎") {
						t.Fatal("false filename note")
					}
				}
			}
		})
	}
}
func TestConditionalCodeBounds(t *testing.T) {
	for _, n := range []int{4, 5} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			source := strings.Repeat(fence("go", 65)+"\n", n)
			b, id := frozenReplyFixture(t, source, &ttsRecorder{})
			d, err := b.PrepareFrozenFinalReply(t.Context(), id, FinalReplyPresentation{Mode: "text"})
			if err != nil {
				t.Fatal(err)
			}
			intent, err := b.store.GetFinalReply(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if (intent.Plan.Request.CodeAlternatives != nil) != (n == 4) {
				t.Fatal("wrong extraction bound")
			}
			docs := 0
			for _, p := range d.Parts {
				if p.Part.Kind == store.PartUpload {
					docs++
				}
			}
			if n == 4 && docs != 4 || n == 5 && docs != 0 {
				t.Fatal(docs)
			}
		})
	}
	b, id := frozenReplyFixture(t, strings.Repeat(fence("go", 3000)+"\n", 5), &ttsRecorder{})
	if _, err := b.PrepareFrozenFinalReply(t.Context(), id, FinalReplyPresentation{Mode: "text"}); !errors.Is(err, store.ErrInvalidDelivery) {
		t.Fatal("overflow not held", err)
	}
}
func TestConditionalCodeIneligibleOutcomesNeverSendText(t *testing.T) {
	for _, tc := range []struct {
		name, desc string
		status     int
	}{{"generic", "Bad Request: anything", 400}, {"wrapped", "Bad Request: file must be non-empty extra", 400}, {"thread", "Bad Request: message thread not found", 400}, {"auth", "Unauthorized", 401}, {"server", "Internal Server Error", 500}} {
		t.Run(tc.name, func(t *testing.T) {
			b, id := frozenReplyFixture(t, fence("go", 70), &ttsRecorder{})
			f := &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
				if method == "sendDocument" {
					return apiError(tc.status, tc.desc)
				}
				return sentMessage("99")
			}}
			b.api = newFakeBot(t, f).api
			d, err := b.PrepareFrozenFinalReply(t.Context(), id, FinalReplyPresentation{Mode: "text"})
			if err != nil {
				t.Fatal(err)
			}
			d = drainFrozenCode(t, b, d)
			if len(f.callsTo("sendMessage")) != 0 || !d.Unresolved {
				t.Fatal("unsafe inline selection", d.State)
			}
		})
	}
}

func TestConditionalCodeCanceledBeforeDispatchKeepsUpload(t *testing.T) {
	b, id := frozenReplyFixture(t, fence("go", 70), &ttsRecorder{})
	d, err := b.PrepareFrozenFinalReply(t.Context(), id, FinalReplyPresentation{Mode: "text"})
	if err != nil {
		t.Fatal(err)
	}
	o, err := b.store.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer o.Release()
	c, _, err := o.ClaimNext(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dispatch := NewOutboxDispatcher(o, b.NewOutboxTransport(nil), OutboxConfig{}, b.log)
	dispatch.execute(ctx, c)
	got, _ := b.store.GetDelivery(t.Context(), d.ID)
	if len(got.Parts) != 1 || got.Parts[0].State != store.PartPending {
		t.Fatal(got)
	}
}

func TestConditionalCodeByteBudgetFallsBackBeforeUpload(t *testing.T) {
	source := strings.Repeat("n", 100000) + "\n" + strings.Repeat(fence("go", 65)+"\n", 4)
	b, id := frozenReplyFixture(t, source, &ttsRecorder{})
	d, err := b.PrepareFrozenFinalReply(t.Context(), id, FinalReplyPresentation{Mode: "text"})
	if err != nil {
		t.Fatal(err)
	}
	i, err := b.store.GetFinalReply(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if i.Plan.Request.CodeAlternatives != nil {
		t.Fatal("over-budget variants retained")
	}
	found := false
	for _, n := range i.Plan.Notes {
		found = found || n == "code_alternatives_budget_inline"
	}
	if !found {
		t.Fatal("missing budget note")
	}
	for _, p := range d.Parts {
		if p.Part.Kind == store.PartUpload || strings.Contains(string(p.Part.Payload), "📎") {
			t.Fatal("upload/note survived inline budget fallback")
		}
	}
	if len(i.Plan.Preparations) != 0 {
		t.Fatal("unused code artifacts attached")
	}
}
func TestConditionalCodeMixedSnapshotFailure(t *testing.T) {
	b, id := frozenReplyFixture(t, fence("go", 65)+"\n"+fence("py", 100), &ttsRecorder{})
	b.prepared.MaxFileBytes = 1000
	_, err := b.PrepareFrozenFinalReply(t.Context(), id, FinalReplyPresentation{Mode: "text"})
	if err != nil {
		t.Fatal(err)
	}
	i, _ := b.store.GetFinalReply(t.Context(), id)
	a := i.Plan.Request.CodeAlternatives
	if a == nil || len(a.Documents) != 1 || len(a.Variants) != 2 {
		t.Fatal("wrong snapshot alternatives")
	}
	for _, v := range a.Variants {
		var text string
		for _, p := range v {
			text += string(p.Payload)
		}
		if !strings.Contains(text, "line 100") || strings.Contains(text, "📎 snippet-2") {
			t.Fatal("failed snapshot lost inline code")
		}
	}
}
