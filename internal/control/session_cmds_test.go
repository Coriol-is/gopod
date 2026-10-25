package control

import (
	"context"
	"testing"
)

// TestSessionCommandsActOnCallerThread checks that /clear and /compact
// act on the conversation (thread) the command was sent in, in the
// chat the Caller names.
func TestSessionCommandsActOnCallerThread(t *testing.T) {
	type call struct {
		folder string
		thread int
		clear  bool
	}
	var got []call
	r := New(silentLog())
	RegisterSessionCommands(r,
		func(_ context.Context, folder string, thread int, clear bool) (string, error) {
			got = append(got, call{folder, thread, clear})
			return "", nil
		},
		func(chatID int64) string {
			if chatID == 5 {
				return "alice"
			}
			return ""
		})

	for _, name := range []string{"clear", "compact"} {
		got = nil
		_, err := r.Dispatch(context.Background(), Command{
			Name: name,
			// /clear asks to confirm first (D028); this test is about
			// where a confirmed clear acts.
			Confirmed: true,
			Caller:    Caller{ChatID: 5, ThreadID: 9, Source: SourceTelegram},
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := call{"alice", 9, name == "clear"}
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s called %+v, want [%+v]", name, got, want)
		}
	}
}
