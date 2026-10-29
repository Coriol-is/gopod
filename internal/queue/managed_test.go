package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeLedger is an in-memory model of the task ledger rules the queue relies on.
type fakeLedger struct {
	mu     sync.Mutex
	items  map[int64]Item
	att    map[string]*fakeAttempt
	byTurn map[int64]string
	calls  []string

	reserveErr error
	beginErr   error
	settleErr  error
	heldErr    error
}

type fakeAttempt struct {
	state, reservation string
	snap               Snapshot
	folder, run        string
	turn               int64
	frozen             string
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{items: map[int64]Item{}, att: map[string]*fakeAttempt{}, byTurn: map[int64]string{}}
}

// admit models a committed admission: a pending managed turn with a queued attempt.
func (l *fakeLedger) admit(id int64, folder string, thread int) Item {
	l.mu.Lock()
	defer l.mu.Unlock()
	aid := fmt.Sprintf("att-%d", id)
	it := Item{ID: id, Source: "task", SourceID: fmt.Sprintf("task:t/run:r%d/attempt:%s", id, aid), Folder: folder, ThreadID: thread,
		RunID: fmt.Sprintf("r%d", id), AttemptID: aid, SessionKind: "isolated", Text: "do it"}
	l.items[id] = it
	l.att[aid] = &fakeAttempt{state: "queued", reservation: "none", folder: folder, run: it.RunID, turn: id}
	l.byTurn[id] = aid
	return it
}

func (l *fakeLedger) set(aid, state, reservation string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.att[aid]
	a.state, a.reservation = state, reservation
	if reservation != "none" && a.snap.Provider == "" {
		a.snap = Snapshot{Provider: "claude", Model: "frozen-model"}
	}
}

func (l *fakeLedger) freeze(aid, result string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.att[aid].frozen = result
}

func (l *fakeLedger) state(aid string) (string, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.att[aid].state, l.att[aid].reservation
}

func (l *fakeLedger) log(s string) { l.calls = append(l.calls, s) }

func (l *fakeLedger) callList() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

func (l *fakeLedger) LoadTurn(_ context.Context, id int64) (Item, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	it, ok := l.items[id]
	if !ok {
		return Item{}, errors.New("no such turn")
	}
	return it, nil
}

func (l *fakeLedger) Reserve(_ context.Context, aid string, snap Snapshot, _ time.Time) (ReserveOutcome, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.log("reserve")
	if l.reserveErr != nil {
		return ReserveOutcome{}, l.reserveErr
	}
	a := l.att[aid]
	switch {
	case a.frozen != "":
		return ReserveOutcome{Kind: ReserveRejected, Reason: "result_frozen"}, nil
	case a.state == "queued":
		a.state, a.reservation, a.snap = "reserved", "held", snap
		return ReserveOutcome{Kind: ReserveNew, Snapshot: snap}, nil
	case a.state == "reserved":
		return ReserveOutcome{Kind: ReserveResume, Snapshot: a.snap}, nil
	}
	return ReserveOutcome{Kind: ReserveRejected, Reason: "not_dispatchable"}, nil
}

func (l *fakeLedger) BeginSpawn(_ context.Context, aid string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.log("spawn-marker")
	if l.beginErr != nil {
		return l.beginErr
	}
	a := l.att[aid]
	if a.state != "reserved" {
		return ErrSpawnRefused
	}
	a.state = "running"
	return nil
}

func (l *fakeLedger) Settle(_ context.Context, aid string, s Settlement) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.log("settle:" + string(s.Kind))
	if l.settleErr != nil {
		return "", l.settleErr
	}
	a := l.att[aid]
	terminal := map[SettleKind]string{SettleExited: StateCompleted, SettleFailed: StateFailed, SettleStopped: StateCancelled,
		SettleInterrupted: StateInterrupted, SettleNotStarted: StateFailed}
	if to, ok := terminal[s.Kind]; ok {
		if s.Evidence == "" {
			return "", errors.New("evidence required")
		}
		a.state, a.reservation = to, "released"
		return to, nil
	}
	a.state = StateUnknown
	return a.state, nil
}

func (l *fakeLedger) Recover(_ context.Context, turnID int64) (Recovery, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.log("recover")
	a := l.att[l.byTurn[turnID]]
	switch a.state {
	case StateCompleted, StateFailed, StateCancelled, StateInterrupted:
		return Recovery{Action: RecoverSettled, State: a.state}, nil
	case "queued", "reserved":
		if a.frozen == "" {
			return Recovery{Action: RecoverDispatch, State: a.state}, nil
		}
	}
	a.state = StateUnknown
	if a.frozen != "" {
		return Recovery{Action: RecoverFrozen, State: a.state, ResultID: a.frozen}, nil
	}
	return Recovery{Action: RecoverHold, State: a.state}, nil
}

func (l *fakeLedger) Held(context.Context) ([]Hold, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.heldErr != nil {
		return nil, l.heldErr
	}
	var out []Hold
	for id, a := range l.att {
		if a.reservation == "held" && a.state != "reserved" {
			out = append(out, Hold{AttemptID: id, RunID: a.run, Folder: a.folder, TurnID: a.turn})
		}
	}
	return out, nil
}

func (l *fakeLedger) Release(_ context.Context, aid string, _ ReleaseProof, _ string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.att[aid]
	if a.reservation != "held" || a.state == "running" || a.state == "reserved" {
		return errors.New("not releasable")
	}
	a.reservation = "released"
	return nil
}

// harnessStore is a TurnStore that accepts managed turn ids chosen by the ledger.
type harnessStore struct {
	mu     sync.Mutex
	nextID int64
	rows   map[int64]*Item
	status map[int64]string
}

func newHarnessStore() *harnessStore {
	return &harnessStore{nextID: 1000, rows: map[int64]*Item{}, status: map[int64]string{}}
}

func (s *harnessStore) InsertTurn(_ context.Context, it Item) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	it.ID = s.nextID
	s.rows[it.ID], s.status[it.ID] = &it, "pending"
	return it.ID, false, nil
}

func (s *harnessStore) MarkRunning(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[id] = "running"
	if r := s.rows[id]; r != nil {
		r.Attempts++
	}
	return nil
}

func (s *harnessStore) MarkFinished(_ context.Context, id int64, status, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[id] = status
	return nil
}

func (s *harnessStore) LoadUnfinished(context.Context) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Item
	for id := int64(0); id <= s.nextID; id++ {
		if r := s.rows[id]; r != nil {
			switch s.status[id] {
			case "pending", "running", StatusInterrupted:
				out = append(out, *r)
			}
		}
	}
	return out, nil
}

// setUnfinished makes items look like rows left behind by a dead process.
func (s *harnessStore) setUnfinished(items ...Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range items {
		it := it
		s.rows[it.ID], s.status[it.ID] = &it, "pending"
	}
}

func (s *harnessStore) statusOf(id int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status[id]
}

// harness wires a Queue with a fake ledger, a recording turn store and handlers.
type harness struct {
	t      *testing.T
	q      *Queue
	ledger *fakeLedger
	store  *harnessStore

	mu       sync.Mutex
	order    []string // interactive "i:<text>" and managed "m:<turn>"
	managed  atomic.Int32
	legacy   []error // errors seen by legacy OnDone hooks
	outcomes []ManagedOutcome
	frozen   []string

	run func(ctx context.Context, h *harness, it Item) error
}

func newHarness(t *testing.T, maxConc int, mutate func(*ManagedRuntime)) *harness {
	t.Helper()
	h := &harness{t: t, ledger: newFakeLedger(), store: newHarnessStore()}
	h.q = New(context.Background(), func(_ context.Context, items []Item) error {
		h.mu.Lock()
		h.order = append(h.order, "i:"+items[len(items)-1].Text)
		h.mu.Unlock()
		return nil
	}, maxConc, h.store, silentLog())
	h.q.BackoffFunc = func(int) time.Duration { return 0 }
	rt := ManagedRuntime{
		Store: h.ledger,
		Snapshot: func(context.Context, Item) (Snapshot, error) {
			return Snapshot{Provider: "claude", Model: "live-model"}, nil
		},
		Handler: func(ctx context.Context, items []Item) error {
			if len(items) != 1 {
				t.Errorf("managed batch of %d", len(items))
			}
			h.managed.Add(1)
			h.mu.Lock()
			h.order = append(h.order, fmt.Sprintf("m:%d", items[0].ID))
			h.mu.Unlock()
			if h.run != nil {
				return h.run(ctx, h, items[0])
			}
			return h.spawnAndExit(ctx, 0)
		},
		OnFrozen: func(_ context.Context, _ Item, id string) error {
			h.mu.Lock()
			h.frozen = append(h.frozen, id)
			h.mu.Unlock()
			return nil
		},
	}
	if mutate != nil {
		mutate(&rt)
	}
	h.q.SetManaged(rt)
	h.q.OnDone(func(_ Item, err error) {
		h.mu.Lock()
		h.legacy = append(h.legacy, err)
		h.mu.Unlock()
	})
	h.q.OnManagedDone(func(_ Item, o ManagedOutcome) {
		h.mu.Lock()
		h.outcomes = append(h.outcomes, o)
		h.mu.Unlock()
	})
	t.Cleanup(h.q.Close)
	return h
}

// spawnAndExit is a well-behaved handler: marker, "native run", observed exit.
func (h *harness) spawnAndExit(ctx context.Context, _ int) error {
	e, _ := ExecutionFrom(ctx)
	if err := e.BeginSpawn(ctx); err != nil {
		return err
	}
	h.ledger.mu.Lock()
	h.ledger.log("native-exec")
	h.ledger.mu.Unlock()
	e.Exited("container_exec_exit:0")
	return nil
}

func (h *harness) nativeCalls() int {
	n := 0
	for _, c := range h.ledger.callList() {
		if c == "native-exec" {
			n++
		}
	}
	return n
}

func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatalf("timeout waiting for %s (calls=%v)", what, h.ledger.callList())
}

func (h *harness) outcomeStates() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, o := range h.outcomes {
		out = append(out, o.State)
	}
	return out
}

func (h *harness) orderList() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.order...)
}

func TestSameBatchNeverCoalescesManagedRuns(t *testing.T) {
	m1 := Item{ID: 1, Source: "task", AttemptID: "a1", RunID: "r1", ThreadID: 0}
	m2 := Item{ID: 2, Source: "task", AttemptID: "a2", RunID: "r2", ThreadID: 0}
	tg := Item{ID: 3, Source: "telegram", ThreadID: 0}
	legacy := Item{ID: 4, Source: "task", ThreadID: 0}
	for name, pair := range map[string][2]Item{
		"managed+telegram": {m1, tg}, "telegram+managed": {tg, m1},
		"managed+managed": {m1, m2}, "managed+legacy task": {m1, legacy}, "managed+itself": {m1, m1},
	} {
		if sameBatch(pair[0], pair[1]) {
			t.Errorf("%s coalesced", name)
		}
	}
	if !sameBatch(legacy, Item{Source: "task"}) || !sameBatch(tg, Item{Source: "telegram"}) {
		t.Error("legacy batching changed")
	}
}

func TestManagedRunsAreSingletonsBesideTelegramAtThreadZero(t *testing.T) {
	h := newHarness(t, 4, nil)
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	h.q.handler = func(_ context.Context, items []Item) error {
		h.mu.Lock()
		h.order = append(h.order, fmt.Sprintf("i:%d", len(items)))
		h.mu.Unlock()
		started <- struct{}{}
		<-gate
		return nil
	}
	h.q.Enqueue(context.Background(), Item{Folder: "alice", Source: "telegram", Text: "t1"})
	<-started
	m1, m2 := h.ledger.admit(1, "alice", 0), h.ledger.admit(2, "alice", 0)
	for _, it := range []Item{m1, m2} {
		if ok, err := h.q.Dispatch(context.Background(), it.ID); err != nil || !ok {
			t.Fatalf("dispatch %d: %v %v", it.ID, ok, err)
		}
	}
	h.q.Enqueue(context.Background(), Item{Folder: "alice", Source: "telegram", Text: "t2"})
	h.q.Enqueue(context.Background(), Item{Folder: "alice", Source: "telegram", Text: "t3"})
	close(gate)
	h.waitFor("all runs", func() bool { return len(h.orderList()) == 4 })
	got := fmt.Sprint(h.orderList())
	// t1 alone, each managed run alone in arrival order, then t2+t3 as one batch.
	if want := "[i:1 m:1 m:2 i:2]"; got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
	if n := h.nativeCalls(); n != 2 {
		t.Fatalf("native calls = %d", n)
	}
}

func TestCrashAfterCommitBeforeDispatchIsRecoveredFromStore(t *testing.T) {
	h := newHarness(t, 2, nil)
	it := h.ledger.admit(7, "alice", 0)
	// The producer committed, then the process died before Dispatch ran. A new
	// process loads the unfinished row.
	h.store.setUnfinished(it)
	n, err := h.q.Recover(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("Recover = %d, %v", n, err)
	}
	h.waitFor("recovered run", func() bool { return len(h.outcomeStates()) == 1 })
	if got := h.outcomeStates()[0]; got != StateCompleted || h.nativeCalls() != 1 {
		t.Fatalf("state=%s native=%d", got, h.nativeCalls())
	}
	if len(h.legacy) != 0 {
		t.Fatalf("legacy done hooks saw managed work: %v", h.legacy)
	}
	want := []string{"recover", "reserve", "spawn-marker", "native-exec", "settle:exited"}
	if got := h.ledger.callList(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("ledger order = %v, want %v", got, want)
	}
}

func TestReservationAndSnapshotArePersistedBeforeSpawnAndFailureMeansNoSpawn(t *testing.T) {
	h := newHarness(t, 2, nil)
	h.ledger.reserveErr = errors.New("database is locked")
	h.q.maxRetries = 2
	it := h.ledger.admit(1, "alice", 0)
	if ok, err := h.q.Dispatch(context.Background(), it.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	h.waitFor("retries exhausted", func() bool { return h.q.ActiveChats() == 0 })
	if h.managed.Load() != 0 || h.nativeCalls() != 0 {
		t.Fatalf("handler ran without a committed reservation (handler=%d native=%d)", h.managed.Load(), h.nativeCalls())
	}
	if state, res := h.ledger.state(it.AttemptID); state != "queued" || res != "none" {
		t.Fatalf("ledger changed: %s/%s", state, res)
	}
	if len(h.outcomeStates()) != 0 {
		t.Fatal("a never-reserved attempt was reported done")
	}
	// It stays durable and is dispatchable again after the store recovers.
	h.ledger.mu.Lock()
	h.ledger.reserveErr = nil
	h.ledger.mu.Unlock()
	if ok, err := h.q.Dispatch(context.Background(), it.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	h.waitFor("run", func() bool { return len(h.outcomeStates()) == 1 })
}

func TestSnapshotErrorMeansNoSpawn(t *testing.T) {
	h := newHarness(t, 1, func(rt *ManagedRuntime) {
		rt.Snapshot = func(context.Context, Item) (Snapshot, error) { return Snapshot{}, errors.New("no provider") }
	})
	h.q.maxRetries = 1
	it := h.ledger.admit(1, "alice", 0)
	h.q.Dispatch(context.Background(), it.ID)
	h.waitFor("exhausted", func() bool { return h.q.ActiveChats() == 0 })
	if h.managed.Load() != 0 || contains(h.ledger.callList(), "reserve") {
		t.Fatalf("snapshot failure reached the ledger or handler: %v", h.ledger.callList())
	}
}

func TestCrashAfterReservationBeforeSpawnRedispatchesWithOriginalSnapshot(t *testing.T) {
	h := newHarness(t, 2, nil)
	it := h.ledger.admit(3, "alice", 0)
	h.ledger.set(it.AttemptID, "reserved", "held") // frozen snapshot: frozen-model
	h.store.setUnfinished(it)
	var seen Snapshot
	h.run = func(ctx context.Context, h *harness, _ Item) error {
		e, _ := ExecutionFrom(ctx)
		seen = e.Snapshot()
		return h.spawnAndExit(ctx, 0)
	}
	if n, err := h.q.Recover(context.Background()); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	h.waitFor("run", func() bool { return len(h.outcomeStates()) == 1 })
	if seen.Model != "frozen-model" {
		t.Fatalf("attempt used snapshot %+v, want the persisted one", seen)
	}
	if h.nativeCalls() != 1 || h.outcomeStates()[0] != StateCompleted {
		t.Fatalf("native=%d outcome=%v", h.nativeCalls(), h.outcomeStates())
	}
}

func TestCrashAfterSpawnNeverInvokesNativeAndHoldsCapacity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(l *fakeLedger, aid string)
	}{
		{"running", func(l *fakeLedger, aid string) { l.set(aid, "running", "held") }},
		{"already unknown", func(l *fakeLedger, aid string) { l.set(aid, StateUnknown, "held") }},
		{"frozen result", func(l *fakeLedger, aid string) { l.set(aid, "running", "held"); l.freeze(aid, "res-1") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 2, nil)
			it := h.ledger.admit(5, "alice", 0)
			tc.setup(h.ledger, it.AttemptID)
			h.store.setUnfinished(it)
			n, err := h.q.Recover(context.Background())
			if err != nil || n != 0 {
				t.Fatalf("Recover scheduled %d: %v", n, err)
			}
			if h.managed.Load() != 0 || h.nativeCalls() != 0 {
				t.Fatal("native execution was invoked for a possibly started attempt")
			}
			if state, res := h.ledger.state(it.AttemptID); state != StateUnknown || res != "held" {
				t.Fatalf("ledger = %s/%s", state, res)
			}
			if h.q.Holds() != 1 {
				t.Fatalf("holds = %d", h.q.Holds())
			}
			if tc.name == "frozen result" && (len(h.frozen) != 1 || h.frozen[0] != "res-1") {
				t.Fatalf("frozen publication not offered: %v", h.frozen)
			}
			if got := h.store.statusOf(5); got != StatusFailed {
				t.Fatalf("turn row = %q, want failed (unknown), never pending/running", got)
			}
			// The held namespace is blocked; another namespace uses the free slot.
			h.q.Enqueue(context.Background(), Item{Folder: "alice", Source: "telegram", Text: "blocked"})
			h.q.Enqueue(context.Background(), Item{Folder: "bob", Source: "telegram", Text: "free"})
			h.waitFor("bob", func() bool { return contains(h.orderList(), "i:free") })
			time.Sleep(30 * time.Millisecond)
			if contains(h.orderList(), "i:blocked") {
				t.Fatal("work admitted into a namespace with an unknown execution")
			}
			// Local exit proof releases the hold and the blocked work runs.
			if err := h.q.ReleaseHold(context.Background(), it.AttemptID, ProofLocalExit, "container gone"); err != nil {
				t.Fatal(err)
			}
			h.waitFor("alice", func() bool { return contains(h.orderList(), "i:blocked") })
			if h.q.Holds() != 0 {
				t.Fatal("hold survived release")
			}
			if h.nativeCalls() != 0 {
				t.Fatal("release authorized a replay")
			}
		})
	}
}

func TestHeldCapacityConsumesTheGlobalBudget(t *testing.T) {
	h := newHarness(t, 1, nil)
	it := h.ledger.admit(5, "alice", 0)
	h.ledger.set(it.AttemptID, "running", "held")
	h.store.setUnfinished(it)
	if _, err := h.q.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.q.Enqueue(context.Background(), Item{Folder: "bob", Source: "telegram", Text: "free"})
	time.Sleep(40 * time.Millisecond)
	if contains(h.orderList(), "i:free") {
		t.Fatal("a freed slot was invented while the only slot is held")
	}
	if err := h.q.ReleaseHold(context.Background(), it.AttemptID, ProofOperatorReconciled, "operator checked"); err != nil {
		t.Fatal(err)
	}
	h.waitFor("bob", func() bool { return contains(h.orderList(), "i:free") })
}

func TestHeldReservationLoadFailureIsFailClosed(t *testing.T) {
	h := newHarness(t, 2, nil)
	it := h.ledger.admit(1, "alice", 0)
	h.store.setUnfinished(it, Item{ID: 9, Folder: "bob", Source: "telegram", Text: "legacy"})
	h.ledger.heldErr = errors.New("db down")
	n, err := h.q.Recover(context.Background())
	if !errors.Is(err, ErrHoldsUnknown) || n != 0 {
		t.Fatalf("Recover = %d, %v", n, err)
	}
	time.Sleep(20 * time.Millisecond)
	if len(h.orderList()) != 0 {
		t.Fatalf("work was scheduled without known holds: %v", h.orderList())
	}
}

func TestNilHandlerErrorIsNeverTaskCompletion(t *testing.T) {
	cases := map[string]struct {
		run  func(ctx context.Context, h *harness, it Item) error
		want string
	}{
		"nil error without exit proof": {func(ctx context.Context, h *harness, _ Item) error {
			e, _ := ExecutionFrom(ctx)
			return e.BeginSpawn(ctx) // attachment ended / timeout: no Exited
		}, StateUnknown},
		"stopped without exit proof": {func(ctx context.Context, h *harness, _ Item) error {
			e, _ := ExecutionFrom(ctx)
			_ = e.BeginSpawn(ctx)
			return fmt.Errorf("wrapped: %w", ErrStopped)
		}, StateUnknown},
		"stopped with exit proof": {func(ctx context.Context, h *harness, _ Item) error {
			e, _ := ExecutionFrom(ctx)
			_ = e.BeginSpawn(ctx)
			e.Exited("container_exec_killed")
			return ErrStopped
		}, StateCancelled},
		"error with exit proof": {func(ctx context.Context, h *harness, _ Item) error {
			e, _ := ExecutionFrom(ctx)
			_ = e.BeginSpawn(ctx)
			e.Exited("container_exec_exit:1")
			return errors.New("claude exited 1")
		}, StateFailed},
		"error without exit proof": {func(ctx context.Context, h *harness, _ Item) error {
			e, _ := ExecutionFrom(ctx)
			_ = e.BeginSpawn(ctx)
			return errors.New("attach stream closed")
		}, StateUnknown},
		"handler never marked a spawn": {func(ctx context.Context, h *harness, _ Item) error {
			return errors.New("ensure container failed") // not ErrNotStarted: no proof
		}, StateUnknown},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, 2, nil)
			h.run = tc.run
			it := h.ledger.admit(1, "alice", 0)
			h.q.Dispatch(context.Background(), it.ID)
			h.waitFor("outcome", func() bool { return len(h.outcomeStates()) == 1 })
			if got := h.outcomeStates()[0]; got != tc.want {
				t.Fatalf("outcome = %s, want %s", got, tc.want)
			}
			if h.managed.Load() != 1 {
				t.Fatalf("handler ran %d times: a started attempt must never be retried", h.managed.Load())
			}
			if len(h.legacy) != 0 {
				t.Fatalf("legacy hooks fired with %v", h.legacy)
			}
			if tc.want == StateUnknown {
				h.waitFor("hold", func() bool { return h.q.Holds() == 1 })
			}
		})
	}
}

func TestNotStartedErrorGetsBoundedPreExecRetriesOnly(t *testing.T) {
	h := newHarness(t, 2, nil)
	h.q.maxRetries = 2
	var calls atomic.Int32
	h.run = func(ctx context.Context, h *harness, _ Item) error {
		if calls.Add(1) < 3 {
			return fmt.Errorf("docker not ready: %w", ErrNotStarted)
		}
		return h.spawnAndExit(ctx, 0)
	}
	it := h.ledger.admit(1, "alice", 0)
	h.q.Dispatch(context.Background(), it.ID)
	h.waitFor("outcome", func() bool { return len(h.outcomeStates()) == 1 })
	if h.outcomeStates()[0] != StateCompleted || calls.Load() != 3 || h.nativeCalls() != 1 {
		t.Fatalf("outcome=%v calls=%d native=%d", h.outcomeStates(), calls.Load(), h.nativeCalls())
	}

	// Exhausted pre-exec retries leave the attempt reserved and durable.
	g := newHarness(t, 2, nil)
	g.q.maxRetries = 1
	g.run = func(context.Context, *harness, Item) error { return ErrNotStarted }
	it2 := g.ledger.admit(2, "alice", 0)
	g.q.Dispatch(context.Background(), it2.ID)
	g.waitFor("stop", func() bool { return g.q.ActiveChats() == 0 })
	if g.managed.Load() != 2 || g.nativeCalls() != 0 {
		t.Fatalf("handler=%d native=%d", g.managed.Load(), g.nativeCalls())
	}
	if state, _ := g.ledger.state(it2.AttemptID); state != "reserved" {
		t.Fatalf("state = %s, want reserved (still proven unstarted)", state)
	}
}

func TestSpawnMarkerRefusedSettlesNotStarted(t *testing.T) {
	h := newHarness(t, 2, nil)
	it := h.ledger.admit(1, "alice", 0)
	h.run = func(ctx context.Context, h *harness, _ Item) error {
		// A stop request wins between reservation and spawn.
		h.ledger.set(it.AttemptID, "cancel_requested", "held")
		e, _ := ExecutionFrom(ctx)
		return e.BeginSpawn(ctx)
	}
	h.q.Dispatch(context.Background(), it.ID)
	h.waitFor("outcome", func() bool { return len(h.outcomeStates()) == 1 })
	if h.nativeCalls() != 0 || h.q.Holds() != 0 {
		t.Fatalf("native=%d holds=%d", h.nativeCalls(), h.q.Holds())
	}
	if !contains(h.ledger.callList(), "settle:not_started") {
		t.Fatalf("calls = %v", h.ledger.callList())
	}
}

func TestAmbiguousSpawnMarkerErrorIsTreatedAsStarted(t *testing.T) {
	h := newHarness(t, 2, nil)
	h.ledger.beginErr = errors.New("commit status unknown")
	it := h.ledger.admit(1, "alice", 0)
	h.q.Dispatch(context.Background(), it.ID)
	h.waitFor("hold", func() bool { return h.q.Holds() == 1 })
	if h.managed.Load() != 1 {
		t.Fatal("ambiguous marker was retried")
	}
}

func TestLedgerSettleFailureHoldsAndNeverCompletes(t *testing.T) {
	h := newHarness(t, 2, nil)
	h.ledger.settleErr = errors.New("db down")
	it := h.ledger.admit(1, "alice", 0)
	h.q.Dispatch(context.Background(), it.ID)
	h.waitFor("hold", func() bool { return h.q.Holds() == 1 })
	for _, s := range h.outcomeStates() {
		if s == StateCompleted {
			t.Fatal("completed without a ledger record")
		}
	}
}

func TestShutdownDuringRun(t *testing.T) {
	t.Run("before spawn leaves the attempt for recovery", func(t *testing.T) {
		h := newHarness(t, 2, nil)
		in := make(chan struct{})
		h.run = func(ctx context.Context, h *harness, _ Item) error {
			close(in)
			<-ctx.Done()
			return ctx.Err()
		}
		it := h.ledger.admit(1, "alice", 0)
		h.q.Dispatch(context.Background(), it.ID)
		<-in
		h.q.Close()
		if state, _ := h.ledger.state(it.AttemptID); state != "reserved" {
			t.Fatalf("state = %s", state)
		}
		if got := h.store.statusOf(1); got != StatusInterrupted {
			t.Fatalf("turn = %s", got)
		}
	})
	t.Run("after spawn without exit proof is unknown", func(t *testing.T) {
		h := newHarness(t, 2, nil)
		in := make(chan struct{})
		h.run = func(ctx context.Context, h *harness, _ Item) error {
			e, _ := ExecutionFrom(ctx)
			_ = e.BeginSpawn(ctx)
			close(in)
			<-ctx.Done()
			return nil // swallowed cancellation
		}
		it := h.ledger.admit(1, "alice", 0)
		h.q.Dispatch(context.Background(), it.ID)
		<-in
		h.q.Close()
		if state, res := h.ledger.state(it.AttemptID); state != StateUnknown || res != "held" {
			t.Fatalf("ledger = %s/%s", state, res)
		}
	})
	t.Run("after spawn with exit proof is interrupted", func(t *testing.T) {
		h := newHarness(t, 2, nil)
		in := make(chan struct{})
		h.run = func(ctx context.Context, h *harness, _ Item) error {
			e, _ := ExecutionFrom(ctx)
			_ = e.BeginSpawn(ctx)
			close(in)
			<-ctx.Done()
			e.Exited("container_exec_killed")
			return ctx.Err()
		}
		it := h.ledger.admit(1, "alice", 0)
		h.q.Dispatch(context.Background(), it.ID)
		<-in
		h.q.Close()
		if state, _ := h.ledger.state(it.AttemptID); state != StateInterrupted {
			t.Fatalf("state = %s", state)
		}
	})
}

func TestDispatchIsIdempotentAndNeverReclassifiesLiveWork(t *testing.T) {
	h := newHarness(t, 2, nil)
	in, out := make(chan struct{}), make(chan struct{})
	h.run = func(ctx context.Context, h *harness, _ Item) error {
		e, _ := ExecutionFrom(ctx)
		_ = e.BeginSpawn(ctx)
		close(in)
		<-out
		e.Exited("container_exec_exit:0")
		return nil
	}
	it := h.ledger.admit(1, "alice", 0)
	if ok, _ := h.q.Dispatch(context.Background(), it.ID); !ok {
		t.Fatal("first dispatch")
	}
	<-in
	// Dispatch while the attempt is running must not turn it into unknown.
	if ok, err := h.q.Dispatch(context.Background(), it.ID); ok || err != nil {
		t.Fatalf("second dispatch = %v, %v", ok, err)
	}
	if state, _ := h.ledger.state(it.AttemptID); state != "running" {
		t.Fatalf("live attempt reclassified: %s", state)
	}
	close(out)
	h.waitFor("done", func() bool { return len(h.outcomeStates()) == 1 })
	if h.managed.Load() != 1 {
		t.Fatal("ran twice")
	}
	if _, err := h.q.Dispatch(context.Background(), 999); err == nil {
		t.Fatal("dispatch of an unknown turn succeeded")
	}
}

func TestInspectOnlyRuntimeStillHoldsAndNeverRuns(t *testing.T) {
	h := newHarness(t, 2, func(rt *ManagedRuntime) { rt.Handler, rt.Snapshot = nil, nil })
	queued := h.ledger.admit(1, "alice", 0)
	running := h.ledger.admit(2, "bob", 0)
	h.ledger.set(running.AttemptID, "running", "held")
	h.store.setUnfinished(queued, running)
	n, err := h.q.Recover(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("Recover = %d, %v", n, err)
	}
	if h.q.Holds() != 1 || h.managed.Load() != 0 {
		t.Fatalf("holds=%d handler=%d", h.q.Holds(), h.managed.Load())
	}
	if state, _ := h.ledger.state(queued.AttemptID); state != "queued" {
		t.Fatalf("queued attempt changed: %s", state)
	}
}

func TestRecoverKeepsLegacyAndManagedOrderTogether(t *testing.T) {
	h := newHarness(t, 1, nil)
	m := h.ledger.admit(2, "alice", 0)
	h.store.setUnfinished(Item{ID: 1, Folder: "alice", Source: "telegram", Text: "first"}, m,
		Item{ID: 3, Folder: "alice", Source: "telegram", Text: "last"})
	if n, err := h.q.Recover(context.Background()); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	h.waitFor("all", func() bool { return len(h.orderList()) == 3 })
	if got := fmt.Sprint(h.orderList()); got != "[i:first m:2 i:last]" {
		t.Fatalf("order = %s", got)
	}
}

func TestConcurrentDispatchAndEnqueueRace(t *testing.T) {
	h := newHarness(t, 3, nil)
	var wg sync.WaitGroup
	for i := 1; i <= 12; i++ {
		it := h.ledger.admit(int64(i), fmt.Sprintf("f%d", i%3), 0)
		wg.Add(2)
		go func() { defer wg.Done(); h.q.Dispatch(context.Background(), it.ID) }()
		go func() {
			defer wg.Done()
			h.q.Enqueue(context.Background(), Item{Folder: it.Folder, Source: "telegram", Text: fmt.Sprint("t", it.ID)})
		}()
	}
	wg.Wait()
	h.waitFor("managed", func() bool { return len(h.outcomeStates()) == 12 })
	if h.nativeCalls() != 12 {
		t.Fatalf("native = %d", h.nativeCalls())
	}
}
