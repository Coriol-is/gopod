package telegram

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// Compare the persisted plan to the actual production final-send helpers. These
// fixtures deliberately exclude preview ownership and upload-time failures: both
// remain cutover gates. No Telegram network or paid speech service is used.
func TestFrozenFinalReplyMatchesDirectPresentation(t *testing.T) {
	code := "Before.\n" + fence("go", 100) + "\nAfter."
	cases := []struct {
		name, mode, text     string
		stream, failSnapshot bool
	}{
		{name: "sync voice and text", mode: "voice+text", text: "Hello"},
		{name: "sync voice only", mode: "voice", text: "Hello"},
		{name: "stream voice retains text", mode: "voice", text: "Hello", stream: true},
		{name: "sync voice before document", mode: "voice", text: code},
		{name: "stream voice after document", mode: "voice+text", text: code, stream: true},
		{name: "multiple documents", mode: "text", text: code + "\n" + fence("py", 90)},
		{name: "medium code stays inline", mode: "voice+text", text: "Before\n" + fence("go", 20)},
		{name: "code only never speaks", mode: "voice", text: fence("go", 100)},
		{name: "unclosed code", mode: "text", text: "Before\n```go\n" + strings.Repeat("line\n", 90)},
		{name: "unicode multichunk sync", mode: "voice+text", text: strings.Repeat("Привет 🌍 **world**\n", 700)},
		{name: "unicode multichunk stream", mode: "text", text: strings.Repeat("Привет 🌍 **world**\n", 700), stream: true},
		{name: "snapshot failure keeps code", mode: "text", text: code, failSnapshot: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "fake")
			SetReplyMode(streamChat, c.mode)
			t.Cleanup(func() { SetReplyMode(streamChat, "") })
			var messageID atomic.Int64
			fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) {
				return sentMessage(strconv.FormatInt(554+messageID.Add(1), 10))
			}}
			direct, item := newStreamFixture(t, fake)
			directRec := &ttsRecorder{audio: []byte("OggS-audio")}
			directPD := enablePrepared(t, direct, recSpeech{directRec})
			frozenRec := &ttsRecorder{audio: []byte("OggS-audio")}
			frozen, id := frozenReplyFixture(t, c.text, frozenRec)
			if c.failSnapshot {
				directPD.MaxFileBytes = 1
				frozen.prepared.MaxFileBytes = 1
			}
			markup := direct.replyMarkup(item, c.text)
			markupJSON := ""
			if markup != nil {
				raw, err := json.Marshal(markup)
				if err != nil {
					t.Fatal(err)
				}
				markupJSON = string(raw)
			}
			if c.stream {
				if _, err := direct.sendFinal(t.Context(), item.ChatID, item.ThreadID, item.MessageID, c.text, markup); err != nil {
					t.Fatal(err)
				}
				direct.maybeVoiceReply(t.Context(), item, c.text)
			} else {
				direct.deliverSyncReply(t.Context(), item, c.text)
			}
			d, err := frozen.PrepareFrozenFinalReply(t.Context(), id, FinalReplyPresentation{Mode: c.mode, Streaming: c.stream, ReplyToMessageID: int64(item.MessageID), ReplyMarkup: markupJSON})
			if err != nil {
				t.Fatal(err)
			}
			d = acknowledgeFinalPlan(t, frozen, d)
			got := plannedPresentation(t, frozen, d)
			want := directPresentation(t, fake)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("presentation differs\nplan: %#v\ndirect: %#v", got, want)
			}
			if !reflect.DeepEqual(frozenRec.texts, directRec.texts) {
				t.Fatalf("speech differs: %q != %q", frozenRec.texts, directRec.texts)
			}
			// Publication retries reuse both exact parts and prepared audio.
			again, err := frozen.PrepareFrozenFinalReply(t.Context(), id, FinalReplyPresentation{Mode: c.mode, Streaming: c.stream, ReplyToMessageID: int64(item.MessageID), ReplyMarkup: markupJSON})
			if err != nil || again.ID != d.ID {
				t.Fatalf("repeat: %v", err)
			}
			if !reflect.DeepEqual(plannedPresentation(t, frozen, again), got) || frozenRec.calls() != directRec.calls() {
				t.Fatal("retry changed output or regenerated speech")
			}
		})
	}
}

type finalPresentationOperation struct {
	method, text, filename, body, markup string
	reply                                int64
}

func directPresentation(t *testing.T, f *fakeTelegram) []finalPresentationOperation {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []finalPresentationOperation
	for _, c := range f.calls {
		op := finalPresentationOperation{method: c.Method}
		switch c.Method {
		case "sendMessage":
			op.text = c.Form["text"]
			op.markup = c.Form["reply_markup"]
		case "sendDocument":
			op.text = c.Form["caption"]
			op.filename = c.Form["document.filename"]
			op.body = c.Form["document.content"]
		case "sendVoice":
			op.body = c.Form["voice.content"]
		default:
			continue
		}
		if raw := c.Form["reply_parameters"]; raw != "" {
			var p struct {
				MessageID int64 `json:"message_id"`
			}
			if err := json.Unmarshal([]byte(raw), &p); err != nil {
				t.Fatal(err)
			}
			op.reply = p.MessageID
		}
		out = append(out, op)
	}
	return out
}
func plannedPresentation(t *testing.T, b *Bot, d store.Delivery) []finalPresentationOperation {
	t.Helper()
	var out []finalPresentationOperation
	for _, p := range d.Parts {
		op := finalPresentationOperation{text: string(p.Part.Payload), markup: p.Part.ReplyMarkup, reply: p.Part.ReplyToMessageID}
		switch p.Part.Kind {
		case store.PartText:
			op.method = "sendMessage"
		case store.PartUpload:
			id, ok := store.ParseArtifactRef(p.Part.ArtifactRef)
			if !ok {
				t.Fatal("invalid artifact")
			}
			a, err := b.store.GetArtifact(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			body, err := b.prepared.Blobs.ReadVerified(a.BlobKey, a.Size, a.SHA256, store.DefaultMaxArtifactFileBytes)
			if err != nil {
				t.Fatal(err)
			}
			op.body = string(body)
			if p.Part.ParseMode == store.UploadPresentationVoice {
				op.method = "sendVoice"
			} else {
				op.method = "sendDocument"
				op.filename = a.Filename
			}
		default:
			t.Fatalf("unexpected kind %s", p.Part.Kind)
		}
		out = append(out, op)
	}
	return out
}

// A ready plan is an immutable contract even if it was staged by the earlier
// planner with different ordering/routing. Recovery must not "fix" its bytes.
func TestFrozenFinalReplyPreservesExistingReadyPresentation(t *testing.T) {
	rec := &ttsRecorder{audio: []byte("OggS-audio")}
	b, id := frozenReplyFixture(t, "source", rec)
	presentation := FinalReplyPresentation{Mode: "text", ReplyToMessageID: 10}
	raw, err := json.Marshal(finalReplyInput{Version: 1, Presentation: presentation})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.store.BeginFinalReply(t.Context(), id, raw); err != nil {
		t.Fatal(err)
	}
	result, err := b.store.GetTurnResult(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	original := store.FinalReplyPlan{Request: store.PublishRequest{ResultID: id, Conversation: result.Conversation, Owner: store.OwnerControllerResult, Parts: []store.DeliveryPart{
		{Kind: store.PartText, Payload: []byte("old first"), ReplyToMessageID: 10},
		{Kind: store.PartText, Payload: []byte("old second"), ReplyToMessageID: 10},
	}}}
	if _, err = b.store.StageFinalReply(t.Context(), id, original); err != nil {
		t.Fatal(err)
	}
	delivery, err := b.PrepareFrozenFinalReply(t.Context(), id, presentation)
	if err != nil {
		t.Fatal(err)
	}
	if len(delivery.Parts) != 2 {
		t.Fatal(delivery)
	}
	for i, p := range delivery.Parts {
		if !reflect.DeepEqual(p.Part, original.Request.Parts[i]) {
			t.Fatalf("rewrote old ready part %d: %+v", i, p.Part)
		}
	}
	if rec.calls() != 0 {
		t.Fatal("ready recovery called speech")
	}
}

func acknowledgeFinalPlan(t *testing.T, b *Bot, d store.Delivery) store.Delivery {
	t.Helper()
	o, err := b.store.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer o.Release()
	for i := 0; i < store.MaxDeliveryParts+1; i++ {
		c, ok, e := o.ClaimNext(t.Context(), time.Now())
		if e != nil {
			t.Fatal(e)
		}
		if !ok {
			break
		}
		if e = o.AckPart(t.Context(), c, int64(100+i)); e != nil {
			t.Fatal(e)
		}
	}
	d, err = b.store.GetDelivery(t.Context(), d.ID)
	if err != nil || d.State != store.DeliverySent {
		t.Fatal(d, err)
	}
	return d
}
