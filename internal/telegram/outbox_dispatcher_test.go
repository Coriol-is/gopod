package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// fakeTransport scripts API outcomes and records every call. It stands in for
// Telegram: there is no network anywhere in these tests.
type fakeTransport struct {
	mu       sync.Mutex
	calls    []SendRequest
	times    []time.Time
	script   func(n int, req SendRequest) Outcome
	hold     func(ctx context.Context, req SendRequest) // optional, runs inside the call
	inflight map[int64]int
	maxIn    map[int64]int
	total    int
	maxTotal int
}

func (f *fakeTransport) Attempt(ctx context.Context, req SendRequest) Outcome {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.times = append(f.times, time.Now())
	n := len(f.calls)
	if f.inflight == nil {
		f.inflight, f.maxIn = map[int64]int{}, map[int64]int{}
	}
	f.inflight[req.ChatID]++
	f.maxIn[req.ChatID] = max(f.maxIn[req.ChatID], f.inflight[req.ChatID])
	f.total++
	f.maxTotal = max(f.maxTotal, f.total)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inflight[req.ChatID]--
		f.total--
		f.mu.Unlock()
	}()
	if f.hold != nil {
		f.hold(ctx, req)
	}
	if f.script == nil {
		return Outcome{Kind: OutcomeAck, RemoteMessageID: int64(1000 + n)}
	}
	return f.script(n, req)
}

func (f *fakeTransport) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTransport) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.Text)
	}
	return out
}

func ack(n int) Outcome { return Outcome{Kind: OutcomeAck, RemoteMessageID: int64(5000 + n)} }

type outboxFixture struct {
	t     *testing.T
	st    *store.Store
	owner *store.OutboxOwner
	conv  store.ConversationScope
}

func newOutboxFixture(t *testing.T) *outboxFixture {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "store.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &outboxFixture{t: t, st: st}
	f.conv = f.register("tg:1", "owner", 0)
	f.owner, err = st.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.owner.Release)
	return f
}

func (f *outboxFixture) register(jid, folder string, thread int) store.ConversationScope {
	f.t.Helper()
	if err := f.st.RegisterChat(f.t.Context(), store.RegisteredChat{JID: jid, Folder: folder, IsOwner: folder == "owner", AddedAt: 1}); err != nil {
		f.t.Fatal(err)
	}
	c, err := f.st.SnapshotConversation(f.t.Context(), jid, thread, store.VisibilityConversation)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func (f *outboxFixture) publish(conv store.ConversationScope, texts ...string) store.Delivery {
	f.t.Helper()
	return f.publishExpiring(conv, 0, texts...)
}

func (f *outboxFixture) publishExpiring(conv store.ConversationScope, expiresAt int64, texts ...string) store.Delivery {
	f.t.Helper()
	var parts []store.DeliveryPart
	for _, s := range texts {
		parts = append(parts, store.DeliveryPart{Kind: store.PartText, Payload: []byte(s), ParseMode: "HTML"})
	}
	d, err := f.st.PublishDelivery(f.t.Context(), store.PublishRequest{Conversation: conv, Owner: store.OwnerControllerResult, Parts: parts, ExpiresAt: expiresAt})
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func (f *outboxFixture) diag(id string) store.DeliveryDiagnostic {
	f.t.Helper()
	d, err := f.st.DeliveryDiagnostic(f.t.Context(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func fastCfg() OutboxConfig {
	return OutboxConfig{
		Backoff:        []time.Duration{5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond},
		MaxIdle:        20 * time.Millisecond,
		AttemptTimeout: 5 * time.Second,
		Jitter:         func(time.Duration) time.Duration { return 0 },
	}
}

func (f *outboxFixture) start(backend outboxBackend, tr OutboxTransport, cfg OutboxConfig) *OutboxDispatcher {
	f.t.Helper()
	d := NewOutboxDispatcher(backend, tr, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := d.Start(context.Background()); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = d.Stop(2 * time.Second) })
	return d
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (f *outboxFixture) waitState(id, state string) store.DeliveryDiagnostic {
	f.t.Helper()
	eventually(f.t, "delivery "+state, func() bool { return f.diag(id).State == state })
	return f.diag(id)
}

// quiet asserts nothing else is sent: used after a terminal or uncertain outcome.
func quiet(t *testing.T, tr *fakeTransport, want int) {
	t.Helper()
	time.Sleep(150 * time.Millisecond)
	if got := tr.callCount(); got != want {
		t.Fatalf("transport calls = %d, want %d (hidden retry or reroute): %v", got, want, tr.texts())
	}
}

func TestSendsAllPartsInOrderWithStoredDestination(t *testing.T) {
	f := newOutboxFixture(t)
	conv := f.register("tg:7", "seven", 42)
	tr := &fakeTransport{}
	f.start(f.owner, tr, fastCfg())
	d := f.publish(conv, "a", "b", "c")
	f.st.NotifyPublished(d.ID)
	diag := f.waitState(d.ID, store.DeliverySent)
	if diag.Acknowledged != 3 {
		t.Fatalf("diag %+v", diag)
	}
	if got := fmt.Sprint(tr.texts()); got != "[a b c]" {
		t.Fatalf("order %s", got)
	}
	for _, c := range tr.calls {
		if c.ChatID != 7 || c.Thread != 42 || c.Kind != store.PartText || c.ParseMode != "HTML" {
			t.Fatalf("request %+v", c)
		}
	}
}

func TestNotifierWakesIdleWorkerAfterPublishCommit(t *testing.T) {
	f := newOutboxFixture(t)
	tr := &fakeTransport{}
	cfg := fastCfg()
	cfg.MaxIdle = time.Hour
	d := f.start(f.owner, tr, cfg)
	f.st.SetPublishNotifier(d.Notify)
	time.Sleep(50 * time.Millisecond) // worker is idle on a one-hour timer
	pub := f.publish(f.conv, "hello")
	f.waitState(pub.ID, store.DeliverySent)
}

func TestRateLimitDelayThenSuccess(t *testing.T) {
	f := newOutboxFixture(t)
	tr := &fakeTransport{script: func(n int, _ SendRequest) Outcome {
		if n == 1 {
			return Outcome{Kind: OutcomeRateLimited, RetryAfter: 120 * time.Millisecond, Code: "rate_limited", HTTPStatus: 429}
		}
		return ack(n)
	}}
	f.start(f.owner, tr, fastCfg())
	d := f.publish(f.conv, "a")
	f.waitState(d.ID, store.DeliverySent)
	if tr.callCount() != 2 {
		t.Fatalf("calls %d", tr.callCount())
	}
	if gap := tr.times[1].Sub(tr.times[0]); gap < 100*time.Millisecond {
		t.Fatalf("retry_after not honored: %v", gap)
	}
}

func TestRateLimitedUntilExpiryExpiresWithoutFurtherCalls(t *testing.T) {
	f := newOutboxFixture(t)
	tr := &fakeTransport{script: func(int, SendRequest) Outcome {
		return Outcome{Kind: OutcomeRateLimited, RetryAfter: time.Minute, Code: "rate_limited", HTTPStatus: 429}
	}}
	f.start(f.owner, tr, fastCfg())
	d := f.publishExpiring(f.conv, time.Now().Add(2*time.Second).UnixMilli(), "a")
	diag := f.waitState(d.ID, store.DeliveryExpired)
	if diag.Unresolved || diag.Parts[0].ReasonCode != "retry_beyond_expiry" {
		t.Fatalf("diag %+v", diag)
	}
	quiet(t, tr, 1)
}

func TestRateLimitBudgetIsFiveAttempts(t *testing.T) {
	f := newOutboxFixture(t)
	tr := &fakeTransport{script: func(int, SendRequest) Outcome {
		return Outcome{Kind: OutcomeRateLimited, RetryAfter: time.Millisecond, Code: "rate_limited", HTTPStatus: 429}
	}}
	f.start(f.owner, tr, fastCfg())
	d := f.publish(f.conv, "a")
	diag := f.waitState(d.ID, store.DeliveryFailed)
	if diag.Parts[0].Attempts != store.MaxPartAttempts || diag.Parts[0].ReasonCode != "retry_budget_exhausted" {
		t.Fatalf("diag %+v", diag)
	}
	quiet(t, tr, store.MaxPartAttempts)
}

func TestProvenUnsentFailureRetriesThenSucceeds(t *testing.T) {
	f := newOutboxFixture(t)
	tr := &fakeTransport{script: func(n int, _ SendRequest) Outcome {
		if n < 3 {
			return Outcome{Kind: OutcomeNotSent, Code: "connect_failed"}
		}
		return ack(n)
	}}
	f.start(f.owner, tr, fastCfg())
	d := f.publish(f.conv, "a")
	f.waitState(d.ID, store.DeliverySent)
	if tr.callCount() != 3 {
		t.Fatalf("calls %d", tr.callCount())
	}
}

func TestExplicitRefusalFailsOnceAndCancelsLaterParts(t *testing.T) {
	f := newOutboxFixture(t)
	tr := &fakeTransport{script: func(n int, _ SendRequest) Outcome {
		if n == 2 {
			return Outcome{Kind: OutcomeRefused, Code: "forbidden", HTTPStatus: 403}
		}
		return ack(n)
	}}
	f.start(f.owner, tr, fastCfg())
	d := f.publish(f.conv, "a", "b", "c")
	diag := f.waitState(d.ID, store.DeliveryPartial)
	if diag.Unresolved || diag.Parts[0].State != store.PartSent || diag.Parts[1].State != store.PartFailed || diag.Parts[2].State != store.PartCancelled {
		t.Fatalf("diag %+v", diag)
	}
	quiet(t, tr, 2)
}

func TestGeneric5xxBecomesUncertainAndIsNeverRetried(t *testing.T) {
	f := newOutboxFixture(t)
	tr := &fakeTransport{script: func(int, SendRequest) Outcome {
		return Outcome{Kind: OutcomeAmbiguous, Code: "server_error", HTTPStatus: 502}
	}}
	f.start(f.owner, tr, fastCfg())
	d := f.publish(f.conv, "a", "b")
	diag := f.waitState(d.ID, store.DeliveryUncertain)
	if !diag.Unresolved || diag.Parts[0].ReasonCode != "server_error" || diag.Parts[1].State != store.PartPending {
		t.Fatalf("diag %+v", diag)
	}
	quiet(t, tr, 1)
}

func TestAckWithoutRemoteIDIsTreatedAsMalformedReply(t *testing.T) {
	f := newOutboxFixture(t)
	tr := &fakeTransport{script: func(int, SendRequest) Outcome { return Outcome{Kind: OutcomeAck} }}
	f.start(f.owner, tr, fastCfg())
	d := f.publish(f.conv, "a")
	diag := f.waitState(d.ID, store.DeliveryUncertain)
	if diag.Parts[0].ReasonCode != "malformed_reply" {
		t.Fatalf("diag %+v", diag)
	}
	quiet(t, tr, 1)
}

func TestTransportPanicIsUncertain(t *testing.T) {
	f := newOutboxFixture(t)
	tr := &fakeTransport{script: func(int, SendRequest) Outcome { panic("boom") }}
	f.start(f.owner, tr, fastCfg())
	d := f.publish(f.conv, "a")
	diag := f.waitState(d.ID, store.DeliveryUncertain)
	if diag.Parts[0].ReasonCode != "transport_panic" {
		t.Fatalf("diag %+v", diag)
	}
}

// crashBackend simulates a process death: once armed, every persistence call
// fails, exactly as if the process were gone after the API call returned.
type crashBackend struct {
	*store.OutboxOwner
	armAfterAcks int32 // acks allowed before the crash; -1 = crash before the first ack
	acks         atomic.Int32
	crashed      atomic.Bool
}

var errCrashed = errors.New("simulated crash")

func (c *crashBackend) AckPart(ctx context.Context, cl store.OutboxClaim, id int64) error {
	if c.crashed.Load() || c.acks.Load() >= c.armAfterAcks {
		c.crashed.Store(true)
		return errCrashed
	}
	c.acks.Add(1)
	return c.OutboxOwner.AckPart(ctx, cl, id)
}

func (c *crashBackend) MarkUncertain(ctx context.Context, cl store.OutboxClaim, r, d string) error {
	if c.crashed.Load() {
		return errCrashed
	}
	return c.OutboxOwner.MarkUncertain(ctx, cl, r, d)
}

func (f *outboxFixture) restart(tr OutboxTransport) *OutboxDispatcher {
	f.t.Helper()
	f.owner.Release()
	var err error
	if f.owner, err = f.st.AcquireOutboxOwner(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(f.owner.Release)
	return f.start(f.owner, tr, fastCfg())
}

func TestCrashAfterRemoteAcceptanceBeforeDBAckIsUncertainNotResent(t *testing.T) {
	f := newOutboxFixture(t)
	tr := &fakeTransport{}
	crash := &crashBackend{OutboxOwner: f.owner, armAfterAcks: 0}
	d1 := f.start(crash, tr, fastCfg())
	d := f.publish(f.conv, "a", "b")
	eventually(t, "crash", crash.crashed.Load)
	time.Sleep(50 * time.Millisecond)
	if err := d1.Stop(time.Second); err != nil {
		t.Fatal(err)
	}
	if st := f.diag(d.ID); st.Parts[0].State != store.PartSending {
		t.Fatalf("expected the crash to leave sending, got %+v", st)
	}

	f.restart(tr) // new process: sweep must not resend
	diag := f.waitState(d.ID, store.DeliveryUncertain)
	if diag.Parts[0].ReasonCode != "restart_while_sending" || diag.Parts[1].State != store.PartPending {
		t.Fatalf("diag %+v", diag)
	}
	quiet(t, tr, 1)
}

func TestCrashAfterPartAckDoesNotResendAcknowledgedPrefix(t *testing.T) {
	f := newOutboxFixture(t)
	tr := &fakeTransport{}
	crash := &crashBackend{OutboxOwner: f.owner, armAfterAcks: 1}
	d1 := f.start(crash, tr, fastCfg())
	d := f.publish(f.conv, "a", "b", "c")
	eventually(t, "crash", crash.crashed.Load)
	if err := d1.Stop(time.Second); err != nil {
		t.Fatal(err)
	}

	f.restart(tr)
	diag := f.waitState(d.ID, store.DeliveryUncertain)
	if diag.AckedPrefix != 1 || diag.Parts[1].State != store.PartUncertain || diag.Parts[2].State != store.PartPending {
		t.Fatalf("diag %+v", diag)
	}
	quiet(t, tr, 2)
	if got := fmt.Sprint(tr.texts()); got != "[a b]" {
		t.Fatalf("sent %s", got)
	}
}

func TestStaleRegistrationRevisionIsNeverSent(t *testing.T) {
	f := newOutboxFixture(t)
	d := f.publish(f.conv, "a")
	if err := f.st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	tr := &fakeTransport{}
	f.start(f.owner, tr, fastCfg())
	diag := f.waitState(d.ID, store.DeliveryCancelled)
	if diag.Parts[0].ReasonCode != "scope_stale" {
		t.Fatalf("diag %+v", diag)
	}
	quiet(t, tr, 0)
}

func TestRetiredTopicRefusalNeverReroutesToMainTopic(t *testing.T) {
	f := newOutboxFixture(t)
	conv := f.register("tg:9", "nine", 77)
	tr := &fakeTransport{script: func(int, SendRequest) Outcome {
		return Outcome{Kind: OutcomeRefused, Code: "thread_gone", HTTPStatus: 400}
	}}
	f.start(f.owner, tr, fastCfg())
	d := f.publish(conv, "a", "b")
	diag := f.waitState(d.ID, store.DeliveryFailed)
	if diag.Parts[0].ReasonCode != "thread_gone" || diag.Parts[1].State != store.PartCancelled {
		t.Fatalf("diag %+v", diag)
	}
	quiet(t, tr, 1)
	for _, c := range tr.calls {
		if c.Thread != 77 {
			t.Fatalf("request left the stored topic: %+v", c)
		}
	}
}

func TestPerConversationOrderingWithParallelConversations(t *testing.T) {
	f := newOutboxFixture(t)
	convB := f.register("tg:2", "two", 0)
	tr := &fakeTransport{hold: func(context.Context, SendRequest) { time.Sleep(25 * time.Millisecond) }}
	cfg := fastCfg()
	cfg.Workers = 3
	f.start(f.owner, tr, cfg)
	a := f.publish(f.conv, "a1", "a2", "a3")
	b := f.publish(convB, "b1", "b2", "b3")
	f.waitState(a.ID, store.DeliverySent)
	f.waitState(b.ID, store.DeliverySent)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.maxIn[1] != 1 || tr.maxIn[2] != 1 {
		t.Fatalf("more than one in-flight part in a conversation: %v", tr.maxIn)
	}
	if tr.maxTotal < 2 {
		t.Fatal("conversations did not progress in parallel")
	}
	seq := map[int64][]string{}
	for _, c := range tr.calls {
		seq[c.ChatID] = append(seq[c.ChatID], c.Text)
	}
	if fmt.Sprint(seq[1]) != "[a1 a2 a3]" || fmt.Sprint(seq[2]) != "[b1 b2 b3]" {
		t.Fatalf("order %v", seq)
	}
}

func TestUncertainHeadBlocksOnlyItsOwnConversation(t *testing.T) {
	f := newOutboxFixture(t)
	convB := f.register("tg:2", "two", 0)
	tr := &fakeTransport{script: func(n int, req SendRequest) Outcome {
		if req.Text == "a1" {
			return Outcome{Kind: OutcomeAmbiguous, Code: "timeout"}
		}
		return ack(n)
	}}
	f.start(f.owner, tr, fastCfg())
	a := f.publish(f.conv, "a1")
	a2 := f.publish(f.conv, "a2")
	b := f.publish(convB, "b1", "b2")
	f.waitState(b.ID, store.DeliverySent)
	f.waitState(a.ID, store.DeliveryUncertain)
	quiet(t, tr, 3)
	if d := f.diag(a2.ID); d.State != store.DeliveryPending {
		t.Fatalf("later delivery behind an uncertain head: %+v", d)
	}
}

func TestShutdownIsBoundedWhenTransportIgnoresContext(t *testing.T) {
	f := newOutboxFixture(t)
	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{}, 1)
	tr := &fakeTransport{hold: func(context.Context, SendRequest) {
		entered <- struct{}{}
		<-release // ignores ctx entirely
	}}
	d := f.start(f.owner, tr, fastCfg())
	pub := f.publish(f.conv, "a")
	<-entered
	begin := time.Now()
	if err := d.Stop(400 * time.Millisecond); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("shutdown took %v", took)
	}
	diag := f.diag(pub.ID)
	if diag.Parts[0].State != store.PartUncertain {
		t.Fatalf("interrupted attempt must be uncertain: %+v", diag)
	}
}

// hungBackend never returns from ClaimNext, to prove Stop gives up on time.
type hungBackend struct {
	*store.OutboxOwner
	release chan struct{}
	entered chan struct{}
}

func (h *hungBackend) ClaimNext(context.Context, time.Time) (store.OutboxClaim, bool, error) {
	select {
	case h.entered <- struct{}{}:
	default:
	}
	<-h.release
	return store.OutboxClaim{}, false, nil
}

func TestStopReportsTimeoutForAWorkerThatCannotBeInterrupted(t *testing.T) {
	f := newOutboxFixture(t)
	hung := &hungBackend{OutboxOwner: f.owner, release: make(chan struct{}), entered: make(chan struct{}, 1)}
	defer close(hung.release)
	d := f.start(hung, &fakeTransport{}, fastCfg())
	<-hung.entered
	begin := time.Now()
	err := d.Stop(200 * time.Millisecond)
	if !errors.Is(err, ErrOutboxShutdownTimeout) {
		t.Fatalf("stop: %v", err)
	}
	if took := time.Since(begin); took > time.Second {
		t.Fatalf("stop not bounded: %v", took)
	}
}

func TestStopBeforeStartIsNoop(t *testing.T) {
	f := newOutboxFixture(t)
	d := NewOutboxDispatcher(f.owner, &fakeTransport{}, fastCfg(), nil)
	if err := d.Stop(time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestSupersededControllerInstanceStopsWithoutSending(t *testing.T) {
	f := newOutboxFixture(t)
	d := f.publish(f.conv, "a")
	old := f.owner
	old.Release()
	newer, err := f.st.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(newer.Release)
	tr := &fakeTransport{}
	disp := NewOutboxDispatcher(old, tr, fastCfg(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := disp.Start(t.Context()); err == nil {
		_ = disp.Stop(time.Second)
		t.Fatal("a superseded owner must not start")
	}
	quiet(t, tr, 0)
	if f.diag(d.ID).State != store.DeliveryPending {
		t.Fatal("superseded owner changed state")
	}
}
