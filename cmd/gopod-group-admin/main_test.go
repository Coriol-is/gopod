package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

func args(action string) []string {
	return []string{"--config", "/opt/gopod/data/group-runtime.json", "--data", "/opt/gopod/data", "--store", "/opt/gopod/data/store.sqlite", "--jid", "tg:-2002", "--controller-name", "gopod-gopod-1", "--action", action}
}
func TestExplicitOfflineArgumentsAndRevisionRequired(t *testing.T) {
	if _, err := parse(args("status")); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"prepare", "enable", "disable"} {
		if _, err := parse(args(action)); err == nil {
			t.Fatal("mutation without expected revision")
		}
		if _, err := parse(append(args(action), "--expected-revision", "4")); err != nil {
			t.Fatal(err)
		}
	}
	for _, extra := range [][]string{{"--controller-name", ""}, {"--jid", "tg:1"}, {"--store", "/other/store.sqlite"}, {"--config", "/other/policy.json"}, {"--data", "relative"}, {"--jid", "tg:-02002"}, {"--action", "run"}} {
		if _, err := parse(append(args("status"), extra...)); err == nil {
			t.Fatalf("unsafe options accepted %v", extra)
		}
	}
}

type fakeRegistration struct {
	row   store.RegisteredChat
	stale bool
}

func (s fakeRegistration) GetRegistered(context.Context, string) (store.RegisteredChat, error) {
	return s.row, nil
}
func (s fakeRegistration) ValidateScope(context.Context, store.ScopeRef) error {
	if s.stale {
		return errors.New("stale")
	}
	return nil
}

type fakeActions struct{ called string }

func (a *fakeActions) Status(context.Context, string) (string, error) {
	a.called = "status"
	return "status", nil
}
func (a *fakeActions) Prepare(context.Context, string) (string, error) {
	a.called = "prepare"
	return "prepared", nil
}
func (a *fakeActions) Enable(context.Context, string) (string, error) {
	a.called = "enable"
	return "enabled", nil
}
func (a *fakeActions) Disable(context.Context, string) (string, error) {
	a.called = "disable"
	return "disabled", nil
}
func TestOfflineOperationRejectsStaleRevisionOrRestartedController(t *testing.T) {
	o, _ := parse(append(args("prepare"), "--expected-revision", "4"))
	row := store.RegisteredChat{JID: o.jid, Folder: "group", RegistrationRevision: 4}
	for _, tc := range []struct {
		name           string
		row            store.RegisteredChat
		stale, running bool
		valid          bool
	}{{"accepted", row, false, false, true}, {"revision changed", store.RegisteredChat{JID: o.jid, RegistrationRevision: 5}, false, false, false}, {"retired", row, true, false, false}, {"controller restarted", row, false, true, false}, {"owner", store.RegisteredChat{JID: o.jid, RegistrationRevision: 4, IsOwner: true}, false, false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			a := &fakeActions{}
			_, err := execute(context.Background(), o, fakeRegistration{tc.row, tc.stale}, a, func(context.Context) error {
				if tc.running {
					return errors.New("running")
				}
				return nil
			})
			if (err == nil) != tc.valid || (!tc.valid && a.called != "") {
				t.Fatalf("called=%q err=%v", a.called, err)
			}
		})
	}
}

type fakeInspector struct {
	value container.InspectResponse
	err   error
}

func (f fakeInspector) ContainerInspect(context.Context, string) (container.InspectResponse, error) {
	return f.value, f.err
}
func TestControllerProofRequiresStoppedMatchingDeployment(t *testing.T) {
	data := "/opt/gopod/data"
	good := container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{State: &container.State{Status: "exited", Pid: 0}}, Config: &container.Config{Labels: map[string]string{"com.docker.compose.service": "gopod"}}, Mounts: []container.MountPoint{{Type: mount.TypeBind, Source: data, Destination: data, RW: true}}}
	if err := controllerStopped(context.Background(), fakeInspector{value: good}, "gopod-gopod-1", data); err != nil {
		t.Fatal(err)
	}
	running := good
	running.ContainerJSONBase = &container.ContainerJSONBase{State: &container.State{Status: "running", Running: true, Pid: 7}}
	wrong := good
	wrong.Mounts = nil
	for _, f := range []fakeInspector{{err: errors.New("missing name")}, {value: running}, {value: wrong}, {}} {
		if controllerStopped(context.Background(), f, "gopod-gopod-1", data) == nil {
			t.Fatal("unknown or running controller accepted")
		}
	}
}
func TestAdminLockRejectsSymlinkAndSerializesInvocations(t *testing.T) {
	if os.Geteuid() <= 0 {
		t.Skip("nonroot operator fixture")
	}
	data, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(data, 0700)
	unlock, err := adminLock(data, os.Geteuid())
	if err != nil {
		t.Fatal(err)
	}
	if second, err := adminLock(data, os.Geteuid()); err == nil {
		second()
		unlock()
		t.Fatal("concurrent admin admitted")
	}
	unlock()
	if err = os.Remove(filepath.Join(data, ".group-admin.lock")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(data, "elsewhere"), filepath.Join(data, ".group-admin.lock")); err != nil {
		t.Fatal(err)
	}
	if unlock, err = adminLock(data, os.Geteuid()); err == nil {
		unlock()
		t.Fatal("followed lock symlink")
	}
}
