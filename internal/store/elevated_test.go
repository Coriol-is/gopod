package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestElevatedReservedFolder(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.RegisterChat(ctx, RegisteredChat{JID: "tg:1", Folder: ElevatedFolder, IsOwner: true, AddedAt: 1}); !errors.Is(err, ErrReservedFolder) {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(ctx, TaskRecord{ChatFolder: ElevatedFolder, Prompt: "test"}); !errors.Is(err, ErrReservedFolder) {
		t.Fatal(err)
	}
}

func TestElevatedJournalIntentSettlement(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	v, err := s.OpenElevatedSession(ctx, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.OpenElevatedSession(ctx, "codex"); err == nil {
		t.Fatal("second live session accepted")
	}
	id, err := s.BeginElevatedExec(ctx, v.ID, []string{"claude", "Bearer private-value", strings.Repeat("я", 5000)})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListElevatedExecs(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].EndedAt.Valid || rows[0].ExitCode.Valid || !rows[0].Truncated || len(rows[0].Argv) > 4096 || !utf8.ValidString(rows[0].Argv) || strings.Contains(rows[0].Argv, "private-value") {
		t.Fatalf("intent: %+v", rows)
	}
	if err = s.FinishElevatedExec(ctx, id, 0); err != nil {
		t.Fatal(err)
	}
	if err = s.FinishElevatedExec(ctx, id, 1); err == nil {
		t.Fatal("rewrote settled exec")
	}
	if err = s.CloseElevatedSession(ctx, v.ID, "stop"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginElevatedExec(ctx, v.ID, []string{"late"}); !errors.Is(err, ErrElevatedClosed) {
		t.Fatal(err)
	}
}

func TestElevatedContextIndependentOfMappingAndFencesLateResults(t *testing.T) {
	for _, mode := range []string{SessionMappingLegacy, SessionMappingDurable} {
		t.Run(mode, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			if err := s.SetState(ctx, "session_mapping", mode); err != nil {
				t.Fatal(err)
			}
			v, err := s.OpenElevatedSession(ctx, "claude")
			if err != nil {
				t.Fatal(err)
			}
			if err = s.PutElevatedNativeContext(ctx, v.ID, "elevated-native"); err != nil {
				t.Fatal(err)
			}
			if err = s.AppendElevatedTranscript(ctx, v.ID, "user", "first"); err != nil {
				t.Fatal(err)
			}
			if err = s.CloseElevatedSession(ctx, v.ID, "stop"); err != nil {
				t.Fatal(err)
			}
			next, err := s.OpenElevatedSession(ctx, "claude")
			if err != nil {
				t.Fatal(err)
			}
			id, err := s.ElevatedNativeContext(ctx, next.ID)
			if err != nil || id != "elevated-native" {
				t.Fatalf("resume %q %v", id, err)
			}
			if err = s.PutElevatedNativeContext(ctx, v.ID, "late"); !errors.Is(err, ErrElevatedClosed) {
				t.Fatalf("late write: %v", err)
			}
			if err = s.AppendElevatedTranscript(ctx, v.ID, "assistant", "late"); !errors.Is(err, ErrElevatedClosed) {
				t.Fatal(err)
			}
			if _, ok, err := s.GetSession(ctx, ElevatedFolder, 0, "claude"); err != nil || ok {
				t.Fatal("ordinary session contaminated", err)
			}
			if err = s.CloseElevatedSession(ctx, next.ID, "stop"); err != nil {
				t.Fatal(err)
			}
			codex, err := s.OpenElevatedSession(ctx, "codex")
			if err != nil {
				t.Fatal(err)
			}
			id, err = s.ElevatedNativeContext(ctx, codex.ID)
			if err != nil || id != "" {
				t.Fatal("provider context leaked", id, err)
			}
		})
	}
}

func TestElevatedLegacyRegistrationCannotProduceScope(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.RegisterChat(ctx, RegisteredChat{JID: "tg:1", Folder: "old-folder", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	// Simulate a row created before the folder became reserved.
	if _, err := s.db.ExecContext(ctx, `UPDATE registered_chats SET folder=? WHERE jid=?`, ElevatedFolder, "tg:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SnapshotScope(ctx, ElevatedFolder); !errors.Is(err, ErrReservedFolder) {
		t.Fatalf("legacy reserved scope accepted: %v", err)
	}
	if _, err := s.OpenElevatedSession(ctx, "claude"); !errors.Is(err, ErrReservedFolder) {
		t.Fatalf("legacy registration inherited: %v", err)
	}
}

func TestAdoptionSkipsHistoricalElevatedRegistration(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ownerScope(t, s)
	if err := s.PutSession(ctx, "owner", 0, "claude", "owner-context"); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterChat(ctx, RegisteredChat{JID: "tg:2", Folder: "old-elevated", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSession(ctx, "old-elevated", 0, "claude", "old-context"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE registered_chats SET folder='elevated' WHERE folder='old-elevated'; UPDATE sessions SET chat_folder='elevated' WHERE chat_folder='old-elevated'`); err != nil {
		t.Fatal(err)
	}
	report, err := s.AdoptLegacySessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != 1 || report.Sessions != 1 || report.Contexts != 1 || len(report.SkippedFolders) != 1 || report.SkippedFolders[0] != ElevatedFolder {
		t.Fatalf("report: %+v", report)
	}
}
