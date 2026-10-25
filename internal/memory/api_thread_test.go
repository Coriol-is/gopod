package memory

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// The agent-facing HTTP API follows the D023 visibility rule: a token
// minted for thread T reads thread 0 rows plus T's own, main reads the
// whole chat, and a conversation_summary is only ever returned to its
// own conversation.
func TestAPIThreadIsolation(t *testing.T) {
	ctx := context.Background()
	m := newThreadTestMemory(t)
	add := func(thread int, kind, content string) int64 {
		t.Helper()
		id, err := m.AddInThread(ctx, "chat", thread, kind, "", content, "test")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	add(0, "fact", "otter MAINFACT")
	add(threadA, "fact", "otter AFACT")
	add(threadB, "fact", "otter BFACT")
	add(0, "conversation_summary", "otter MAINSUM")
	add(threadA, "conversation_summary", "otter ASUM")
	add(threadB, "conversation_summary", "otter BSUM")
	gone := add(threadA, "fact", "otter AOLD")
	if err := m.Supersede(ctx, gone, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddInThread(ctx, "other", 0, "fact", "", "otter FOREIGN", "test"); err != nil {
		t.Fatal(err)
	}

	ts := NewTokens()
	s := newAPIServer(m, "127.0.0.1:0", ts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	all := []string{"MAINFACT", "AFACT", "BFACT", "MAINSUM", "ASUM", "BSUM", "AOLD", "FOREIGN"}
	cases := []struct {
		name   string
		thread int
		see    []string
	}{
		{"thread A", threadA, []string{"MAINFACT", "AFACT", "ASUM"}},
		{"thread B", threadB, []string{"MAINFACT", "BFACT", "BSUM"}},
		{"main", 0, []string{"MAINFACT", "AFACT", "BFACT", "MAINSUM"}},
	}
	for _, c := range cases {
		tok := ts.Mint("chat", c.thread)
		for _, target := range []string{"/memory/search?q=otter&k=50", "/memory/list?limit=50"} {
			rr := do(s, http.MethodGet, target, tok, "")
			if rr.Code != http.StatusOK {
				t.Fatalf("%s %s: %d %s", c.name, target, rr.Code, rr.Body.String())
			}
			var res []struct{ Content string }
			if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			var got strings.Builder
			for _, r := range res {
				got.WriteString(r.Content + "|")
			}
			for _, marker := range all {
				want := false
				for _, v := range c.see {
					want = want || v == marker
				}
				// Markers are distinct words, so match with the separator.
				has := strings.Contains(got.String(), " "+marker+"|")
				if has != want {
					t.Errorf("%s %s: sees %s = %v, want %v (got %s)", c.name, target, marker, has, want, got.String())
				}
			}
		}
	}
}
