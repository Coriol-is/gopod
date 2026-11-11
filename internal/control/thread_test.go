package control

import (
	"context"
	"github.com/Coriol-is/gopod/internal/store"
	"testing"
	"time"
)

func TestThreadConfirmationScopeAndReplay(t *testing.T) {
	st := testStore(t)
	if err := st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:100", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	r := New(nil)
	calls := 0
	RegisterThreadCommands(r, st, func(_ context.Context, s store.ScopeRef, thread int) error {
		calls++
		if s.Folder != "owner" || thread != 7 {
			t.Fatal(s, thread)
		}
		return nil
	})
	cmd := Command{Name: "thread", Args: []string{"delete"}, Caller: Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 100, UserID: 100, ThreadID: 7, MessageID: 42, IsOwner: true}}
	prompt, err := r.Dispatch(t.Context(), cmd)
	if err != nil || prompt.Code != CodeConfirm || calls != 0 {
		t.Fatal(prompt, err, calls)
	}
	cmd.Args = prompt.Buttons[0][0].Args
	cmd.Confirmed = true
	for _, mutate := range []func(*Caller){func(c *Caller) { c.ThreadID = 8 }, func(c *Caller) { c.UserID = 101 }, func(c *Caller) { c.ChatKind = "supergroup" }, func(c *Caller) { c.Source = SourceCLI }, func(c *Caller) { c.ThreadID = 0 }} {
		wrong := cmd
		mutate(&wrong.Caller)
		res, err := r.Dispatch(t.Context(), wrong)
		if err != nil || res.Code != 1 || calls != 0 {
			t.Fatal(res, err, calls)
		}
	}
	res, err := r.Dispatch(t.Context(), cmd)
	if err != nil || res.Code != 0 || calls != 1 {
		t.Fatal(res, err, calls)
	}
	res, err = r.Dispatch(t.Context(), cmd)
	if err != nil || res.Code != 1 || calls != 1 {
		t.Fatal("replayed", res, err, calls)
	}
}
func TestThreadGateOffAndInfo(t *testing.T) {
	st := testStore(t)
	if err := st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:100", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	r := New(nil)
	RegisterThreadCommands(r, st, nil)
	cmd := Command{Name: "thread", Caller: Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 100, UserID: 100, ThreadID: 7, MessageID: 42, IsOwner: true}}
	res, err := r.Dispatch(t.Context(), cmd)
	if err != nil || res.Code != 0 {
		t.Fatal(res, err)
	}
	cmd.Args = []string{"delete"}
	res, err = r.Dispatch(t.Context(), cmd)
	if err != nil || res.Code != 1 || len(res.Buttons) != 0 {
		t.Fatal(res, err)
	}
	intents, err := st.PendingTopicDeletions(t.Context())
	if err != nil || len(intents) != 0 {
		t.Fatal(intents, err)
	}
	cmd.Caller.IsOwner = false
	if _, err = r.Dispatch(t.Context(), cmd); err != ErrNotAuthorized {
		t.Fatal(err)
	}
}

func TestThreadExpiredAndChangedRegistrationConfirmation(t *testing.T) {
	for _, change := range []string{"expiry", "registration"} {
		t.Run(change, func(t *testing.T) {
			st := testStore(t)
			rc := store.RegisteredChat{JID: "tg:100", Folder: "owner", IsOwner: true, AddedAt: 1}
			if err := st.RegisterChat(t.Context(), rc); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			r := New(nil)
			calls := 0
			registerThreadCommands(r, st, func(context.Context, store.ScopeRef, int) error { calls++; return nil }, func() time.Time { return now })
			cmd := Command{Name: "thread", Args: []string{"delete"}, Caller: Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 100, UserID: 100, ThreadID: 7, MessageID: 42, IsOwner: true}}
			res, err := r.Dispatch(t.Context(), cmd)
			if err != nil || res.Code != CodeConfirm {
				t.Fatal(res, err)
			}
			cmd.Args = res.Buttons[0][0].Args
			cmd.Confirmed = true
			if change == "expiry" {
				now = now.Add(6 * time.Minute)
			} else {
				rc.Name = "changed"
				if err = st.RegisterChat(t.Context(), rc); err != nil {
					t.Fatal(err)
				}
			}
			res, err = r.Dispatch(t.Context(), cmd)
			if err != nil || res.Code != 1 || calls != 0 {
				t.Fatal(res, err, calls)
			}
		})
	}
}
