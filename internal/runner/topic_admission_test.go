package runner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestNativeTopicAdmissionBeforeRuntime(t *testing.T) {
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "store.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	scope, err := st.SnapshotScope(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{}
	r.SetAdmissionPolicy(1, st)
	calls := 0
	r.ensureFn = func(context.Context, string, Tier, *mountsec.Allowlist) (string, error) {
		calls++
		return "existing", nil
	}
	d, _, err := st.BeginTopicDeletion(t.Context(), scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"deleting", "uncertain", "deleted", "retired"} {
		if state == "retired" {
			err = st.RetireDeletedTopic(t.Context(), d)
		} else if state != "deleting" {
			err = st.RecordTopicDeletionOutcome(t.Context(), d, state)
		}
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, err = r.turnRuntime(t.Context(), "owner", 7, TierOwner, nil)
		want := store.ErrTopicDeleting
		if state == "deleted" || state == "retired" {
			want = store.ErrConversationRetired
		}
		if !errors.Is(err, want) || calls != 0 {
			t.Fatalf("%s runtime started: calls=%d err=%v", state, calls, err)
		}
	}
	for _, thread := range []int{0, 8} {
		_, _, _, err = r.turnRuntime(t.Context(), "owner", thread, TierOwner, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("unrelated runtime calls=%d", calls)
	}
}

func TestNativeTopicAdmissionDoesNotChangeGroupOrLegacyReaders(t *testing.T) {
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "store.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:-2", Folder: "group", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	r := &Runner{}
	r.SetAdmissionPolicy(1, st)
	if err = r.validateTopicAdmission(t.Context(), "group", 77); err != nil {
		t.Fatalf("group topic newly denied: %v", err)
	}
	r.SetAdmissionPolicy(1, profileRegistrations{rows: []store.RegisteredChat{{JID: "tg:1", Folder: "owner", IsOwner: true}}})
	if err = r.validateTopicAdmission(t.Context(), "owner", 7); err != nil {
		t.Fatalf("legacy registration reader changed: %v", err)
	}
}

func TestNativeTopicAdmissionSurvivesStoreReopenAndRefusalReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.sqlite")
	st, err := store.Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	scope, err := st.SnapshotScope(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := st.BeginTopicDeletion(t.Context(), scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := &Runner{}
	r.SetAdmissionPolicy(1, st)
	if err = r.validateTopicAdmission(t.Context(), "owner", 7); !errors.Is(err, store.ErrTopicDeleting) {
		t.Fatalf("reopen lost fence: %v", err)
	}
	if err = st.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
		t.Fatal(err)
	}
	if err = r.validateTopicAdmission(t.Context(), "owner", 7); err != nil {
		t.Fatalf("refusal did not reopen: %v", err)
	}
}
