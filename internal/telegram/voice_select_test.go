package telegram

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Coriol-is/gopod/internal/queue"
)

// ttsRecorder stands in for the personal TTS call: it records the key and the
// text it was asked to speak and returns scripted audio.
type ttsRecorder struct {
	mu    sync.Mutex
	keys  []string
	texts []string
	audio []byte
	err   error
}

func (r *ttsRecorder) call(_ context.Context, key, text string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, key)
	r.texts = append(r.texts, text)
	return r.audio, r.err
}

func (r *ttsRecorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.texts)
}

func voiceFake(voiceFails bool) *fakeTelegram {
	return &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
		switch method {
		case "sendVoice":
			if voiceFails {
				return apiError(400, "Bad Request: wrong file")
			}
			return sentMessage("777")
		case "sendMessage":
			return sentMessage("555")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
}

// voiceCase is one input to the personal automatic voice selection. The same
// table runs against the legacy path and the prepared path: a migration must
// not change which replies are spoken, what is spoken or where it is sent.
type voiceCase struct {
	name      string
	chat      func(item queue.Item) queue.Item
	mode      string // reply mode for the owner chat ("" = auto)
	isVoice   bool
	key       string // personal credential in the environment
	reply     string
	wantSpeak string // text handed to TTS; "" = no TTS call at all
	voiceFail bool
	ttsErr    error
	wantSent  bool // a sendVoice reached Telegram
}

func voiceCases() []voiceCase {
	same := func(i queue.Item) queue.Item { return i }
	group := func(i queue.Item) queue.Item { i.ChatID = -100200; return i }
	topic := func(i queue.Item) queue.Item { i.ThreadID = 7; return i }
	code := "```go\nfmt.Println(1)\n```"
	return []voiceCase{
		{name: "voice mode speaks", chat: same, mode: "voice", key: "k", reply: "Hello there", wantSpeak: "Hello there", wantSent: true},
		{name: "voice+text mode speaks", chat: same, mode: "voice+text", key: "k", reply: "Hello there", wantSpeak: "Hello there", wantSent: true},
		{name: "text mode is silent", chat: same, mode: "text", key: "k", reply: "Hello"},
		{name: "auto mirrors a voice message", chat: same, isVoice: true, key: "k", reply: "Hi", wantSpeak: "Hi", wantSent: true},
		{name: "auto stays text for typed input", chat: same, key: "k", reply: "Hi"},
		{name: "no personal key is silent", chat: same, mode: "voice", key: "", reply: "Hi"},
		{name: "code is never read aloud", chat: same, mode: "voice", key: "k", reply: "Done.\n" + code + "\nTests pass.", wantSpeak: "Done.\nTests pass.", wantSent: true},
		{name: "code-only reply is not spoken", chat: same, mode: "voice", key: "k", reply: code},
		{name: "blank reply is not spoken", chat: same, mode: "voice", key: "k", reply: "  \n "},
		{name: "group never uses the personal key", chat: group, mode: "voice", key: "k", reply: "Hi"},
		{name: "topic keeps its thread", chat: topic, mode: "voice", key: "k", reply: "Hi", wantSpeak: "Hi", wantSent: true},
		{name: "tts failure sends nothing", chat: same, mode: "voice", key: "k", reply: "Hi", wantSpeak: "Hi", ttsErr: errors.New("boom")},
		{name: "voice upload failure is not fatal", chat: same, mode: "voice", key: "k", reply: "Hi", wantSpeak: "Hi", voiceFail: true, wantSent: true},
	}
}

func TestLegacyVoiceSelectionCharacterization(t *testing.T) {
	for _, c := range voiceCases() {
		t.Run(c.name, func(t *testing.T) {
			runVoiceCase(t, c, nil)
		})
	}
}

// runVoiceCase drives maybeVoiceReply. prep, when non-nil, switches the bot to
// the prepared path and returns the recorder of the fake speech backend.
func runVoiceCase(t *testing.T, c voiceCase, prep func(t *testing.T, b *Bot, rec *ttsRecorder)) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", c.key)
	SetReplyMode(streamChat, c.mode)
	t.Cleanup(func() { SetReplyMode(streamChat, "") })
	fake := voiceFake(c.voiceFail)
	b, item := newStreamFixture(t, fake)
	item.IsVoice = c.isVoice
	item = c.chat(item)
	rec := &ttsRecorder{audio: []byte("OggS-audio"), err: c.ttsErr}
	b.tts = rec.call
	if prep != nil {
		prep(t, b, rec)
	}
	b.maybeVoiceReply(context.Background(), item, c.reply)

	if c.wantSpeak == "" {
		if rec.calls() != 0 {
			t.Fatalf("TTS called with %q, want no call", rec.texts)
		}
	} else {
		if rec.calls() != 1 || rec.texts[0] != c.wantSpeak {
			t.Fatalf("TTS texts = %q, want [%q]", rec.texts, c.wantSpeak)
		}
	}
	voices := fake.callsTo("sendVoice")
	if (len(voices) == 1) != c.wantSent || len(voices) > 1 {
		t.Fatalf("sendVoice calls = %d, want sent=%v", len(voices), c.wantSent)
	}
	if c.wantSent {
		v := voices[0].Form
		if v["voice.content"] != "OggS-audio" || v["voice.filename"] != "reply.ogg" {
			t.Errorf("voice upload = %q / %q", v["voice.filename"], v["voice.content"])
		}
		wantThread := ""
		if c.name == "topic keeps its thread" {
			wantThread = "7"
		}
		if v["message_thread_id"] != wantThread {
			t.Errorf("message_thread_id = %q, want %q", v["message_thread_id"], wantThread)
		}
		if !strings.Contains(v["reply_parameters"], `"message_id":10`) {
			t.Errorf("reply_parameters = %q, want reply to message 10", v["reply_parameters"])
		}
	}
	if len(fake.callsTo("sendMessage")) != 0 {
		t.Error("the voice path must not send text itself")
	}
}
