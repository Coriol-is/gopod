package queue

// Managed task dispatch (CP-004/021 plan PR 2).
//
// A managed item is one attempt of a task run whose rows live in the task
// ledger. It differs from an interactive or legacy-task item in four ways:
//
//   - it is always a singleton batch (never coalesced with Telegram turns, other
//     tasks or another run, even at thread 0);
//   - it is dispatched from the store after the admission transaction commits
//     (Dispatch), and Recover finds it again if the process died before that;
//   - capacity and the provider snapshot are persisted (Reserve) before the
//     handler may spawn, and the spawn marker (Execution.BeginSpawn) is
//     persisted before native execution is requested;
//   - once the spawn marker may have been committed it is never run again: not
//     by the backoff loop, not by Recover. Only observed local exit settles it.
//
// Single-controller assumption: one gopod process owns the database and the task
// containers, so the ledger alone is enough to rebuild held capacity at boot.
// Two live controllers on one database are not supported.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Errors a managed Handler may return.
var (
	// ErrNotStarted marks a failure the host boundary reports as happening before
	// any native execution was requested (the spawn marker was not committed).
	// Only this error, never provider prose, earns a bounded pre-exec retry.
	ErrNotStarted = errors.New("queue: managed attempt not started")
	// ErrDenied marks a deterministic refusal before any spawn marker: the host
	// will not run this attempt (policy, role, provider or profile). Unlike
	// ErrNotStarted it earns no retry: retrying the same attempt cannot change
	// the answer, so the attempt is settled as not started and the turn closed.
	// Returned by the Snapshot resolver or the Handler.
	ErrDenied = errors.New("queue: managed attempt denied before spawn")
	// ErrSpawnRefused is returned by ManagedStore.BeginSpawn when the ledger
	// proves the spawn marker was NOT committed (for example a stop request won
	// the race). Any other BeginSpawn error is treated as possibly committed.
	ErrSpawnRefused = errors.New("queue: spawn refused by the task ledger")
)

// Snapshot is the provider selection frozen onto an attempt before spawn.
type Snapshot struct{ Provider, Model, ProfileFingerprint string }

// ReserveKind mirrors the store's reservation outcome.
type ReserveKind string

const (
	ReserveNew      ReserveKind = "reserved"
	ReserveResume   ReserveKind = "resume"
	ReserveRejected ReserveKind = "rejected"
)

// ReserveOutcome is the committed result of the pre-spawn gate.
type ReserveOutcome struct {
	Kind     ReserveKind
	Reason   string // fixed code when rejected
	State    string // attempt state as committed (lets the queue settle a stale reservation)
	Snapshot Snapshot
}

// SettleKind mirrors the store's settlement kinds.
type SettleKind string

const (
	SettleExited      SettleKind = "exited"
	SettleFailed      SettleKind = "failed"
	SettleStopped     SettleKind = "stopped"
	SettleInterrupted SettleKind = "interrupted"
	SettleUnknown     SettleKind = "unknown"
	SettleNotStarted  SettleKind = "not_started"
)

// Settlement is a host observation about how a handler call ended.
type Settlement struct {
	Kind        SettleKind
	Evidence    string
	FailureCode string
}

// Execution states reported back by the ledger (values match store.ExecState).
const (
	StateCompleted   = "completed"
	StateFailed      = "failed"
	StateCancelled   = "cancelled"
	StateInterrupted = "interrupted"
	StateUnknown     = "execution_unknown"
)

// RecoveryAction mirrors the store's boot-recovery decision.
type RecoveryAction string

const (
	RecoverDispatch RecoveryAction = "dispatch"
	RecoverHold     RecoveryAction = "hold"
	RecoverFrozen   RecoveryAction = "frozen"
	RecoverSettled  RecoveryAction = "settled"
)

// Recovery is the ledger's decision for one managed turn found unfinished.
type Recovery struct {
	Action   RecoveryAction
	State    string // attempt state after the decision
	ResultID string // frozen result, if any
}

// Hold is a capacity reservation that survives a restart.
type Hold struct {
	AttemptID, RunID, Folder string
	TurnID                   int64
}

// ReleaseProof names why a hold may be released.
type ReleaseProof string

const (
	// ProofLocalExit: the host observed the owned local process exit.
	ProofLocalExit ReleaseProof = "local_exit_observed"
	// ProofOperatorReconciled: an operator established the runtime is stopped.
	ProofOperatorReconciled ReleaseProof = "operator_reconciled"
)

// ManagedStore is the ledger side of dispatch, declared at the consumer. cmd/gopod
// adapts *store.Store to it. No method may be called with a transaction held, and
// none may spawn, wait or touch the network.
type ManagedStore interface {
	// LoadTurn returns an already persisted turn as an Item (with run, attempt
	// and session kind), so a commit can be dispatched without Enqueue.
	LoadTurn(ctx context.Context, turnID int64) (Item, error)
	Reserve(ctx context.Context, attemptID string, snap Snapshot, now time.Time) (ReserveOutcome, error)
	BeginSpawn(ctx context.Context, attemptID string) error
	Settle(ctx context.Context, attemptID string, s Settlement) (state string, err error)
	Recover(ctx context.Context, turnID int64) (Recovery, error)
	Held(ctx context.Context) ([]Hold, error)
	Release(ctx context.Context, attemptID string, proof ReleaseProof, detail string) error
}

// ManagedRuntime configures managed dispatch. Handler nil means inspect-only:
// Recover still performs the conservative ledger sweep and rebuilds held
// capacity, but no managed item is ever run. That is the PR 2 production wiring.
type ManagedRuntime struct {
	Store ManagedStore
	// Handler runs exactly one managed item. It must call
	// ExecutionFrom(ctx).BeginSpawn before requesting native execution, report
	// Exited only after observing the owned local process exit, and return
	// ErrNotStarted for failures before the spawn marker.
	Handler Handler
	// Snapshot resolves provider/model/profile. It is called after the chat slot
	// is acquired and before Reserve, never while a store transaction is open.
	Snapshot func(ctx context.Context, it Item) (Snapshot, error)
	// OnFrozen is told about a frozen result found at dispatch or recovery. The
	// queue never runs the model for it; publication is the callee's job (PR 4).
	OnFrozen func(ctx context.Context, it Item, resultID string) error
}

// ManagedOutcome is what OnManagedDone hooks receive. State is the ledger
// state after settlement; it is never derived from a nil error.
type ManagedOutcome struct {
	State    string // StateCompleted ... StateUnknown
	ResultID string
	Err      error // diagnostic only
}

// Managed reports whether the item is a managed task attempt.
func (i Item) Managed() bool { return i.AttemptID != "" }

type execKey struct{}

// Execution is the handler's channel back to the ledger for one attempt.
type Execution struct {
	q    *Queue
	item Item
	snap Snapshot

	mu        sync.Mutex
	started   bool  // spawn marker committed or possibly committed
	confirmed bool  // spawn marker known committed
	ambiguous error // marker commit status unknown: never allow a spawn
	refused   bool  // ledger proved the marker was not committed
	exitInfo  string
}

// ExecutionFrom returns the Execution of a managed handler call.
func ExecutionFrom(ctx context.Context) (*Execution, bool) {
	e, ok := ctx.Value(execKey{}).(*Execution)
	return e, ok
}

// Snapshot returns the provider selection frozen for this attempt.
func (e *Execution) Snapshot() Snapshot { return e.snap }

// BeginSpawn persists the spawn marker. The handler must not request native
// execution unless it returns nil. It is idempotent after success.
func (e *Execution) BeginSpawn(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.confirmed {
		return nil
	}
	if e.ambiguous != nil {
		return e.ambiguous // never turn an unknown commit into permission to spawn
	}
	err := e.q.managedStore().BeginSpawn(context.WithoutCancel(ctx), e.item.AttemptID)
	switch {
	case err == nil:
		e.started, e.confirmed = true, true
	case errors.Is(err, ErrSpawnRefused):
		e.refused = true
	default:
		e.started = true // ambiguous commit: assume it may have happened
		e.ambiguous = err
	}
	return err
}

// Exited records host-observed proof that the owned local process exited
// (container exec wait result, inspection). Closing an attach stream or hitting
// a timeout is NOT such proof and must not call this.
func (e *Execution) Exited(evidence string) {
	if len(evidence) > 350 {
		evidence = evidence[:350]
	}
	e.mu.Lock()
	e.exitInfo = evidence
	e.mu.Unlock()
}

func (e *Execution) snapshot() (started, refused bool, exit string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.started, e.refused, e.exitInfo
}

// held tracks reconstructed and live capacity holds per folder.
type holdState struct {
	folder string
	slot   bool // a global semaphore slot is accounted to this hold
}

// SetManaged enables managed dispatch. Call before Recover and before any
// producer runs.
func (q *Queue) SetManaged(rt ManagedRuntime) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.managed = &rt
}

// OnManagedDone registers a hook called when a managed attempt reaches a ledger
// outcome in this process (including recovery decisions). Legacy OnDone hooks
// are never called for managed items: they cannot tell success from a stop.
func (q *Queue) OnManagedDone(fn func(Item, ManagedOutcome)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onManaged = append(q.onManaged, fn)
}

func (q *Queue) runtime() *ManagedRuntime {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.managed
}

func (q *Queue) managedStore() ManagedStore { return q.runtime().Store }

// managedRunnable reports whether managed items may be scheduled to run.
func (q *Queue) managedRunnable() bool {
	rt := q.runtime()
	return rt != nil && rt.Handler != nil && rt.Snapshot != nil
}

// Dispatch schedules an already persisted managed turn after its admission
// transaction committed. It is safe to call twice and safe to skip: a crash
// before this call is recovered by Recover from the store. It reports whether
// the item was scheduled in this process.
func (q *Queue) Dispatch(ctx context.Context, turnID int64) (bool, error) {
	rt := q.runtime()
	if rt == nil {
		return false, errors.New("queue: managed dispatch is not configured")
	}
	it, err := rt.Store.LoadTurn(ctx, turnID)
	if err != nil {
		return false, fmt.Errorf("queue: load turn %d: %w", turnID, err)
	}
	if !it.Managed() {
		return false, fmt.Errorf("queue: turn %d is not a managed task attempt", turnID)
	}
	if !q.markKnown(it.ID) {
		return false, nil // already in memory; never reclassify live work
	}
	run, err := q.planManaged(ctx, it)
	if err != nil {
		q.forget(it.ID)
		return false, err
	}
	if !run {
		q.forget(it.ID)
		return false, nil
	}
	if !q.schedule(it) {
		q.forget(it.ID)
		return false, nil
	}
	return true, nil
}

// markKnown records the turn as live in memory; false if it already was.
func (q *Queue) markKnown(id int64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.known == nil {
		q.known = map[int64]struct{}{}
	}
	if _, ok := q.known[id]; ok {
		return false
	}
	q.known[id] = struct{}{}
	return true
}

func (q *Queue) forget(id int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.known, id)
}

// planManaged asks the ledger what may happen to a managed turn and applies the
// non-running outcomes (closing the turn row, notifying hooks, publishing a
// frozen result). It returns true only when the item may be scheduled to run.
func (q *Queue) planManaged(ctx context.Context, it Item) (bool, error) {
	rt := q.runtime()
	plan, err := rt.Store.Recover(ctx, it.ID)
	if err != nil {
		return false, fmt.Errorf("queue: recover managed turn %d: %w", it.ID, err)
	}
	switch plan.Action {
	case RecoverDispatch:
		if it.Attempts >= MaxAttempts {
			// Crash loop before any spawn marker: fail it without running.
			q.settleManaged(it, Settlement{Kind: SettleNotStarted, Evidence: "not_started:crash_loop_before_spawn", FailureCode: "crash_loop"}, ErrCrashLoop)
			return false, nil
		}
		if !q.managedRunnable() {
			q.log.Warn("queue: managed turn left pending, no managed handler configured",
				slog.Int64("turn", it.ID), slog.String("attempt", it.AttemptID))
			return false, nil
		}
		return true, nil
	case RecoverFrozen:
		q.log.Info("queue: frozen result recovered, publication only",
			slog.Int64("turn", it.ID), slog.String("attempt", it.AttemptID), slog.String("result", plan.ResultID))
		if rt.OnFrozen != nil {
			if ferr := rt.OnFrozen(ctx, it, plan.ResultID); ferr != nil {
				q.log.Error("queue: publish frozen result", slog.Int64("turn", it.ID), slog.Any("err", ferr))
			}
		}
		fallthrough
	case RecoverHold:
		q.log.Warn("queue: managed attempt may have started, not replayed",
			slog.Int64("turn", it.ID), slog.String("attempt", it.AttemptID), slog.String("state", plan.State))
		q.closeManaged(it, StatusFailed, "execution_unknown", ManagedOutcome{State: plan.State, ResultID: plan.ResultID})
	case RecoverSettled:
		q.closeManaged(it, statusForState(plan.State), "", ManagedOutcome{State: plan.State, ResultID: plan.ResultID})
	}
	return false, nil
}

func statusForState(state string) string {
	switch state {
	case StateCompleted:
		return StatusDone
	case StateCancelled:
		return StatusStopped
	case StateInterrupted:
		return StatusInterrupted
	default:
		return StatusFailed
	}
}

// closeManaged finishes the durable turn row and notifies managed hooks.
func (q *Queue) closeManaged(it Item, status, errText string, out ManagedOutcome) {
	if q.store != nil && it.ID != 0 {
		if err := q.store.MarkFinished(context.WithoutCancel(q.ctx), it.ID, status, errText); err != nil {
			q.log.Error("queue: mark managed finished", slog.Int64("turn", it.ID), slog.Any("err", err))
		}
	}
	q.forget(it.ID)
	q.mu.Lock()
	hooks := append([]func(Item, ManagedOutcome){}, q.onManaged...)
	q.mu.Unlock()
	for _, h := range hooks {
		h(it, out)
	}
}

// settleManaged records s in the ledger and closes the turn. It returns the
// ledger state, or "" if the ledger write failed (the caller must then hold).
func (q *Queue) settleManaged(it Item, s Settlement, cause error) string {
	state, err := q.managedStore().Settle(context.WithoutCancel(q.ctx), it.AttemptID, s)
	if err != nil {
		q.log.Error("queue: settle managed attempt", slog.String("attempt", it.AttemptID), slog.Any("err", err))
		return ""
	}
	if state == StateUnknown {
		return state // caller takes the hold; turn closed by it
	}
	errText := ""
	if cause != nil {
		errText = cause.Error()
	}
	q.closeManaged(it, statusForState(state), errText, ManagedOutcome{State: state, Err: cause})
	return state
}

// reconstructHolds rebuilds held capacity from the ledger. Call before any work
// is scheduled. Each hold blocks its folder and takes a global slot when one is
// free; it stays until ReleaseHold.
func (q *Queue) reconstructHolds(ctx context.Context) error {
	rt := q.runtime()
	if rt == nil {
		return nil
	}
	holds, err := rt.Store.Held(ctx)
	if err != nil {
		return fmt.Errorf("queue: load held reservations: %w", err)
	}
	for _, h := range holds {
		q.addHold(h.AttemptID, h.Folder, false)
		q.log.Warn("queue: holding capacity for a possibly running task attempt",
			slog.String("attempt", h.AttemptID), slog.String("run", h.RunID), slog.String("folder", h.Folder))
	}
	return nil
}

// addHold registers a hold. ownsSlot says the caller already holds a semaphore
// slot that now belongs to the hold; otherwise one is taken if free.
func (q *Queue) addHold(attemptID, folder string, ownsSlot bool) {
	slot := ownsSlot
	if !slot {
		select {
		case q.sem <- struct{}{}:
			slot = true
		default:
			q.log.Warn("queue: no free slot to account a hold; folder is still blocked", slog.String("attempt", attemptID))
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.holds[attemptID]; ok {
		if slot && !ownsSlot {
			<-q.sem // already accounted; give the extra slot back
		}
		return
	}
	if q.holds == nil {
		q.holds = map[string]holdState{}
	}
	q.holds[attemptID] = holdState{folder: folder, slot: slot}
	cs, ok := q.chats[folder]
	if !ok {
		cs = &chatState{}
		q.chats[folder] = cs
	}
	cs.held++
}

// ReleaseHold releases a held reservation after local exit was observed or an
// operator reconciled the runtime. It never reruns the attempt and does not
// authorize replay of uncertain remote effects.
func (q *Queue) ReleaseHold(ctx context.Context, attemptID string, proof ReleaseProof, detail string) error {
	rt := q.runtime()
	if rt == nil {
		return errors.New("queue: managed dispatch is not configured")
	}
	if err := rt.Store.Release(ctx, attemptID, proof, detail); err != nil {
		return fmt.Errorf("queue: release hold %s: %w", attemptID, err)
	}
	q.mu.Lock()
	h, ok := q.holds[attemptID]
	if ok {
		delete(q.holds, attemptID)
		if cs := q.chats[h.folder]; cs != nil && cs.held > 0 {
			cs.held--
		}
	}
	q.mu.Unlock()
	if ok {
		if h.slot {
			<-q.sem
		}
		q.kick(h.folder)
	}
	return nil
}

// Holds returns the number of capacity holds, for diagnostics.
func (q *Queue) Holds() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.holds)
}

// kick starts a worker for a folder whose pending items were waiting on a hold.
func (q *Queue) kick(folder string) {
	q.mu.Lock()
	cs := q.chats[folder]
	if q.closed || cs == nil || cs.active || cs.held > 0 || len(cs.pending) == 0 {
		q.mu.Unlock()
		return
	}
	cs.active = true
	q.wg.Add(1)
	q.mu.Unlock()
	go func() {
		defer q.wg.Done()
		q.worker(q.ctx, folder)
	}()
}

// FolderHeld reports whether a possibly running managed attempt holds the
// folder. The runner consults it before recycling a container.
func (q *Queue) FolderHeld(folder string) bool { return q.folderHeld(folder) }

// folderHeld reports whether a hold blocks the folder.
func (q *Queue) folderHeld(folder string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	cs := q.chats[folder]
	return cs != nil && cs.held > 0
}

// recoverManagedItems runs the conservative ledger sweep over the managed items
// of a recovered batch, then rebuilds held capacity. It returns the set of turn
// ids that the caller must schedule. It runs before anything is scheduled, so
// no work is admitted into a namespace before its holds exist.
func (q *Queue) recoverManagedItems(ctx context.Context, items []Item) (run map[int64]bool, err error) {
	run = map[int64]bool{}
	if q.runtime() == nil {
		return run, nil
	}
	for _, it := range items {
		if !it.Managed() {
			continue
		}
		if !q.markKnown(it.ID) {
			continue
		}
		ok, perr := q.planManaged(ctx, it)
		if perr != nil {
			q.forget(it.ID)
			q.log.Error("queue: managed recovery failed, turn left untouched", slog.Int64("turn", it.ID), slog.Any("err", perr))
			continue
		}
		if ok {
			run[it.ID] = true
		} else {
			q.forget(it.ID)
		}
	}
	return run, q.reconstructHolds(ctx)
}

// runManaged executes one managed singleton batch inside the worker.
func (q *Queue) runManaged(ctx context.Context, folder string, it Item) workerStep {
	rt := q.runtime()
	for {
		if ctx.Err() != nil {
			q.forget(it.ID) // untouched: still pending in the store for Recover
			return stepStop
		}
		snap, err := rt.Snapshot(ctx, it)
		var out ReserveOutcome
		if err == nil {
			out, err = rt.Store.Reserve(context.WithoutCancel(ctx), it.AttemptID, snap, q.now())
		}
		if err != nil {
			if errors.Is(err, ErrDenied) {
				return q.denyManaged(folder, it, err)
			}
			// Failure to resolve or commit the reservation means no spawn.
			if !q.preSpawnBackoff(ctx, folder, it, err) {
				return stepStop
			}
			continue
		}
		if out.Kind == ReserveRejected {
			q.log.Info("queue: managed attempt not dispatched", slog.String("attempt", it.AttemptID), slog.String("reason", out.Reason))
			q.resetRetries(folder)
			q.settleRejected(it, out)
			return stepNext
		}

		q.markRunning(ctx, []Item{it})
		exec := &Execution{q: q, item: it, snap: out.Snapshot}
		herr := rt.Handler(context.WithValue(ctx, execKey{}, exec), []Item{it})
		started, refused, exit := exec.snapshot()
		// Shutdown counts only when the handler reported being cut off. A clean
		// return with proven exit is recorded as Exited even if shutdown raced it.
		shutdown := ctx.Err() != nil && herr != nil

		switch {
		case !started && ctx.Err() != nil:
			// Never spawned: leave the attempt reserved and the turn interrupted so
			// Recover redispatches it with its original snapshot.
			q.closeInterrupted(it, herr)
			return stepStop
		case !started && refused:
			q.settleManaged(it, Settlement{Kind: SettleNotStarted, Evidence: "not_started:spawn_marker_refused", FailureCode: "spawn_refused"}, herr)
			q.resetRetries(folder)
			return stepNext
		case !started && errors.Is(herr, ErrDenied):
			return q.denyManaged(folder, it, herr)
		case !started && errors.Is(herr, ErrNotStarted):
			if !q.preSpawnBackoff(ctx, folder, it, herr) {
				return stepStop
			}
			continue // attempt stays reserved; next pass resumes it
		}

		s := classify(started, exit, herr, shutdown)
		state := q.settleManaged(it, s, herr)
		if state != "" && state != StateUnknown {
			q.resetRetries(folder)
			if shutdown {
				return stepStop
			}
			return stepNext
		}
		// Unknown (or the ledger write failed): keep the slot as a hold.
		q.closeManaged(it, StatusFailed, "execution_unknown", ManagedOutcome{State: StateUnknown, Err: herr})
		q.addHold(it.AttemptID, folder, true)
		q.log.Error("queue: managed attempt exit not proven, capacity held until reconciliation",
			slog.String("attempt", it.AttemptID), slog.String("folder", folder), slog.Any("err", herr))
		return stepHold
	}
}

// denyManaged settles an attempt the host refused before any spawn marker. The
// ledger records it as not started (failed, reservation released) and the turn
// is closed, so neither the backoff loop nor Recover runs it again.
func (q *Queue) denyManaged(folder string, it Item, cause error) workerStep {
	s := Settlement{Kind: SettleNotStarted, Evidence: "not_started:denied", FailureCode: "denied"}
	if q.settleManaged(it, s, cause) == "" {
		q.forget(it.ID) // ledger untouched: the turn stays pending for Recover
		return stepStop
	}
	q.log.Warn("queue: managed attempt denied before spawn",
		slog.String("attempt", it.AttemptID), slog.Any("err", cause))
	q.resetRetries(folder)
	return stepNext
}

// settleRejected finishes an attempt the pre-spawn gate refused. A terminal
// attempt (the store already cancelled it) just closes the turn. A queued,
// reserved or cancel_requested attempt that never reached the spawn marker is
// settled as not started, so the run and its reservation cannot linger live
// after the turn is closed. A frozen result is left for Recover (publication).
func (q *Queue) settleRejected(it Item, out ReserveOutcome) {
	switch out.State {
	case StateCompleted, StateFailed, StateCancelled, StateInterrupted:
		q.closeManaged(it, statusForState(out.State), "dispatch_rejected:"+out.Reason, ManagedOutcome{State: out.State})
	case "queued", "reserved", "cancel_requested":
		if out.Reason == "result_frozen" {
			q.forget(it.ID)
			return
		}
		s := Settlement{Kind: SettleNotStarted, Evidence: "not_started:dispatch_rejected_" + out.Reason, FailureCode: "dispatch_rejected"}
		if q.settleManaged(it, s, fmt.Errorf("dispatch rejected: %s", out.Reason)) == "" {
			q.forget(it.ID) // ledger untouched: the turn stays pending for Recover
		}
	default:
		q.forget(it.ID) // running/unknown: never touched here, Recover decides
	}
}

// classify maps a handler outcome to a settlement. Only a host-observed exit
// can produce a terminal state, and a nil or stopped error alone never does.
func classify(started bool, exit string, err error, shutdown bool) Settlement {
	if !started || exit == "" {
		return Settlement{Kind: SettleUnknown, FailureCode: "exit_not_proven"}
	}
	switch {
	case shutdown:
		return Settlement{Kind: SettleInterrupted, Evidence: exit}
	case err == nil:
		return Settlement{Kind: SettleExited, Evidence: exit}
	case errors.Is(err, ErrStopped):
		return Settlement{Kind: SettleStopped, Evidence: exit}
	default:
		return Settlement{Kind: SettleFailed, Evidence: exit}
	}
}

// closeInterrupted marks the turn interrupted without touching the ledger.
func (q *Queue) closeInterrupted(it Item, cause error) {
	if cause == nil {
		cause = context.Canceled
	}
	if q.store != nil && it.ID != 0 {
		if err := q.store.MarkFinished(context.WithoutCancel(q.ctx), it.ID, StatusInterrupted, cause.Error()); err != nil {
			q.log.Error("queue: mark managed interrupted", slog.Int64("turn", it.ID), slog.Any("err", err))
		}
	}
	q.forget(it.ID)
}

// preSpawnBackoff applies the bounded backoff for failures that provably happened
// before the spawn marker. It returns false when the worker must stop: retries
// exhausted (the attempt stays durable and queued/reserved for the next
// Recover) or shutdown.
func (q *Queue) preSpawnBackoff(ctx context.Context, folder string, it Item, cause error) bool {
	retries := q.incrementRetries(folder)
	if retries > q.maxRetries {
		q.log.Error("queue: managed pre-spawn retries exhausted, attempt left for recovery",
			slog.String("attempt", it.AttemptID), slog.Int("retries", retries), slog.Any("err", cause))
		q.forget(it.ID)
		return false
	}
	backoff := q.BackoffFunc(retries)
	q.log.Warn("queue: managed attempt not started, backing off",
		slog.String("attempt", it.AttemptID), slog.Int("retry", retries), slog.Duration("backoff", backoff), slog.Any("err", cause))
	select {
	case <-time.After(backoff):
		return true
	case <-ctx.Done():
		q.forget(it.ID)
		return false
	}
}

type workerStep int

const (
	stepNext workerStep = iota // keep draining this folder
	stepStop                   // deactivate the folder and release the slot
	stepHold                   // deactivate the folder; the slot now belongs to a hold
)
