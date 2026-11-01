package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/errdefs"
)

// ErrDraining is a pre-dispatch refusal. Callers must retain accepted input.
var ErrDraining = errors.New("runner: restart drain in progress")

// BeginDrain closes admission without cancelling any operation already admitted.
func (r *Runner) BeginDrain() {
	r.agentSlotsMu.Lock()
	defer r.agentSlotsMu.Unlock()
	r.draining = true
	r.signalSlotsLocked()
}

// CancelDrain reopens admission. It does not resurrect force-cancelled work.
func (r *Runner) CancelDrain() {
	r.agentSlotsMu.Lock()
	defer r.agentSlotsMu.Unlock()
	r.draining, r.forcing = false, false
	r.signalSlotsLocked()
}

func (r *Runner) signalSlotsLocked() {
	if r.slotsChanged != nil {
		close(r.slotsChanged)
	}
	r.slotsChanged = make(chan struct{})
}

// WaitDrained waits for operation claims, not a period of silent model output.
// An unknown cleanup remains held even after its Go goroutine has exited.
func (r *Runner) WaitDrained(ctx context.Context) error {
	for {
		r.reconcileExited(ctx)
		r.agentSlotsMu.Lock()
		if len(r.unknownExecutions) > 0 {
			r.agentSlotsMu.Unlock()
			return ErrExecutionUnproven
		}
		if len(r.agentSlots) == 0 && len(r.operationCancels) == 0 {
			r.agentSlotsMu.Unlock()
			if r.d != nil && r.d.hasQuarantine(ctx) {
				return ErrExecutionUnproven
			}
			for _, folder := range r.ActiveChats() {
				if err := r.verifyQuiescent(ctx, folder); err != nil {
					return err
				}
			}
			return nil
		}
		if r.slotsChanged == nil {
			r.slotsChanged = make(chan struct{})
		}
		wake := r.slotsChanged
		r.agentSlotsMu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ForceDrain cancels registered native/local exec contexts after closing
// admission. It never converts an unproven cleanup into successful termination.
func (r *Runner) ForceDrain(ctx context.Context) error {
	r.CancelActive()
	return r.WaitDrained(ctx)
}

// CancelActive closes admission and cancels current operation contexts. It does
// not wait; a controller may then terminate exact scope-validated quarantined
// runtimes and call WaitDrained to verify settlement.
func (r *Runner) CancelActive() {
	r.agentSlotsMu.Lock()
	r.draining, r.forcing = true, true
	cancels := make([]context.CancelFunc, 0, len(r.operationCancels))
	for _, cancel := range r.operationCancels {
		cancels = append(cancels, cancel)
	}
	r.signalSlotsLocked()
	r.agentSlotsMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// operationContext registers a cancellable exec inside its already-owned slot.
// Force racing registration is handled under the same admission mutex.
func (r *Runner) operationContext(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	r.agentSlotsMu.Lock()
	r.operationSequence++
	id := r.operationSequence
	if r.operationCancels == nil {
		r.operationCancels = make(map[uint64]context.CancelFunc)
	}
	r.operationCancels[id] = cancel
	force := r.forcing
	r.agentSlotsMu.Unlock()
	if force {
		cancel()
	}
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel()
			r.agentSlotsMu.Lock()
			delete(r.operationCancels, id)
			r.signalSlotsLocked()
			r.agentSlotsMu.Unlock()
		})
	}
}

func (r *Runner) markExecutionUnknown(folder, containerID string) {
	r.agentSlotsMu.Lock()
	if r.unknownExecutions == nil {
		r.unknownExecutions = make(map[string]string)
	}
	r.unknownExecutions[folder] = containerID
	r.signalSlotsLocked()
	r.agentSlotsMu.Unlock()
}

// tryIdleSlot makes the idle check and operation admission mutually exclusive.
// It never cancels maintenance in order to evict its container.
func (r *Runner) tryIdleSlot(folder string) (func(), bool) {
	r.agentSlotsMu.Lock()
	defer r.agentSlotsMu.Unlock()
	if r.draining || r.agentSlots[folder] != nil || r.unknownExecutions[folder] != "" {
		return nil, false
	}
	if r.agentSlots == nil {
		r.agentSlots = make(map[string]chan struct{})
	}
	done := make(chan struct{})
	r.agentSlots[folder] = done
	return r.slotReleaser(folder, done, false), true
}

func (r *Runner) slotReleaser(folder string, done chan struct{}, refresh bool) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			// Refresh before exposing the idle runtime to a sweep.
			if refresh {
				r.touch(folder)
			}
			r.agentSlotsMu.Lock()
			delete(r.agentSlots, folder)
			delete(r.backgroundCancels, folder)
			r.signalSlotsLocked()
			close(done)
			r.agentSlotsMu.Unlock()
		})
	}
}

func (r *Runner) admissionErrorLocked(folder string) error {
	if r.draining {
		return ErrDraining
	}
	if r.unknownExecutions[folder] != "" {
		return fmt.Errorf("%w: %s", ErrExecutionUnproven, folder)
	}
	for scope := range r.unknownExecutions {
		if strings.HasPrefix(scope, "container:") {
			return ErrExecutionUnproven
		}
	}
	return nil
}

// ExecutionUncertain reports whether a namespace has unresolved process ownership.
// Telegram recovery must not describe these attempts as definitely stopped.
func (r *Runner) ExecutionUncertain(ctx context.Context, folder string) bool {
	r.reconcileExited(ctx)
	r.agentSlotsMu.Lock()
	unknown := r.unknownExecutions[folder] != ""
	for scope := range r.unknownExecutions {
		if strings.HasPrefix(scope, "container:") {
			unknown = true
		}
	}
	r.agentSlotsMu.Unlock()
	return unknown || (r.d != nil && r.d.quarantined(ctx, folder))
}

func (r *Runner) reconcileExited(ctx context.Context) {
	if r.d == nil {
		return
	}
	r.agentSlotsMu.Lock()
	copy := make(map[string]string, len(r.unknownExecutions))
	for folder, id := range r.unknownExecutions {
		copy[folder] = id
	}
	r.agentSlotsMu.Unlock()
	for folder, id := range copy {
		if id == "unresolved" {
			actual, err := r.d.inspectByName(ctx, ContainerName(folder))
			if err != nil {
				continue
			}
			if actual != "" && !r.d.containerDead(ctx, actual) {
				continue
			}
		} else if !r.d.containerDead(ctx, id) {
			continue
		}
		r.agentSlotsMu.Lock()
		if r.unknownExecutions[folder] == id {
			delete(r.unknownExecutions, folder)
			r.signalSlotsLocked()
		}
		r.agentSlotsMu.Unlock()
	}
	r.agentSlotsMu.Lock()
	var releases []func()
	if len(r.unknownExecutions) == 0 {
		releases = r.pendingGateReleases
		r.pendingGateReleases = nil
	}
	r.agentSlotsMu.Unlock()
	for _, release := range releases {
		release()
	}
}
func (d *Docker) containerDead(ctx context.Context, id string) bool {
	info, err := d.cli.ContainerInspect(ctx, id)
	return errdefs.IsNotFound(err) || (err == nil && info.State != nil && !info.State.Running && !info.State.Restarting && !info.State.Paused)
}
func (d *Docker) quarantined(ctx context.Context, folder string) bool {
	d.quarantineMu.Lock()
	id := d.quarantine[folder]
	d.quarantineMu.Unlock()
	if id == "" {
		return false
	}
	if !d.containerDead(ctx, id) {
		return true
	}
	d.quarantineMu.Lock()
	if d.quarantine[folder] == id {
		delete(d.quarantine, folder)
	}
	d.quarantineMu.Unlock()
	return false
}
func (d *Docker) hasQuarantine(ctx context.Context) bool {
	d.quarantineMu.Lock()
	folders := make([]string, 0, len(d.quarantine))
	for f := range d.quarantine {
		folders = append(folders, f)
	}
	d.quarantineMu.Unlock()
	for _, f := range folders {
		if d.quarantined(ctx, f) {
			return true
		}
	}
	return false
}

func (r *Runner) markContainerUnknown(ctx context.Context, id string) {
	if r.d == nil {
		return
	}
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	info, err := r.d.cli.ContainerInspect(probe, id)
	if err == nil && info.Config != nil && info.Config.Labels[LabelChat] != "" {
		r.markExecutionUnknown(info.Config.Labels[LabelChat], id)
		return
	}
	// Inspection failure must still hold admission even when folder is unavailable.
	r.markExecutionUnknown("container:"+id, id)
}

// verifyQuiescent checks for unregistered/detached processes after the owned exec
// has settled. Production runtime PID 1 is sleep; any additional process must
// have an operation claim. Called only while admission excludes concurrent exec.
func (r *Runner) verifyQuiescent(ctx context.Context, folder string) error {
	if r.d == nil {
		return nil
	}
	id, err := r.d.inspectByName(ctx, ContainerName(folder))
	if err != nil {
		return fmt.Errorf("%w: inspect runtime", ErrExecutionUnproven)
	}
	if id == "" || r.d.containerDead(ctx, id) {
		return nil
	}
	top, err := r.d.cli.ContainerTop(ctx, id, []string{"-eo", "pid"})
	if err != nil || len(top.Processes) != 1 {
		r.markExecutionUnknown(folder, id)
		return ErrExecutionUnproven
	}
	return nil
}

// releaseGateWhenSettled retains global resource accounting for an exec whose
// transport closed without death evidence. Other namespaces cannot reuse its
// capacity while it may still be consuming resources.
func (r *Runner) releaseGateWhenSettled(ctx context.Context, folder string, release func()) {
	probe, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.verifyQuiescent(probe, folder); err != nil {
		// Inspection failure without an ID is still unresolved capacity.
		r.agentSlotsMu.Lock()
		if len(r.unknownExecutions) == 0 {
			r.unknownExecutions = map[string]string{folder: "unresolved"}
		}
		r.agentSlotsMu.Unlock()
	}
	r.agentSlotsMu.Lock()
	if len(r.unknownExecutions) > 0 {
		r.pendingGateReleases = append(r.pendingGateReleases, release)
		r.agentSlotsMu.Unlock()
		return
	}
	r.agentSlotsMu.Unlock()
	release()
}
