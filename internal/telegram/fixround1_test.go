package telegram

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// voiceOnly sets the explicit voice-only reply mode for the test chat.
func voiceOnly(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "") // no TTS call leaves the test
	SetReplyMode(streamChat, "voice")
	t.Cleanup(func() { SetReplyMode(streamChat, "") })
}

func TestDeliverSyncReply_VoiceOnlyStillDeliversCode(t *testing.T) {
	voiceOnly(t)
	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	b.deliverSyncReply(context.Background(), item, "Here it is:\n"+fence("go", 100))
	if n := len(fake.callsTo("sendDocument")); n != 1 {
		t.Errorf("sendDocument calls = %d, want 1", n)
	}
	msgs := fake.callsTo("sendMessage")
	if len(msgs) != 1 || msgs[0].Form["text"] != "Here it is:\n📎 snippet.go (100 lines)" {
		t.Errorf("sendMessage = %v, want the text with the note", msgs)
	}
	checkOneReply(t, b, item)
}

func TestDeliverSyncReply_VoiceOnlyMediumCodeCollapsed(t *testing.T) {
	voiceOnly(t)
	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	b.deliverSyncReply(context.Background(), item, fence("py", 20))
	msgs := fake.callsTo("sendMessage")
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0].Form["text"], "<blockquote expandable><pre>") {
		t.Errorf("sendMessage = %v, want one collapsed block", msgs)
	}
}

func TestDeliverSyncReply_VoiceOnlyPlainTextSendsNoText(t *testing.T) {
	voiceOnly(t)
	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	b.deliverSyncReply(context.Background(), item, "Just words, with `inline` code.")
	if n := len(fake.callsTo("sendMessage")); n != 0 {
		t.Errorf("sendMessage calls = %d, want 0 in voice-only mode", n)
	}
}

func TestEditMessageHTML_FallbackShowsNoMarkup(t *testing.T) {
	fake := &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
		if form["parse_mode"] == "HTML" {
			return apiError(400, "Bad Request: can't parse entities")
		}
		return sentMessage("5")
	}}
	b := newFakeBot(t, fake)
	in := finalReplyHTML("a **b** & c\n" + fence("go", 13))
	if err := b.editMessageHTML(context.Background(), streamChat, 5, in); err != nil {
		t.Fatalf("editMessageHTML: %v", err)
	}
	edits := fake.callsTo("editMessageText")
	if len(edits) != 2 {
		t.Fatalf("edits = %d, want 2", len(edits))
	}
	got := edits[1].Form["text"]
	if !strings.HasPrefix(got, "a b & c\nline 1 <x>\n") || strings.Contains(got, "<pre>") ||
		strings.Contains(got, "<blockquote") || strings.Contains(got, "&lt;") {
		t.Errorf("plain fallback = %q", got)
	}
}

func TestEditMessageHTML_NotModifiedNoFallback(t *testing.T) {
	fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) {
		return apiError(400, "Bad Request: message is not modified")
	}}
	b := newFakeBot(t, fake)
	err := b.editMessageHTML(context.Background(), streamChat, 5, "<b>x</b>")
	if err == nil || !isNotModified(err) {
		t.Errorf("err = %v, want the not-modified error", err)
	}
	if n := len(fake.callsTo("editMessageText")); n != 1 {
		t.Errorf("edits = %d, want 1 (no plain-text fallback)", n)
	}
}

func TestSendFinal_LogsWhenGivingUp(t *testing.T) {
	fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) {
		return tooManyRequests("1")
	}}
	b := newFakeBot(t, fake)
	recordSleeps(b)
	var logs bytes.Buffer
	b.log = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := b.sendFinal(context.Background(), streamChat, 7, 0, "secret answer"); err == nil {
		t.Fatal("want error")
	}
	out := logs.String()
	if !strings.Contains(out, "giving up") || !strings.Contains(out, "attempts=3") || !strings.Contains(out, "thread=7") {
		t.Errorf("no give-up warning: %s", out)
	}
	if strings.Contains(out, "secret answer") {
		t.Error("log carries the reply text")
	}
}
