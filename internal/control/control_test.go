package control

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

func silentLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func echoHandler(_ context.Context, cmd Command) (Response, error) {
	return Response{Text: cmd.Name + " ok"}, nil
}

func TestDispatchPublic(t *testing.T) {
	r := New(silentLog())
	r.Register("ping", "ping", "liveness", PermPublic, echoHandler)

	resp, err := r.Dispatch(context.Background(), Command{
		Name:   "ping",
		Caller: Caller{Source: SourceTelegram, ChatID: 123},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.Text != "ping ok" {
		t.Errorf("Text = %q", resp.Text)
	}
}

func TestDispatchOwnerOnly_Allowed(t *testing.T) {
	r := New(silentLog())
	r.Register("chats.list", "chats", "list chats", PermOwnerOnly, echoHandler)

	resp, err := r.Dispatch(context.Background(), Command{
		Name:   "chats.list",
		Caller: Caller{IsOwner: true, Source: SourceTelegram},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.Text != "chats.list ok" {
		t.Errorf("Text = %q", resp.Text)
	}
}

func TestDispatchOwnerOnly_Denied(t *testing.T) {
	r := New(silentLog())
	r.Register("chats.list", "chats", "list chats", PermOwnerOnly, echoHandler)

	_, err := r.Dispatch(context.Background(), Command{
		Name:   "chats.list",
		Caller: Caller{IsOwner: false, Source: SourceTelegram, ChatID: 999},
	})
	if !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("err = %v, want ErrNotAuthorized", err)
	}
}

func TestDispatchUnknown(t *testing.T) {
	r := New(silentLog())
	_, err := r.Dispatch(context.Background(), Command{Name: "nope"})
	if !errors.Is(err, ErrUnknownCommand) {
		t.Errorf("err = %v, want ErrUnknownCommand", err)
	}
}

func TestListSorted(t *testing.T) {
	r := New(silentLog())
	r.Register("whoami", "whoami", "who", PermPublic, echoHandler)
	r.Register("ping", "ping", "pong", PermPublic, echoHandler)
	r.Register("chats.list", "chats", "list", PermOwnerOnly, echoHandler)

	cmds := r.List()
	if len(cmds) != 3 {
		t.Fatalf("len = %d", len(cmds))
	}
	if cmds[0].Name != "chats.list" || cmds[1].Name != "ping" || cmds[2].Name != "whoami" {
		t.Errorf("order: %v %v %v", cmds[0].Name, cmds[1].Name, cmds[2].Name)
	}
}

func TestLookupBySlash(t *testing.T) {
	r := New(silentLog())
	r.Register("chats.register", "register", "reg", PermOwnerOnly, echoHandler)

	name, ok := r.LookupBySlash("register")
	if !ok || name != "chats.register" {
		t.Errorf("LookupBySlash(register) = (%q, %v)", name, ok)
	}

	_, ok = r.LookupBySlash("nope")
	if ok {
		t.Error("LookupBySlash(nope) should return false")
	}
}

func TestRegisterPanicOnDuplicate(t *testing.T) {
	r := New(silentLog())
	r.Register("ping", "ping", "p", PermPublic, echoHandler)
	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate Register")
		}
	}()
	r.Register("ping", "ping", "p", PermPublic, echoHandler)
}

func TestCLICallerIsAlwaysOwner(t *testing.T) {
	r := New(silentLog())
	r.Register("secret", "secret", "s", PermOwnerOnly, echoHandler)

	_, err := r.Dispatch(context.Background(), Command{
		Name:   "secret",
		Caller: Caller{IsOwner: true, Source: SourceCLI},
	})
	if err != nil {
		t.Errorf("CLI caller with IsOwner=true should pass: %v", err)
	}
}
