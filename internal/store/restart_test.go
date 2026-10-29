package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestRestartReceiptReplayAcrossOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.sqlite")
	st, err := Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	req := RestartRecord{ChatID: 1, ThreadID: 7, MessageID: 15}
	if err = st.BeginRestart(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if err = st.SetRestartPhase(t.Context(), 15, "requested", "restarting"); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.ReadRestart(t.Context())
	if err != nil || got.Phase != "restarting" || got.ThreadID != 7 {
		t.Fatal(got, err)
	}
	if err = st.BeginRestart(t.Context(), req); !errors.Is(err, ErrRestartReplay) {
		t.Fatal(err)
	}
	req.MessageID++
	if err = st.BeginRestart(t.Context(), req); !errors.Is(err, ErrRestartPending) {
		t.Fatal(err)
	}
	if err = st.SetRestartPhase(t.Context(), 14, "restarting", "ready"); !errors.Is(err, ErrRestartReplay) {
		t.Fatal(err)
	}
	if err = st.SetRestartPhase(t.Context(), 15, "restarting", "notice_claimed"); err != nil {
		t.Fatal(err)
	}
	if err = st.SetRestartPhase(t.Context(), 15, "restarting", "ready"); !errors.Is(err, ErrRestartReplay) {
		t.Fatal(err)
	}
}

func TestAbortRestartCoversAmbiguousPhaseWriteAndPreservesTerminal(t *testing.T) {
	st, err := Open(t.Context(), filepath.Join(t.TempDir(), "store.sqlite"), silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.BeginRestart(t.Context(), RestartRecord{ChatID: 1, MessageID: 17}); err != nil {
		t.Fatal(err)
	}
	if err = st.SetRestartPhase(t.Context(), 17, "requested", "restarting"); err != nil {
		t.Fatal(err)
	}
	// Simulate a committed transition whose caller did not receive acknowledgement.
	if err = st.AbortRestart(t.Context(), 17, "aborted"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.ReadRestart(t.Context())
	if got.Phase != "aborted" {
		t.Fatal(got)
	}
	if err = st.AbortRestart(t.Context(), 17, "notice_unknown"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ReadRestart(t.Context())
	if got.Phase != "aborted" {
		t.Fatal("terminal record overwritten", got)
	}
}
