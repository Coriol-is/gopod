package control

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

func fallbackFixture(t *testing.T) (*store.Store, Command) {
	t.Helper()
	st := testStore(t)
	ctx := context.Background()
	if err := st.RegisterChat(ctx, store.RegisteredChat{JID: "tg:100", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	id, _, err := st.InsertTurn(ctx, store.Turn{Source: "telegram", SourceID: "100:42", ChatFolder: "owner", ChatID: 100, TGMessageID: 42, ThreadID: 7, IsOwner: true, Text: "finish task", IsVoice: true, FilePath: "/workspace/chat/uploads/a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetState(ctx, QuotaOfferKey(id), "claude"); err != nil {
		t.Fatal(err)
	}
	return st, Command{Name: "provider.fallback", Args: []string{strconv.FormatInt(id, 10), "claude", "codex"}, Confirmed: true, Caller: Caller{Source: SourceTelegram, ChatID: 100, ThreadID: 7, IsOwner: true}}
}

func TestProviderFallbackConfirmedAndDurableDedup(t *testing.T) {
	st, cmd := fallbackFixture(t)
	r := New(silentLog())
	switches, retries := 0, 0
	RegisterProviderFallbackCommand(r, st, func(ctx context.Context, folder, expected, target string) error {
		switches++
		if folder != "owner" || expected != "claude" || target != "codex" {
			t.Fatal("wrong switch scope")
		}
		return nil
	}, func(ctx context.Context, turn store.Turn) (bool, error) {
		retries++
		if turn.ThreadID != 7 || turn.ChatFolder != "owner" || !turn.IsVoice || turn.FilePath == "" {
			t.Fatal("retry metadata lost")
		}
		turn.ID = 0
		turn.SourceID = FallbackSourceID(mustID(cmd))
		_, dup, err := st.InsertTurn(ctx, turn)
		return dup, err
	})
	unconfirmed := cmd
	unconfirmed.Confirmed = false
	resp, err := r.Dispatch(context.Background(), unconfirmed)
	if err != nil || resp.Code == 0 || switches != 0 || retries != 0 {
		t.Fatal("unconfirmed switch executed")
	}
	for i := 0; i < 2; i++ {
		resp, err = r.Dispatch(context.Background(), cmd)
		if err != nil || resp.Code != 0 {
			t.Fatalf("fallback: %+v %v", resp, err)
		}
	}
	if switches != 1 || retries != 1 {
		t.Fatalf("not once: switch=%d retry=%d", switches, retries)
	}
}
func mustID(cmd Command) int64 { id, _ := strconv.ParseInt(cmd.Args[0], 10, 64); return id }

func TestProviderFallbackRejectsForeignAndStaleOffers(t *testing.T) {
	for _, kind := range []string{"chat", "thread", "folder", "unconfirmed", "no-offer", "chain", "target", "source", "replied"} {
		t.Run(kind, func(t *testing.T) {
			st, cmd := fallbackFixture(t)
			r := New(silentLog())
			calls := 0
			switch kind {
			case "chat":
				cmd.Caller.ChatID = 200
			case "thread":
				cmd.Caller.ThreadID = 8
			case "folder":
				ctx := context.Background()
				if err := st.RegisterChat(ctx, store.RegisteredChat{JID: "tg:-200", Folder: "group", AddedAt: 1}); err != nil {
					t.Fatal(err)
				}
				id, _, err := st.InsertTurn(ctx, store.Turn{Source: "telegram", SourceID: "-200:42", ChatFolder: "group", ChatID: -200, TGMessageID: 42, ThreadID: 7, Text: "group request"})
				if err != nil {
					t.Fatal(err)
				}
				if err = st.SetState(ctx, QuotaOfferKey(id), "claude"); err != nil {
					t.Fatal(err)
				}
				cmd.Args[0] = strconv.FormatInt(id, 10)
				cmd.Caller.ChatID = -200
				cmd.Caller.IsOwner = false
				registered, err := st.GetRegistered(ctx, "tg:-200")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = st.RotateGroupEpoch(ctx, registered.JID, registered.RegistrationRevision); err != nil {
					t.Fatal(err)
				}
			case "unconfirmed":
				cmd.Confirmed = false
			case "no-offer":
				st.SetState(context.Background(), QuotaOfferKey(mustID(cmd)), "")
			case "chain":
				turn, _ := st.GetTurn(context.Background(), mustID(cmd))
				turn.ID = 0
				turn.SourceID = "fallback:99"
				id, _, _ := st.InsertTurn(context.Background(), turn)
				st.SetState(context.Background(), QuotaOfferKey(id), "claude")
				cmd.Args[0] = strconv.FormatInt(id, 10)
			case "target":
				cmd.Args[2] = "other"
			case "source":
				cmd.Caller.Source = SourceCLI
			case "replied":
				st.SetTurnReply(context.Background(), mustID(cmd), 999)
			}
			RegisterProviderFallbackCommand(r, st, func(context.Context, string, string, string) error { calls++; return nil }, func(context.Context, store.Turn) (bool, error) { calls++; return false, nil })
			resp, _ := r.Dispatch(context.Background(), cmd)
			if resp.Code == 0 || calls != 0 {
				t.Fatalf("%s allowed: %+v calls=%d", kind, resp, calls)
			}
		})
	}
}

func TestProviderFallbackSwitchAndQueueFailures(t *testing.T) {
	for _, kind := range []string{"stale", "switch", "queue"} {
		t.Run(kind, func(t *testing.T) {
			st, cmd := fallbackFixture(t)
			r := New(silentLog())
			queued := false
			RegisterProviderFallbackCommand(r, st, func(context.Context, string, string, string) error {
				if kind == "stale" {
					return ErrFallbackStale
				}
				if kind == "switch" {
					return errors.New("private Docker details")
				}
				return nil
			}, func(context.Context, store.Turn) (bool, error) {
				queued = true
				return false, errors.New("database unavailable")
			})
			resp, _ := r.Dispatch(context.Background(), cmd)
			if resp.Code == 0 || strings.Contains(resp.Text, "private") || queued != (kind == "queue") {
				t.Fatalf("failure not handled: %+v queued=%v", resp, queued)
			}
			if kind == "queue" && !strings.Contains(resp.Text, "переключён") {
				t.Fatal("concealed partial success")
			}
		})
	}
}
