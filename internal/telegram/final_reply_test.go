package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

func frozenReplyFixture(t *testing.T, text string, rec *ttsRecorder) (*Bot, string) {
	t.Helper()
	b, item := newStreamFixture(t, voiceFake(false))
	enablePrepared(t, b, recSpeech{rec})
	conv, err := b.personalConversation(t.Context(), item.ChatID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.store.MarkTurnRunning(t.Context(), item.ID); err != nil {
		t.Fatal(err)
	}
	r, err := b.store.FreezeTurnResult(t.Context(), store.ExecutionRef{Conversation: conv, Kind: store.ExecInteractive, Members: []store.ResultMember{{TurnID: item.ID, Attempt: 1}}, Provider: "claude"}, text)
	if err != nil {
		t.Fatal(err)
	}
	return b, r.ID
}
func TestFrozenFinalReplyPresentation(t *testing.T) {
	for _, c := range []struct {
		mode        string
		stream      bool
		text, voice int
	}{{"text", false, 1, 0}, {"voice", false, 0, 1}, {"voice", true, 1, 1}, {"voice+text", false, 1, 1}} {
		t.Run(c.mode+string(rune('0'+c.text)), func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "fake")
			rec := &ttsRecorder{audio: []byte("OggS-audio")}
			b, id := frozenReplyFixture(t, "Hello there", rec)
			p := FinalReplyPresentation{Mode: c.mode, Streaming: c.stream, ReplyToMessageID: 10}
			d, err := b.PrepareFrozenFinalReply(t.Context(), id, p)
			if err != nil {
				t.Fatal(err)
			}
			texts, voices := 0, 0
			for _, part := range d.Parts {
				if part.Part.Kind == store.PartText {
					texts++
				}
				if part.Part.ParseMode == store.UploadPresentationVoice {
					voices++
				}
				if part.Part.ReplyToMessageID != 10 {
					t.Fatal("lost reply target")
				}
			}
			if texts != c.text || voices != c.voice || rec.calls() != c.voice {
				t.Fatalf("text=%d voice=%d calls=%d", texts, voices, rec.calls())
			}
			again, err := b.PrepareFrozenFinalReply(t.Context(), id, p)
			if err != nil || again.ID != d.ID || rec.calls() != c.voice {
				t.Fatalf("repeat %+v %v calls=%d", again, err, rec.calls())
			}
		})
	}
}
func TestFrozenFinalReplyUnknownVoiceNeverRebills(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "fake")
	rec := &ttsRecorder{err: context.DeadlineExceeded}
	b, id := frozenReplyFixture(t, "Hello", rec)
	for range 2 {
		_, err := b.PrepareFrozenFinalReply(t.Context(), id, FinalReplyPresentation{Mode: "voice"})
		if !errors.Is(err, ErrPreparationUnknown) {
			t.Fatal(err)
		}
	}
	if rec.calls() != 1 {
		t.Fatal(rec.calls())
	}
	i, err := b.store.GetFinalReply(t.Context(), id)
	if err != nil || i.State != store.FinalReplyPreparing {
		t.Fatalf("%+v %v", i, err)
	}
}
func TestFrozenFinalReplyCodeAndEdit(t *testing.T) {
	rec := &ttsRecorder{}
	b, id := frozenReplyFixture(t, "Example:\n```go\n"+strings.Repeat("line\n", 100)+"```", rec)
	d, err := b.PrepareFrozenFinalReply(t.Context(), id, FinalReplyPresentation{Mode: "text", EditMessageID: 50, ReplyMarkup: `{"inline_keyboard":[[{"text":"ok","callback_data":"x"}]]}`})
	if err != nil {
		t.Fatal(err)
	}
	d = acknowledgeFinalPlan(t, b, d)
	docs, edits := 0, 0
	for _, p := range d.Parts {
		if p.Part.Kind == store.PartUpload {
			docs++
		}
		if p.Part.Kind == store.PartEdit {
			edits++
			if p.Part.TargetMessageID != 50 || p.Part.ReplyMarkup == "" {
				t.Fatal(p)
			}
		}
	}
	if docs != 1 || edits != 1 || rec.calls() != 0 {
		t.Fatalf("docs=%d edits=%d calls=%d", docs, edits, rec.calls())
	}
}
