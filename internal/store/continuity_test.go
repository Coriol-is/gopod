package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestConversationHandoffIsolationAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.sqlite")
	s, err := Open(ctx, path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		folder string
		thread int
		text   string
	}{{"a", 0, "main-only"}, {"a", 7, "thread-seven"}, {"a", 8, "thread-eight"}, {"b", 7, "other-chat"}} {
		if _, err = s.ConversationHandoff(ctx, v.folder, v.thread, "claude"); err != nil {
			t.Fatal(err)
		}
		if err = s.RecordConversationRequest(ctx, v.folder, v.thread, v.text); err != nil {
			t.Fatal(err)
		}
		if err = s.CompleteConversation(ctx, v.folder, v.thread, "claude", "answer-"+v.text); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.ConversationHandoff(ctx, "a", 7, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "thread-seven") || !strings.Contains(got, "answer-thread-seven") {
		t.Fatalf("lost transcript: %s", got)
	}
	for _, bad := range []string{"main-only", "thread-eight", "other-chat"} {
		if strings.Contains(got, bad) {
			t.Fatalf("leak %s: %s", bad, got)
		}
	}
	same, err := s.ConversationHandoff(ctx, "a", 7, "claude")
	if err != nil || same != "" {
		t.Fatalf("same provider: %s %v", same, err)
	}
	// Merely reading the bridge, including a failed attempt, doesn't consume it.
	retry, _ := s.ConversationHandoff(ctx, "a", 7, "codex")
	if retry != got {
		t.Fatal("handoff consumed before success")
	}
	if err = s.RecordConversationRequest(ctx, "a", 7, "codex followup"); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteConversation(ctx, "a", 7, "codex", "new result"); err != nil {
		t.Fatal(err)
	}
	back, _ := s.ConversationHandoff(ctx, "a", 7, "claude")
	if !strings.Contains(back, "codex followup") || !strings.Contains(back, "new result") {
		t.Fatalf("return lost progress: %s", back)
	}
}

func TestClearConversationAcrossProvidersOnlyOneThread(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, thread := range []int{0, 7, 8} {
		for _, p := range []string{"claude", "codex"} {
			if err := s.PutSession(ctx, "a", thread, p, "native-session"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.ConversationHandoff(ctx, "a", thread, "claude"); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordConversationRequest(ctx, "a", thread, fmt.Sprintf("private-%d", thread)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ClearConversationContinuity(ctx, "a", 7); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"claude", "codex"} {
		if _, ok, err := s.GetSession(ctx, "a", 7, p); err != nil || ok {
			t.Fatalf("cleared session remains %s %v", p, err)
		}
		if _, ok, err := s.GetSession(ctx, "a", 0, p); err != nil || !ok {
			t.Fatalf("main touched %s %v", p, err)
		}
		got, err := s.ConversationHandoff(ctx, "a", 7, p)
		if err != nil || got != "" {
			t.Fatalf("clear resurrected history: %s %v", got, err)
		}
	}
	got, _ := s.ConversationHandoff(ctx, "a", 8, "codex")
	if !strings.Contains(got, "private-8") {
		t.Fatal("sibling cleared")
	}
	marker, _ := s.GetState(ctx, "session_adopted:a:codex")
	if marker != "" {
		t.Fatal("thread clear changed main adoption")
	}
	if err := s.ClearConversationContinuity(ctx, "a", 0); err != nil {
		t.Fatal(err)
	}
	marker, _ = s.GetState(ctx, "session_adopted:a:codex")
	if marker == "" {
		t.Fatal("main legacy history could resurrect")
	}
}

func TestConversationHandoffSeedsLegacyOnce(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	turn := sampleTurn()
	turn.ThreadID = 7
	turn.Status = TurnDone
	turn.Text = "legacy user"
	turn.ReplyMsgID = 99
	if _, _, err := s.InsertTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveMessage(ctx, Message{ChatJID: "tg:100", TGMessageID: 99, Sender: "bot", Content: "legacy answer", IsFromMe: true}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ConversationHandoff(ctx, "owner", 7, "codex")
	if err != nil || !strings.Contains(got, "legacy answer") {
		t.Fatalf("legacy: %s %v", got, err)
	}
	again, _ := s.ConversationHandoff(ctx, "owner", 7, "codex")
	if again != got {
		t.Fatal("legacy duplicated")
	}
	if err := s.ClearConversationContinuity(ctx, "owner", 7); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ConversationHandoff(ctx, "owner", 7, "codex")
	if got != "" {
		t.Fatal("reimported cleared legacy history")
	}
}

func TestConversationHandoffBoundedJSON(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.ConversationHandoff(ctx, "a", 7, "claude"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 80; i++ {
		if err := s.RecordConversationRequest(ctx, "a", 7, strings.Repeat("ж<\n\"", 3000)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ConversationHandoff(ctx, "a", 7, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(got)) || !utf8.ValidString(got) || len(got) > 48000 || got == "" {
		t.Fatalf("bad bounded JSON: %d bytes, %v", len(got), err)
	}
}
