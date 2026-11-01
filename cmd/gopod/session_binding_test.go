package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

type fakeBinder struct {
	calls    []string
	binding  store.ConversationBinding
	bound    map[int64]store.AttemptBinding
	bindings []store.AttemptBinding
	acqErr   error
	settled  []bool
}

func (f *fakeBinder) SnapshotScope(_ context.Context, folder string) (store.ScopeRef, error) {
	f.calls = append(f.calls, "snapshot")
	return store.ScopeRef{Folder: folder}, nil
}

func (f *fakeBinder) ResolveConversation(_ context.Context, _ store.ScopeRef, thread int) (store.ConversationBinding, error) {
	f.calls = append(f.calls, fmt.Sprintf("resolve:%d", thread))
	return f.binding, nil
}

func (f *fakeBinder) BindAttempt(_ context.Context, _ store.ScopeRef, b store.AttemptBinding) error {
	f.calls = append(f.calls, fmt.Sprintf("bind:%d", b.TurnID))
	f.bindings = append(f.bindings, b)
	return nil
}

func (f *fakeBinder) GetAttemptBinding(_ context.Context, id int64) (store.AttemptBinding, bool, error) {
	f.calls = append(f.calls, fmt.Sprintf("get:%d", id))
	b, ok := f.bound[id]
	return b, ok, nil
}

func (f *fakeBinder) AcquireExecutionClaim(_ context.Context, _ store.ScopeRef, sid string, id int64) error {
	f.calls = append(f.calls, fmt.Sprintf("acquire:%s:%d", sid, id))
	return f.acqErr
}

func (f *fakeBinder) SettleExecutionClaim(_ context.Context, sid string, id int64, unknown bool) error {
	f.calls = append(f.calls, fmt.Sprintf("settle:%s:%d", sid, id))
	f.settled = append(f.settled, unknown)
	return nil
}

func newBindingFixture() (*fakeBinder, func(string) string, func(string) string, *slog.Logger) {
	f := &fakeBinder{binding: store.ConversationBinding{ActiveSessionID: "s1", ContextEpoch: 2}}
	return f, func(string) string { return "claude" }, func(string) string { return "m1" },
		slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBindingHandlerBindsEveryItemAndClaimsLast(t *testing.T) {
	f, prov, mod, log := newBindingFixture()
	var nextCalls int
	h := bindingHandler(f, prov, mod, func(context.Context, []queue.Item) error {
		nextCalls++
		f.calls = append(f.calls, "next")
		return nil
	}, log)
	items := []queue.Item{{ID: 1, Folder: "a", ThreadID: 5}, {ID: 2, Folder: "a", ThreadID: 5}, {ID: 3, Folder: "a", ThreadID: 5}}
	if err := h(t.Context(), items); err != nil {
		t.Fatal(err)
	}
	want := []string{"snapshot", "resolve:5", "get:1", "bind:1", "get:2", "bind:2", "get:3", "bind:3", "acquire:s1:3", "next", "settle:s1:3"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
	for i, b := range f.bindings {
		if b.SessionID != "s1" || b.ContextEpoch != 2 || b.Provider != "claude" || b.Model != "m1" || b.TurnID != int64(i+1) {
			t.Errorf("binding %d = %+v", i, b)
		}
	}
	if nextCalls != 1 || !reflect.DeepEqual(f.settled, []bool{false}) {
		t.Errorf("next=%d settled=%v", nextCalls, f.settled)
	}
}

func TestBindingHandlerRefusesStaleEpoch(t *testing.T) {
	f, prov, mod, log := newBindingFixture()
	f.bound = map[int64]store.AttemptBinding{1: {TurnID: 1, SessionID: "s1", ContextEpoch: 1}}
	h := bindingHandler(f, prov, mod, func(context.Context, []queue.Item) error {
		t.Error("next called for stale epoch")
		return nil
	}, log)
	err := h(t.Context(), []queue.Item{{ID: 1, Folder: "a"}})
	if !errors.Is(err, ErrContextEpochChanged) || !errors.Is(err, queue.ErrStopped) {
		t.Fatalf("err = %v, want ErrContextEpochChanged wrapping ErrStopped", err)
	}
	if len(f.bindings) != 0 || len(f.settled) != 0 {
		t.Errorf("bound %v settled %v on refusal", f.bindings, f.settled)
	}
}

func TestBindingHandlerSettlesUnknownOnUnprovenExit(t *testing.T) {
	f, prov, mod, log := newBindingFixture()
	h := bindingHandler(f, prov, mod, func(context.Context, []queue.Item) error {
		return fmt.Errorf("run: %w", runner.ErrExecutionUnproven)
	}, log)
	err := h(t.Context(), []queue.Item{{ID: 7, Folder: "a"}})
	if !errors.Is(err, runner.ErrExecutionUnproven) {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(f.settled, []bool{true}) {
		t.Errorf("settled = %v, want [true]", f.settled)
	}
}

func TestBindingHandlerPassesManagedThrough(t *testing.T) {
	f, prov, mod, log := newBindingFixture()
	var nextCalls int
	h := bindingHandler(f, prov, mod, func(context.Context, []queue.Item) error { nextCalls++; return nil }, log)
	if err := h(t.Context(), []queue.Item{{ID: 1, Folder: "a", Source: "task", AttemptID: "att-1", RunID: "r1"}}); err != nil {
		t.Fatal(err)
	}
	if nextCalls != 1 || len(f.calls) != 0 {
		t.Errorf("next=%d binder calls=%v", nextCalls, f.calls)
	}
}

func TestBindingHandlerRetriesOnBusyClaim(t *testing.T) {
	f, prov, mod, log := newBindingFixture()
	f.acqErr = store.ErrSessionBusy
	h := bindingHandler(f, prov, mod, func(context.Context, []queue.Item) error {
		t.Error("next called while claim is busy")
		return nil
	}, log)
	err := h(t.Context(), []queue.Item{{ID: 1, Folder: "a"}})
	if !errors.Is(err, store.ErrSessionBusy) || errors.Is(err, queue.ErrStopped) {
		t.Fatalf("err = %v, want retryable ErrSessionBusy", err)
	}
	if len(f.settled) != 0 {
		t.Errorf("settled %v without a claim", f.settled)
	}
}
