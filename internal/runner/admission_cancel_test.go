package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"github.com/Coriol-is/gopod/internal/store"
)

type canceledRegistrationRead struct {
	cause error
}

func (r canceledRegistrationRead) ListRegistered(context.Context) ([]store.RegisteredChat, error) {
	return nil, r.cause
}

func TestAdmissionPreservesCanceledRegistrationRead(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, path := range []string{"profile", "group", "owner", "runtime"} {
			t.Run(cause.Error()+"/"+path, func(t *testing.T) {
				r := &Runner{}
				r.SetAdmissionPolicy(1, canceledRegistrationRead{cause: cause})
				r.ensureFn = func(context.Context, string, Tier, *mountsec.Allowlist) (string, error) {
					t.Fatal("canceled authorization reached runtime")
					return "", nil
				}
				var err error
				switch path {
				case "profile":
					err = r.authorizeProfile(t.Context(), "owner")
				case "group":
					_, _, err = r.groupRegistration(t.Context(), "owner")
				case "owner":
					err = r.authorizeOwner(t.Context(), "owner")
				case "runtime":
					_, _, _, err = r.turnRuntime(t.Context(), "owner", 0, TierRegistered, nil)
				}
				if !errors.Is(err, cause) {
					t.Fatalf("cancellation masked: %v", err)
				}
				if errors.Is(err, ErrProfileUnavailable) {
					t.Fatalf("cancellation misclassified as unavailable profile: %v", err)
				}
			})
		}
	}
}

type preemptedRegistrationRead struct {
	runner  *Runner
	started chan struct{}
}

func (r *preemptedRegistrationRead) ListRegistered(ctx context.Context) ([]store.RegisteredChat, error) {
	r.runner.agentSlotsMu.Lock()
	background := r.runner.backgroundCancels["owner"] != nil
	r.runner.agentSlotsMu.Unlock()
	if background {
		close(r.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return []store.RegisteredChat{{JID: "tg:1", Folder: "owner", IsOwner: true}}, nil
}

func TestForegroundPreemptionDuringFreshAuthorizationReturnsCancellation(t *testing.T) {
	r := &Runner{}
	registrations := &preemptedRegistrationRead{runner: r, started: make(chan struct{})}
	r.SetAdmissionPolicy(1, registrations)
	r.ensureFn = func(context.Context, string, Tier, *mountsec.Allowlist) (string, error) {
		t.Error("preempted background authorization reached runtime")
		return "", errors.New("unexpected runtime access")
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.RunFresh(t.Context(), "owner", TierRegistered, nil, "extract")
		done <- err
	}()
	recvOrFail(t, registrations.started)
	release, err := r.acquireAgentSlot(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := recvOrFail(t, done); !errors.Is(err, context.Canceled) || errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("preempted extraction misclassified: %v", err)
	}
}
