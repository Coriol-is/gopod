package maintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrUnsettled is the sentinel of UnsettledError.
var ErrUnsettled = errors.New("maintenance: owned writers are not settled")

// UnsettledError lists why a capture was refused. Nothing was stopped.
type UnsettledError struct{ Reasons []string }

func (e *UnsettledError) Error() string {
	return ErrUnsettled.Error() + ": " + strings.Join(e.Reasons, "; ")
}
func (e *UnsettledError) Is(target error) bool { return target == ErrUnsettled }

// ContainerLister reports live agent containers owned by this deployment. It is
// deliberately read-only: the fence has no way to stop, kill or remove anything.
type ContainerLister interface {
	// LiveOwned returns identifiers of owned containers that are running,
	// restarting or paused. An error means the inventory is unknown.
	LiveOwned(ctx context.Context) ([]string, error)
}

// Container inventory evidence recorded in the manifest.
const (
	ContainersNoneRunning      = "none_running"
	ContainersOperatorAsserted = "operator_asserted_none"
)

// Settlement is what must be true before a stopped-writer capture starts.
type Settlement struct {
	// LockPath is the controller instance lock (ControllerLockPath).
	LockPath string
	// Containers lists live owned writers. When nil, the capture is refused
	// unless AssertNoContainers is set: an unknown writer set is never assumed
	// empty.
	Containers ContainerLister
	// AssertNoContainers is the operator's explicit declaration that no agent
	// containers can exist (for example GOPOD_NO_CONTAINER deployments). It is
	// recorded in the manifest as operator-asserted, not verified.
	AssertNoContainers bool
}

// Fence is a held capture admission fence. Release it when the capture ends.
type Fence struct {
	lock *Lock
	// ContainerEvidence is ContainersNoneRunning or ContainersOperatorAsserted.
	ContainerEvidence string
}

// Acquire takes the controller lock, so no controller is running and none can
// start, then checks the owned containers. On any unsettled writer it releases
// everything and returns an *UnsettledError; it never stops a writer.
func (s Settlement) Acquire(ctx context.Context) (*Fence, error) {
	l, err := TryLock(s.LockPath)
	if err != nil {
		if errors.Is(err, ErrHeld) {
			return nil, &UnsettledError{Reasons: []string{"controller is running (instance lock is held); stop it through the normal service procedure, nothing was stopped"}}
		}
		return nil, err
	}
	f := &Fence{lock: l}
	switch {
	case s.Containers != nil:
		ids, lerr := s.Containers.LiveOwned(ctx)
		if lerr != nil {
			_ = l.Release()
			return nil, &UnsettledError{Reasons: []string{fmt.Sprintf("container inventory unavailable: %v", lerr)}}
		}
		if len(ids) > 0 {
			_ = l.Release()
			short := make([]string, len(ids))
			for i, id := range ids {
				short[i] = shortID(id)
			}
			return nil, &UnsettledError{Reasons: []string{fmt.Sprintf("%d owned agent container(s) still live: %s; nothing was stopped", len(ids), strings.Join(short, ","))}}
		}
		f.ContainerEvidence = ContainersNoneRunning
	case s.AssertNoContainers:
		f.ContainerEvidence = ContainersOperatorAsserted
	default:
		_ = l.Release()
		return nil, &UnsettledError{Reasons: []string{"owned writer set is unknown: no container inventory and no operator assertion"}}
	}
	return f, nil
}

// Release drops the fence. Safe on nil.
func (f *Fence) Release() error {
	if f == nil {
		return nil
	}
	return f.lock.Release()
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
