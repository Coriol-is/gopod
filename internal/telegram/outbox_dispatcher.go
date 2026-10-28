package telegram

// Durable-outbox dispatcher (CP-002 PR 2). Disabled by default.
//
// Per attempt: claim (commit sending + attempt + fence), ONE transport call
// with no SQL transaction open, then one completion commit. The dispatcher
// never calls the transport twice for the same claim, never changes the
// destination, and treats anything it cannot prove unsent as uncertain.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// OutboxTransport makes exactly one API attempt per call (see outbox_transport.go).
type OutboxTransport interface {
	Attempt(ctx context.Context, req SendRequest) Outcome
}

// outboxBackend is the persistence the dispatcher needs; *store.OutboxOwner
// implements it. Declared here, at the consumer.
type outboxBackend interface {
	ClaimNext(ctx context.Context, now time.Time) (store.OutboxClaim, bool, error)
	AckPart(ctx context.Context, c store.OutboxClaim, remoteMessageID int64) error
	ReleaseUnsent(ctx context.Context, c store.OutboxClaim, nextAt time.Time, reason, diagnostic string, refund bool) (string, error)
	FailPart(ctx context.Context, c store.OutboxClaim, reason, diagnostic string) error
	MarkUncertain(ctx context.Context, c store.OutboxClaim, reason, diagnostic string) error
	SweepSending(ctx context.Context) (int, error)
	NextDue(ctx context.Context, now time.Time) (time.Time, bool, error)
}

var _ outboxBackend = (*store.OutboxOwner)(nil)

// ErrOutboxShutdownTimeout means workers were still running when Stop gave up.
var ErrOutboxShutdownTimeout = errors.New("telegram: outbox workers did not stop in time")

// OutboxConfig tunes the dispatcher. Zero values take the defaults.
type OutboxConfig struct {
	// Workers is the number of concurrent workers (default 1, at most 4). The
	// per-conversation rule holds for any value: the store never hands out two
	// parts of one conversation.
	Workers int
	// AttemptTimeout bounds one API attempt (default 30s). Hitting it is ambiguous.
	AttemptTimeout time.Duration
	// Backoff is the known-unsent retry delay by attempt (default 1s, 5s, 15s, 60s).
	Backoff []time.Duration
	// MaxIdle caps how long an idle worker sleeps between polls (default 30s).
	MaxIdle time.Duration
	// Now and Jitter are test seams. Jitter returns the extra delay added to d.
	Now    func() time.Time
	Jitter func(d time.Duration) time.Duration
}

func (c OutboxConfig) withDefaults() OutboxConfig {
	c.Workers = min(max(c.Workers, 1), 4)
	if c.AttemptTimeout <= 0 {
		c.AttemptTimeout = 30 * time.Second
	}
	if len(c.Backoff) == 0 {
		c.Backoff = []time.Duration{time.Second, 5 * time.Second, 15 * time.Second, 60 * time.Second}
	}
	if c.MaxIdle <= 0 {
		c.MaxIdle = 30 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Jitter == nil {
		c.Jitter = func(d time.Duration) time.Duration {
			if d <= 0 {
				return 0
			}
			return time.Duration(rand.Int64N(int64(d)/5 + 1)) // up to +20%
		}
	}
	return c
}

// OutboxDispatcher drains the durable outbox.
type OutboxDispatcher struct {
	backend   outboxBackend
	transport OutboxTransport
	log       *slog.Logger
	cfg       OutboxConfig

	mu            sync.Mutex
	wake          chan struct{}
	started       bool
	loopCancel    context.CancelFunc
	attemptCancel context.CancelFunc
	done          chan struct{}
}

// NewOutboxDispatcher builds a dispatcher. Nothing runs until Start.
func NewOutboxDispatcher(backend outboxBackend, transport OutboxTransport, cfg OutboxConfig, log *slog.Logger) *OutboxDispatcher {
	if log == nil {
		log = slog.Default()
	}
	return &OutboxDispatcher{backend: backend, transport: transport, log: log, cfg: cfg.withDefaults(), wake: make(chan struct{})}
}

// Notify wakes idle workers. Safe to register as the store publish notifier
// (non-blocking; called only after the publishing commit).
func (d *OutboxDispatcher) Notify(string) {
	d.mu.Lock()
	close(d.wake)
	d.wake = make(chan struct{})
	d.mu.Unlock()
}

func (d *OutboxDispatcher) wakeChan() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.wake
}

// Start runs the restart sweep (every part left sending becomes uncertain and
// is never resent) and then launches the workers. ctx ends claiming; an attempt
// already in flight finishes, bounded by AttemptTimeout, unless Stop cancels it.
func (d *OutboxDispatcher) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return errors.New("telegram: outbox dispatcher already started")
	}
	d.started = true
	d.mu.Unlock()

	n, err := d.backend.SweepSending(ctx)
	if err != nil {
		d.mu.Lock()
		d.started = false
		d.mu.Unlock()
		return fmt.Errorf("outbox restart sweep: %w", err)
	}
	if n > 0 {
		d.log.Warn("outbox restart sweep marked unacknowledged parts uncertain", slog.Int("parts", n))
	}

	loopCtx, loopCancel := context.WithCancel(ctx)
	attemptCtx, attemptCancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	d.mu.Lock()
	d.loopCancel, d.attemptCancel, d.done = loopCancel, attemptCancel, done
	d.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < d.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.work(loopCtx, attemptCtx)
		}()
	}
	go func() {
		wg.Wait()
		attemptCancel()
		close(done)
	}()
	return nil
}

// Stop ends claiming, gives an in-flight attempt half of timeout to finish,
// then cancels it and waits the rest. A cancelled attempt is persisted as
// uncertain (it may already have been accepted). It returns
// ErrOutboxShutdownTimeout if workers are still running afterwards; whatever
// stays sending is turned into uncertain by the next start's sweep.
func (d *OutboxDispatcher) Stop(timeout time.Duration) error {
	d.mu.Lock()
	loopCancel, attemptCancel, done := d.loopCancel, d.attemptCancel, d.done
	d.mu.Unlock()
	if done == nil {
		return nil
	}
	loopCancel()
	grace := time.NewTimer(timeout / 2)
	defer grace.Stop()
	select {
	case <-done:
		return nil
	case <-grace.C:
	}
	attemptCancel()
	rest := time.NewTimer(timeout - timeout/2)
	defer rest.Stop()
	select {
	case <-done:
		return nil
	case <-rest.C:
		return ErrOutboxShutdownTimeout
	}
}

func (d *OutboxDispatcher) work(loopCtx, attemptCtx context.Context) {
	for loopCtx.Err() == nil {
		wake := d.wakeChan() // snapshot before claiming so a publish in between is not lost
		progressed, err := d.step(loopCtx, attemptCtx)
		switch {
		case errors.Is(err, store.ErrOutboxFenced):
			d.log.Error("outbox worker stopped: another controller instance owns the outbox")
			return
		case err != nil && loopCtx.Err() == nil:
			d.log.Error("outbox step failed", slog.Any("err", err))
			d.sleep(loopCtx, wake, time.Second)
		case progressed:
		default:
			d.idle(loopCtx, wake)
		}
	}
}

func (d *OutboxDispatcher) idle(ctx context.Context, wake <-chan struct{}) {
	wait := d.cfg.MaxIdle
	if due, ok, err := d.backend.NextDue(ctx, d.cfg.Now()); err == nil && ok {
		wait = min(wait, max(due.Sub(d.cfg.Now()), time.Millisecond))
	}
	d.sleep(ctx, wake, wait)
}

func (d *OutboxDispatcher) sleep(ctx context.Context, wake <-chan struct{}, wait time.Duration) {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-wake:
	case <-t.C:
	}
}

// step claims and fully processes at most one part.
func (d *OutboxDispatcher) step(loopCtx, attemptCtx context.Context) (bool, error) {
	claim, ok, err := d.backend.ClaimNext(loopCtx, d.cfg.Now())
	if err != nil || !ok {
		return false, err
	}
	d.execute(attemptCtx, claim)
	return true, nil
}

func (d *OutboxDispatcher) execute(attemptCtx context.Context, c store.OutboxClaim) {
	chatID, err := chatIDFromJID(c.Conversation.Scope.JID)
	if err != nil {
		d.persist(c, Outcome{Kind: OutcomeRefused, Code: "bad_destination"})
		return
	}
	req := SendRequest{
		Kind: c.Part.Kind, ChatID: chatID, Thread: c.Conversation.ThreadID,
		Text: string(c.Part.Payload), ParseMode: c.Part.ParseMode, ReplyMarkup: c.Part.ReplyMarkup,
		TargetMessageID: c.Part.TargetMessageID, ArtifactRef: c.Part.ArtifactRef,
	}
	d.persist(c, d.attempt(attemptCtx, req))
}

// attempt makes the single transport call. A transport that ignores its
// context cannot hang the worker: when the deadline passes the call is
// abandoned and reported ambiguous, because it may still complete remotely.
func (d *OutboxDispatcher) attempt(ctx context.Context, req SendRequest) Outcome {
	if ctx.Err() != nil {
		return Outcome{Kind: OutcomeNotSent, Code: "canceled_before_dispatch"}
	}
	ctx, cancel := context.WithTimeout(ctx, d.cfg.AttemptTimeout)
	defer cancel()
	res := make(chan Outcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				d.log.Error("outbox transport panicked", slog.Any("panic", r))
				res <- Outcome{Kind: OutcomeAmbiguous, Code: "transport_panic"}
			}
		}()
		res <- d.transport.Attempt(ctx, req)
	}()
	select {
	case out := <-res:
		return out
	case <-ctx.Done():
		return Outcome{Kind: OutcomeAmbiguous, Code: "timeout"}
	}
}

// persist commits the outcome of the attempt. It never calls the transport.
func (d *OutboxDispatcher) persist(c store.OutboxClaim, out Outcome) {
	// Detached from shutdown: the attempt already happened and must be recorded.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out.Kind == OutcomeAck && out.RemoteMessageID <= 0 {
		out = Outcome{Kind: OutcomeAmbiguous, Code: "malformed_reply"}
	}
	diag := out.diagnostic()
	var err error
	switch out.Kind {
	case OutcomeAck:
		err = retryPersist(func() error { return d.backend.AckPart(ctx, c, out.RemoteMessageID) })
		if err != nil {
			// Accepted remotely but not recorded: record the truth we can.
			d.log.Error("outbox ack not persisted", slog.String("part", c.PartID), slog.Any("err", err))
			if e := d.backend.MarkUncertain(ctx, c, "ack_not_persisted", diag); e != nil {
				d.log.Error("outbox part left sending; restart sweep will mark it uncertain", slog.String("part", c.PartID), slog.Any("err", e))
			}
			return
		}
	case OutcomeRateLimited:
		_, err = d.backend.ReleaseUnsent(ctx, c, d.cfg.Now().Add(d.retryDelay(c.Attempt, out.RetryAfter)), out.Code, diag, false)
	case OutcomeNotSent:
		refund := out.Code == "canceled_before_dispatch"
		_, err = d.backend.ReleaseUnsent(ctx, c, d.cfg.Now().Add(d.retryDelay(c.Attempt, 0)), out.Code, diag, refund)
	case OutcomeRefused:
		err = d.backend.FailPart(ctx, c, out.Code, diag)
	default:
		err = d.backend.MarkUncertain(ctx, c, out.Code, diag)
	}
	if err != nil {
		d.log.Error("outbox outcome not persisted; restart sweep will mark the part uncertain",
			slog.String("part", c.PartID), slog.String("outcome", out.Kind.String()), slog.Any("err", err))
	}
}

func retryPersist(fn func() error) error {
	var err error
	for i := 0; i < 3; i++ {
		if err = fn(); err == nil || errors.Is(err, store.ErrClaimLost) || errors.Is(err, store.ErrOutboxFenced) {
			return err
		}
		time.Sleep(time.Duration(i+1) * 50 * time.Millisecond)
	}
	return err
}

// retryDelay is the known-unsent delay after attempt n: the backoff step with
// bounded jitter, or the larger authoritative retry_after.
func (d *OutboxDispatcher) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	step := d.cfg.Backoff[min(max(attempt, 1), len(d.cfg.Backoff))-1]
	return max(step+d.cfg.Jitter(step), retryAfter)
}

// chatIDFromJID parses the "tg:<id>" destination of a stored conversation.
func chatIDFromJID(jid string) (int64, error) {
	rest, ok := strings.CutPrefix(jid, "tg:")
	if !ok {
		return 0, errors.New("not a telegram jid")
	}
	return strconv.ParseInt(rest, 10, 64)
}
