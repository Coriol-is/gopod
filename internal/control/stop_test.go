package control

import (
	"errors"
	"testing"
)

func TestStopCommandPermissionAndScope(t *testing.T) {
	r := New(nil)
	var chats []int64
	var threads []int
	RegisterStopCommand(r, func(chat int64, thread int) bool {
		chats = append(chats, chat)
		threads = append(threads, thread)
		return len(chats) == 1
	})
	cmd := Command{Name: "stop", Caller: Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 123, ThreadID: 7, MessageID: 42}}
	if _, err := r.Dispatch(t.Context(), cmd); !errors.Is(err, ErrNotAuthorized) {
		t.Fatal(err)
	}
	cmd.Caller.IsOwner = true
	for _, kind := range []string{"group", "supergroup", ""} {
		cmd.Caller.ChatKind = kind
		if res, err := r.Dispatch(t.Context(), cmd); err != nil || res.Code != 1 {
			t.Fatal(res, err)
		}
	}
	cmd.Caller.ChatKind = "private"
	cmd.Caller.Source = SourceCLI
	if res, _ := r.Dispatch(t.Context(), cmd); res.Code != 1 {
		t.Fatal(res)
	}
	cmd.Caller.Source = SourceTelegram
	// A caller without a real private message is refused, the same fence
	// /restart applies. The inline Stop control is its own callback and does
	// not reach this handler.
	cmd.Caller.MessageID = 0
	if res, _ := r.Dispatch(t.Context(), cmd); res.Code != 1 {
		t.Fatal(res)
	}
	cmd.Caller.MessageID = 42
	cmd.Args = []string{"all"}
	if res, _ := r.Dispatch(t.Context(), cmd); res.Code != 1 {
		t.Fatal(res)
	}
	if len(chats) != 0 {
		t.Fatal("invalid request stopped a run")
	}
	cmd.Args = nil
	res, err := r.Dispatch(t.Context(), cmd)
	if err != nil || res.Code != 0 || len(chats) != 1 || chats[0] != 123 || threads[0] != 7 {
		t.Fatal(res, err, chats, threads)
	}
	first := res.Text
	res, err = r.Dispatch(t.Context(), cmd)
	if err != nil || res.Code != 0 || res.Text == first {
		t.Fatal("idle/already stopped must be distinguished", res, err)
	}
	if name, ok := r.LookupBySlash("stop"); !ok || name != "stop" {
		t.Fatal(name, ok)
	}
}
