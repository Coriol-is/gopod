package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/go-telegram/bot/models"
)

func TestTranscriptMetadataMediaTypesAndDuration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message *models.Message
		want    string
	}{
		{"voice", &models.Message{Voice: &models.Voice{Duration: 12}}, "[Telegram input metadata: source=voice_message; transcription=automatic; duration_seconds=12]\n\n"},
		{"audio", &models.Message{Audio: &models.Audio{Duration: 3, FileName: "private-file-name.mp3"}}, "[Telegram input metadata: source=audio_file; transcription=automatic; duration_seconds=3]\n\n"},
		{"video", &models.Message{VideoNote: &models.VideoNote{Duration: 4}}, "[Telegram input metadata: source=video_note; transcription=automatic; duration_seconds=4]\n\n"},
		{"unknown duration", &models.Message{Voice: &models.Voice{}}, "[Telegram input metadata: source=voice_message; transcription=automatic]\n\n"},
		{"invalid duration", &models.Message{Voice: &models.Voice{Duration: -1}}, "[Telegram input metadata: source=voice_message; transcription=automatic]\n\n"},
		{"text", &models.Message{Text: "hello"}, ""}, {"nil", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "Привет 👋\nsecond line"
			if got := transcriptWithMetadata(tc.message, body); got != tc.want+body {
				t.Fatalf("metadata/body changed: %q", got)
			}
			if got := transcriptWithMetadata(tc.message, ""); got != "" {
				t.Fatalf("empty transcript invented: %q", got)
			}
		})
	}
}

func TestVoiceTranscriptMetadataReachesThreadAndHandoff(t *testing.T) {
	f := newInboundFixture(t, map[string]string{"v1": "voice/message.ogg"})
	newFakeWhisper(t, f.b, "нужно продолжить задачу")
	m := threadedMsg(7, "")
	m.Voice = &models.Voice{FileID: "v1", Duration: 9}
	f.handle(m)
	item := f.next(t)
	if item.ThreadID != 7 || !item.IsVoice || !strings.Contains(item.Text, "source=voice_message") || !strings.Contains(item.Text, "duration_seconds=9") || !strings.HasSuffix(item.Text, "нужно продолжить задачу") {
		t.Fatalf("voice provenance lost: %+v", item)
	}
	ctx := context.Background()
	if err := f.b.store.RecordConversationRequest(ctx, "owner", 7, item.Text); err != nil {
		t.Fatal(err)
	}
	bridge, err := f.b.store.ConversationHandoff(ctx, "owner", 7, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(bridge, "source=voice_message") != 1 || !strings.Contains(bridge, "duration_seconds=9") {
		t.Fatalf("handoff lost or duplicated metadata: %s", bridge)
	}
	sibling, err := f.b.store.ConversationHandoff(ctx, "owner", 8, "codex")
	if err != nil || sibling != "" {
		t.Fatalf("voice metadata leaked: %s %v", sibling, err)
	}
}

func TestTranscriptMetadataLeavesCaptionDistinct(t *testing.T) {
	f := newInboundFixture(t, map[string]string{"a1": "audio/file.mp3"})
	newFakeWhisper(t, f.b, "spoken words")
	m := threadedMsg(7, "")
	m.Audio = &models.Audio{FileID: "a1", Duration: 5}
	m.Caption = "typed caption"
	f.handle(m)
	item := f.next(t)
	want := "typed caption\n\n[Telegram input metadata: source=audio_file; transcription=automatic; duration_seconds=5]\n\nspoken words"
	if item.Text != want || item.IsVoice {
		t.Fatalf("caption/transcript conflated: %+v", item)
	}
}

func TestFailedTranscriptionCaptionHasNoSuccessMetadata(t *testing.T) {
	f := newInboundFixture(t, nil)
	t.Setenv("OPENAI_API_KEY", "")
	m := threadedMsg(7, "")
	m.Audio = &models.Audio{FileID: "a1"}
	m.Caption = "typed caption"
	f.handle(m)
	item := f.next(t)
	if strings.Contains(item.Text, "transcription=automatic") || !strings.Contains(item.Text, "could not be transcribed") {
		t.Fatalf("failed transcription mislabeled: %q", item.Text)
	}
}

func TestTranscribedVoiceNeverBecomesLoginCode(t *testing.T) {
	f := newInboundFixture(t, map[string]string{"v1": "voice/message.ogg"})
	newFakeWhisper(t, f.b, "abc#def")
	f.b.logins.set(streamChat, &loginSession{chatID: streamChat, awaitsCode: true, private: true})
	var code string
	f.b.loginCode = func(_ context.Context, _ int64, text string) { code = text }
	m := threadedMsg(7, "")
	m.Voice = &models.Voice{FileID: "v1", Duration: 2}
	f.handle(m)
	item := f.next(t)
	if code != "" || !strings.Contains(item.Text, "abc#def") {
		t.Fatalf("voice routed as login code: code=%q text=%q", code, item.Text)
	}
}

func TestVoiceMetadataDoesNotAlterSlashRouting(t *testing.T) {
	f := newInboundFixture(t, map[string]string{"v1": "voice/message.ogg"})
	newFakeWhisper(t, f.b, "/echo one two")
	f.b.router = control.New(f.b.log)
	var args []string
	f.b.router.Register("echo", "echo", "test", control.PermChatLocal, func(_ context.Context, c control.Command) (control.Response, error) {
		args = c.Args
		return control.Response{Text: "ok"}, nil
	})
	m := threadedMsg(7, "")
	m.Voice = &models.Voice{FileID: "v1", Duration: 2}
	f.handle(m)
	f.none(t)
	if strings.Join(args, " ") != "one two" {
		t.Fatalf("metadata changed slash args: %v", args)
	}
}

func TestEmptyVoiceTranscriptDoesNotInventMetadataOnlyTurn(t *testing.T) {
	f := newInboundFixture(t, map[string]string{"v1": "voice/message.ogg"})
	newFakeWhisper(t, f.b, " \n ")
	m := threadedMsg(7, "")
	m.Voice = &models.Voice{FileID: "v1", Duration: 2}
	f.handle(m)
	f.none(t)
}
