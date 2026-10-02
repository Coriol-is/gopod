package control

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(),
		filepath.Join(t.TempDir(), "store.sqlite"), silentLog())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func ownerCmd(args ...string) Command {
	return Command{
		Name:   "chats.register",
		Args:   args,
		Caller: Caller{Source: SourceTelegram, ChatID: 100, IsOwner: true},
	}
}

func TestChatsRegister(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	r := New(silentLog())
	RegisterChatCommands(r, st, 100)

	resp, err := r.Dispatch(ctx, ownerCmd("777", "alice"))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.Code != 0 {
		t.Fatalf("Code = %d, Text = %q", resp.Code, resp.Text)
	}

	rc, err := st.GetRegistered(ctx, "tg:777")
	if err != nil {
		t.Fatalf("GetRegistered: %v", err)
	}
	if rc.Folder != "alice" {
		t.Errorf("Folder = %q, want alice", rc.Folder)
	}
	if rc.IsOwner {
		t.Error("IsOwner = true, want false")
	}
	if rc.AddedAt == 0 {
		t.Error("AddedAt = 0")
	}
}

func TestChatsRegisterRejects(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	r := New(silentLog())
	RegisterChatCommands(r, st, 100)

	cases := []struct {
		name string
		args []string
		want string // substring of the reply
	}{
		{"no args", nil, "Usage:"},
		{"one arg", []string{"777"}, "Usage:"},
		{"three args", []string{"777", "alice", "extra"}, "Usage:"},
		{"bad chat id", []string{"abc", "alice"}, "bad chat_id"},
		{"owner chat", []string{"100", "owner"}, "auto-registered"},
		{"bad folder", []string{"777", "../escape"}, "register failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := r.Dispatch(ctx, ownerCmd(tc.args...))
			if resp.Code == 0 {
				t.Fatalf("Code = 0, want non-zero (Text = %q)", resp.Text)
			}
			if !strings.Contains(resp.Text, tc.want) {
				t.Errorf("Text = %q, want substring %q", resp.Text, tc.want)
			}
		})
	}
}

func TestChatsRegisterNonOwnerDenied(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	r := New(silentLog())
	RegisterChatCommands(r, st, 100)

	_, err := r.Dispatch(ctx, Command{
		Name:   "chats.register",
		Args:   []string{"777", "alice"},
		Caller: Caller{Source: SourceTelegram, ChatID: 555},
	})
	if err == nil {
		t.Fatal("err = nil, want ErrNotAuthorized")
	}
	if _, gerr := st.GetRegistered(ctx, "tg:777"); gerr == nil {
		t.Error("chat was registered despite permission denial")
	}
}
