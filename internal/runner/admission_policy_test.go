package runner

import (
	"context"
	"errors"
	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"github.com/Coriol-is/gopod/internal/store"
	"slices"
	"testing"
)

type profileRegistrations struct {
	rows []store.RegisteredChat
	err  error
}

func (s profileRegistrations) ListRegistered(context.Context) ([]store.RegisteredChat, error) {
	return s.rows, s.err
}

// Transport/session tests model independently privileged personal runtimes using
// fake registrations; production SQLite enforces unique owner JID membership.
func testPersonalPolicy() *admissionPolicy {
	rows := []store.RegisteredChat{}
	for _, folder := range []string{"owner", "chat", "chat-a", "chat-b", "a", "b", "c", "bg1", "bg2", "test-chat", "alice", "x", "other", "user"} {
		rows = append(rows, store.RegisteredChat{JID: "tg:1", Folder: folder, IsOwner: true})
	}
	return &admissionPolicy{ownerID: 1, registrations: profileRegistrations{rows: rows}}
}

func TestProfileAdmissionIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		owner  int64
		rows   []store.RegisteredChat
		folder string
		valid  bool
	}{
		{"personal", 1, []store.RegisteredChat{{JID: "tg:1", Folder: "personal", IsOwner: true}}, "personal", true},
		{"group-owner-config", -1, []store.RegisteredChat{{JID: "tg:-1", Folder: "owner", IsOwner: true}}, "owner", false},
		{"group-forged-owner", 1, []store.RegisteredChat{{JID: "tg:-2", Folder: "owner", IsOwner: true}}, "owner", false},
		{"other-private-forged-owner", 1, []store.RegisteredChat{{JID: "tg:2", Folder: "owner", IsOwner: true}}, "owner", false},
		{"inconsistent-owner-flag", 1, []store.RegisteredChat{{JID: "tg:1", Folder: "personal"}}, "personal", false},
		{"ambiguous-folder", 1, []store.RegisteredChat{{JID: "tg:1", Folder: "owner", IsOwner: true}, {JID: "tg:-2", Folder: "owner"}}, "owner", false},
		{"unknown", 1, nil, "unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{}
			r.SetAdmissionPolicy(tc.owner, profileRegistrations{rows: tc.rows})
			err := r.authorizeProfile(context.Background(), tc.folder)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	r := &Runner{}
	if !errors.Is(r.authorizeProfile(context.Background(), "owner"), ErrProfileUnavailable) {
		t.Fatal("unset policy admitted")
	}
	r.SetAdmissionPolicy(1, profileRegistrations{err: errors.New("store down")})
	if !errors.Is(r.authorizeProfile(context.Background(), "owner"), ErrProfileUnavailable) {
		t.Fatal("store error admitted")
	}
}

func TestUnqualifiedProfilesNeverTouchRuntime(t *testing.T) {
	for _, folder := range []string{"group", "owner", "missing"} {
		t.Run(folder, func(t *testing.T) {
			r := &Runner{}
			r.SetAdmissionPolicy(1, profileRegistrations{rows: []store.RegisteredChat{{JID: "tg:-2", Folder: "group"}, {JID: "tg:2", Folder: "owner", IsOwner: true}}})
			// Nil Docker/provider/store: a denied path touching these would panic.
			ctx := context.Background()
			check := func(err error) {
				t.Helper()
				if !errors.Is(err, ErrProfileUnavailable) {
					t.Fatalf("not denied: %v", err)
				}
			}
			_, err := r.Ensure(ctx, folder, TierOwner, nil)
			check(err)
			_, err = r.ensure(ctx, folder, TierOwner, nil)
			check(err)
			_, err = r.Run(ctx, folder, 0, TierOwner, nil, "canary")
			check(err)
			_, err = r.RunStream(ctx, folder, 0, TierOwner, nil, "canary")
			check(err)
			_, err = r.RunFresh(ctx, folder, TierOwner, nil, "canary")
			check(err)
			_, err = r.CheckAuth(ctx, folder, TierOwner, nil)
			check(err)
			check(r.ClearSession(ctx, folder, 0, TierOwner, nil))
			_, err = r.CompactSession(ctx, folder, 0, TierOwner, nil)
			check(err)
			check(r.SwitchChatProvider(ctx, folder, NewCodexProvider("")))
			report := r.StartupCheck(ctx, folder, TierOwner, nil)
			if report.Container != "profile_unavailable" {
				t.Fatalf("startup: %+v", report)
			}
			management := r.managementCheck(ctx, folder, func(context.Context) (string, error) { t.Fatal("inspected group runtime"); return "", nil }, func(context.Context, string) (ManagementReport, error) {
				t.Fatal("probed group runtime")
				return ManagementReport{}, nil
			})
			if management.ErrorClass != "profile_unavailable" {
				t.Fatalf("management: %+v", management)
			}
		})
	}
}

func TestOwnerBackgroundTierResolvedFromRegistration(t *testing.T) {
	r := &Runner{}
	r.SetAdmissionPolicy(1, profileRegistrations{rows: []store.RegisteredChat{{JID: "tg:1", Folder: "personal", IsOwner: true}}})
	r.ensureFn = func(_ context.Context, folder string, tier Tier, _ *mountsec.Allowlist) (string, error) {
		if tier != TierOwner || folder != "personal" {
			t.Fatalf("incorrect trusted tier %v / %s", tier, folder)
		}
		return "existing-personal", nil
	}
	got, err := r.ensure(context.Background(), "personal", TierRegistered, nil)
	if err != nil || got != "existing-personal" {
		t.Fatalf("owner background: %q %v", got, err)
	}
}

func TestContainerEnvironmentNoRestrictedCredentials(t *testing.T) {
	for _, prov := range []AgentProvider{NewClaudeProvider(""), NewCodexProvider("")} {
		if env := containerEnvAllow(TierRegistered, prov, []string{"PRIVATE_CANARY"}); len(env) != 0 {
			t.Fatalf("restricted env inherited %v", env)
		}
		if env := containerEnvAllow(TierOwner, prov, []string{"PRIVATE_CANARY"}); !slices.Contains(env, "PRIVATE_CANARY") {
			t.Fatal("personal env changed")
		}
	}
}

type revokingRegistrations struct{ calls int }

func (s *revokingRegistrations) ListRegistered(context.Context) ([]store.RegisteredChat, error) {
	s.calls++
	if s.calls == 1 {
		return []store.RegisteredChat{{JID: "tg:1", Folder: "personal", IsOwner: true}}, nil
	}
	return []store.RegisteredChat{{JID: "tg:-2", Folder: "personal"}}, nil
}
func TestAdmissionRecheckedAfterSlotBeforeRuntime(t *testing.T) {
	r := &Runner{}
	r.SetAdmissionPolicy(1, &revokingRegistrations{})
	r.ensureFn = func(context.Context, string, Tier, *mountsec.Allowlist) (string, error) {
		t.Fatal("stale profile reached runtime")
		return "", nil
	}
	_, err := r.Run(context.Background(), "personal", 0, TierOwner, nil, "canary")
	if !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("profile revocation not denied: %v", err)
	}
}
