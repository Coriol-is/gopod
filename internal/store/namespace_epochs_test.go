package store

import (
	"errors"
	"testing"
)

func TestGroupEpochMigrationPreservesOldStateAndPersonalData(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	for _, c := range []RegisteredChat{{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}, {JID: "tg:-2", Folder: "group-a", AddedAt: 2}, {JID: "tg:-3", Folder: "group-b", AddedAt: 3}} {
		if err := st.RegisterChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := st.SnapshotScope(ctx, "owner")
	old, _ := st.SnapshotScope(ctx, "group-a")
	other, _ := st.SnapshotScope(ctx, "group-b")
	if _, err := st.db.ExecContext(ctx, `INSERT INTO conversation_history(chat_folder,thread_id,role,content) VALUES('owner',0,'user','personal-canary'),('group-a',0,'user','old-group-canary')`); err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{"owner", "group-a", "group-b"} {
		if _, err := st.CreateTask(ctx, TaskRecord{ID: folder, ChatFolder: folder, ChatJID: "tg:-2", Prompt: "do not replay", ScheduleType: "cron", ScheduleValue: "0 9 * * *", NextRun: 1, CreatedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	preview, err := st.PreviewGroupEpoch(ctx, "tg:-2")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Current != old || preview.ActiveTasks != 1 || preview.Ready {
		t.Fatalf("preview %+v", preview)
	}
	if err = st.ValidateScope(ctx, old); err != nil {
		t.Fatal("preview mutated state")
	}
	migrated, err := st.RotateGroupEpoch(ctx, "tg:-2", old.RegistrationRevision)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.PausedTasks != 1 || migrated.CurrentScope.NamespaceID != old.NamespaceID || migrated.CurrentScope.NamespaceEpoch != 2 || migrated.CurrentScope.Folder == old.Folder {
		t.Fatalf("migration %+v", migrated)
	}
	c, _ := st.GetRegistered(ctx, "tg:-2")
	if c.GroupEpochReady {
		t.Fatal("rotation enabled incomplete epoch")
	}
	if !errors.Is(st.ValidateScope(ctx, old), ErrStaleScope) {
		t.Fatal("retired scope resumed")
	}
	for _, ref := range []ScopeRef{before, other} {
		if err = st.ValidateScope(ctx, ref); err != nil {
			t.Fatal("foreign namespace touched")
		}
	}
	var retained, imported int
	st.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_history WHERE chat_folder='group-a'`).Scan(&retained)
	st.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_history WHERE chat_folder=?`, c.Folder).Scan(&imported)
	if retained != 1 || imported != 0 {
		t.Fatal("old state erased/imported")
	}
	for _, folder := range []string{"owner", "group-b"} {
		task, e := st.GetTask(ctx, folder)
		if e != nil || task.Status != "active" {
			t.Fatal("foreign task changed")
		}
	}
	task, _ := st.GetTask(ctx, "group-a")
	if task.Status != "paused" || task.ChatFolder != "group-a" {
		t.Fatal("legacy task replay/import")
	}
	if _, err = st.RotateGroupEpoch(ctx, "tg:-2", c.RegistrationRevision); err == nil {
		t.Fatal("incomplete epoch rotated again")
	}
	if _, err = st.CompleteGroupEpoch(ctx, old); !errors.Is(err, ErrStaleScope) {
		t.Fatal("stale completion")
	}
	complete, err := st.CompleteGroupEpoch(ctx, c.Scope())
	if err != nil {
		t.Fatal(err)
	}
	c, _ = st.GetRegistered(ctx, "tg:-2")
	if !c.GroupEpochReady || c.Scope() != complete {
		t.Fatal("completion not durable")
	}
	if err = st.RegisterChat(ctx, RegisteredChat{JID: "tg:-4", Folder: "group-a", AddedAt: 4}); !errors.Is(err, ErrNamespaceIdentityConflict) {
		t.Fatal("retired alias transferred")
	}
}

func TestEpochMigrationRefusesPersonalAndStaleIdentity(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	for _, c := range []RegisteredChat{{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}, {JID: "tg:-2", Folder: "group", AddedAt: 2}} {
		if err := st.RegisterChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.RotateGroupEpoch(ctx, "tg:1", 1); !errors.Is(err, ErrNamespaceIdentityConflict) {
		t.Fatal("personal migration allowed")
	}
	if _, err := st.RotateGroupEpoch(ctx, "tg:-2", 2); !errors.Is(err, ErrStaleScope) {
		t.Fatal("stale migration allowed")
	}
	ref, _ := st.SnapshotScope(ctx, "group")
	if _, err := st.CompleteGroupEpoch(ctx, ref); !errors.Is(err, ErrNamespaceIdentityConflict) {
		t.Fatal("legacy epoch qualified")
	}
}

func TestInterruptedEpochRemainsClosedAndCanCompleteAfterReopen(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	if err := st.RegisterChat(ctx, RegisteredChat{JID: "tg:-2", Folder: "group", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	c, _ := st.GetRegistered(ctx, "tg:-2")
	migration, err := st.RotateGroupEpoch(ctx, c.JID, c.RegistrationRevision)
	if err != nil {
		t.Fatal(err)
	}
	path := st.Path()
	st.Close()
	reopened, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	c, err = reopened.GetRegistered(ctx, "tg:-2")
	if err != nil {
		t.Fatal(err)
	}
	if c.GroupEpochReady || c.Scope() != migration.CurrentScope {
		t.Fatal("restart enabled/imported old epoch")
	}
	if _, err = reopened.GetByFolder(ctx, "group"); !errors.Is(err, ErrChatNotRegistered) {
		t.Fatal("old registration retained")
	}
	if _, err = reopened.CompleteGroupEpoch(ctx, c.Scope()); err != nil {
		t.Fatal(err)
	}
	c, _ = reopened.GetRegistered(ctx, "tg:-2")
	if !c.GroupEpochReady {
		t.Fatal("completion lost")
	}
}
