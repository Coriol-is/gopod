package store

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestManagedTaskKindValidation(t *testing.T) {
	f := newDeliveryFixture(t)
	cases := []struct {
		name string
		spec func() ManagedTask
		ok   bool
	}{
		{"agent", f.agentSpec, true},
		{"reminder without any prompt", f.reminderSpec, true},
		{"reminder at the byte limit", func() ManagedTask { s := f.reminderSpec(); s.LiteralText = strings.Repeat("a", 3500); return s }, true},
		{"agent without prompt", func() ManagedTask { s := f.agentSpec(); s.Prompt = " "; return s }, false},
		{"agent with literal text", func() ManagedTask { s := f.agentSpec(); s.LiteralText = "x"; return s }, false},
		{"agent main session", func() ManagedTask { s := f.agentSpec(); s.SessionMode = SessionLegacyMain; return s }, false},
		{"agent unknown delivery", func() ManagedTask { s := f.agentSpec(); s.DeliveryMode = "stdout"; return s }, false},
		{"agent controller_result is explicit", func() ManagedTask { s := f.agentSpec(); s.DeliveryMode = TaskDeliveryControllerResult; return s }, true},
		{"reminder with prompt", func() ManagedTask { s := f.reminderSpec(); s.Prompt = "p"; return s }, false},
		{"reminder empty text", func() ManagedTask { s := f.reminderSpec(); s.LiteralText = ""; return s }, false},
		{"reminder too long", func() ManagedTask { s := f.reminderSpec(); s.LiteralText = strings.Repeat("a", 3501); return s }, false},
		{"reminder bad utf8", func() ManagedTask { s := f.reminderSpec(); s.LiteralText = "\xff\xfe"; return s }, false},
		{"reminder legacy_ipc", func() ManagedTask { s := f.reminderSpec(); s.DeliveryMode = TaskDeliveryLegacyIPC; return s }, false},
		{"reminder with a session", func() ManagedTask { s := f.reminderSpec(); s.SessionMode = SessionIsolated; return s }, false},
		{"unknown kind", func() ManagedTask { s := f.agentSpec(); s.Kind = "workflow"; return s }, false},
		{"no timezone", func() ManagedTask { s := f.agentSpec(); s.Timezone = ""; return s }, false},
		{"bad timezone", func() ManagedTask { s := f.agentSpec(); s.Timezone = "Mars/Base"; return s }, false},
		{"no actor", func() ManagedTask { s := f.agentSpec(); s.ActorRef = ""; return s }, false},
		{"bad schedule type", func() ManagedTask { s := f.agentSpec(); s.ScheduleType = "weekly"; return s }, false},
		{"active without cursor", func() ManagedTask { s := f.agentSpec(); s.NextRun = 0; return s }, false},
		{"bad overlap", func() ManagedTask { s := f.agentSpec(); s.Overlap = "always"; return s }, false},
		{"queue limit above 10", func() ManagedTask { s := f.agentSpec(); s.QueueLimit = 11; return s }, false},
		{"bad dst policy", func() ManagedTask { s := f.agentSpec(); s.DSTFold = "twice"; return s }, false},
		{"grace beyond a day", func() ManagedTask { s := f.agentSpec(); s.MisfireGraceMs = MaxMisfireGraceMs + 1; return s }, false},
		{"no scope", func() ManagedTask { s := f.agentSpec(); s.Conversation = ConversationScope{}; return s }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := f.st.CreateManagedTask(t.Context(), c.spec())
			if c.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("invalid definition was accepted")
			}
			if c.ok && got.Revision != 1 {
				t.Fatalf("revision = %d", got.Revision)
			}
		})
	}
}

func TestCreateManagedTaskStoresCanonicalDefaults(t *testing.T) {
	f := newDeliveryFixture(t)
	r := f.create(f.reminderSpec())
	got, err := f.st.GetManagedTask(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "" || got.LiteralText != "stand up" || got.SessionMode != SessionNone || got.DeliveryMode != TaskDeliveryControllerResult {
		t.Fatalf("reminder shape: %+v", got)
	}
	if got.Overlap != OverlapSkip || got.Misfire != MisfireSkip || got.MisfireGraceMs != 120_000 || got.QueueLimit != 1 || got.CatchUpLimit != 1 || got.DSTFold != DSTFoldOnce {
		t.Fatalf("defaults: %+v", got)
	}
	if got.Conversation != f.conv || got.Revision != 1 || got.Status != "active" {
		t.Fatalf("identity: %+v", got)
	}
	a := f.create(f.agentSpec())
	if a.SessionMode != SessionIsolated || a.DeliveryMode != TaskDeliveryLegacyIPC {
		t.Fatalf("agent defaults: %+v", a)
	}
}

func TestCreateManagedTaskRejectsWrongScopeAndGroups(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	other := f.agentSpec()
	other.Conversation.Scope.NamespaceID = "22222222-2222-4222-8222-222222222222"
	if _, err := f.st.CreateManagedTask(ctx, other); err == nil {
		t.Fatal("foreign namespace accepted")
	}
	if err := f.st.RegisterChat(ctx, RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateManagedTask(ctx, f.agentSpec()); err == nil {
		t.Fatal("stale registration revision accepted")
	}
	conv, err := f.st.SnapshotConversation(ctx, "tg:1", 0, VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	// Groups are disabled until CP-020 qualifies them: by ID and by metadata.
	if err = f.st.RegisterChat(ctx, RegisteredChat{JID: "tg:-77", Folder: "grp", AddedAt: 1, RequiresTrigger: true}); err != nil {
		t.Fatal(err)
	}
	g, err := f.st.SnapshotConversation(ctx, "tg:-77", 0, VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	spec := f.agentSpec()
	spec.Conversation = g
	_, err = f.st.CreateManagedTask(ctx, spec)
	wantErr(t, err, ErrGroupTaskDisabled)
	if err = f.st.RegisterChat(ctx, RegisteredChat{JID: "tg:9", Folder: "pal", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err = f.st.UpsertChat(ctx, ChatRecord{JID: "tg:9", IsGroup: true, LastMessageTime: 1}); err != nil {
		t.Fatal(err)
	}
	p, err := f.st.SnapshotConversation(ctx, "tg:9", 0, VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	spec.Conversation = p
	_, err = f.st.CreateManagedTask(ctx, spec)
	wantErr(t, err, ErrGroupTaskDisabled)
	spec.Conversation = conv
	if _, err = f.st.CreateManagedTask(ctx, spec); err != nil {
		t.Fatalf("personal chat refused: %v", err)
	}
}

func TestUpdateManagedTaskExpectedRevision(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	task := f.create(f.agentSpec())
	prompt := "new prompt"
	up, err := f.st.UpdateManagedTask(ctx, "owner", task.ID, 1, TaskPatch{Prompt: &prompt})
	if err != nil || up.Revision != 2 || up.Prompt != prompt {
		t.Fatalf("update: %+v %v", up, err)
	}
	// A stale button / IPC update conflicts instead of overwriting.
	other := "stale"
	_, err = f.st.UpdateManagedTask(ctx, "owner", task.ID, 1, TaskPatch{Prompt: &other})
	wantErr(t, err, ErrRevisionConflict)
	// Wrong scope looks like a missing task.
	_, err = f.st.UpdateManagedTask(ctx, "elsewhere", task.ID, 2, TaskPatch{Prompt: &other})
	wantErr(t, err, ErrTaskNotFound)
	// An invalid patch changes nothing, not even the revision.
	empty := ""
	_, err = f.st.UpdateManagedTask(ctx, "owner", task.ID, 2, TaskPatch{Prompt: &empty})
	wantErr(t, err, ErrInvalidTask)
	if got, _ := f.st.GetManagedTask(ctx, task.ID); got.Revision != 2 || got.Prompt != prompt {
		t.Fatalf("rejected patch leaked: %+v", got)
	}
	// Pause and resume are revisioned; pausing does not stop a run (RequestRunStop does).
	p, err := f.st.SetManagedTaskStatus(ctx, "owner", task.ID, 2, "paused", 0)
	if err != nil || p.Status != "paused" || p.Revision != 3 {
		t.Fatalf("pause: %+v %v", p, err)
	}
	_, err = f.st.SetManagedTaskStatus(ctx, "owner", task.ID, 2, "active", 0)
	wantErr(t, err, ErrRevisionConflict)
	r, err := f.st.SetManagedTaskStatus(ctx, "owner", task.ID, 3, "active", baseTime+500)
	if err != nil || r.Status != "active" || r.NextRun != baseTime+500 || r.Revision != 4 {
		t.Fatalf("resume: %+v %v", r, err)
	}
}

func TestConcurrentUpdatesExactlyOneWins(t *testing.T) {
	f := newDeliveryFixture(t)
	task := f.create(f.agentSpec())
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := strings.Repeat("p", i+1)
			_, errs[i] = f.st.UpdateManagedTask(context.Background(), "owner", task.ID, 1, TaskPatch{Prompt: &p})
		}()
	}
	wg.Wait()
	wins := 0
	for _, e := range errs {
		if e == nil {
			wins++
		} else {
			wantErr(t, e, ErrRevisionConflict)
		}
	}
	if got, _ := f.st.GetManagedTask(t.Context(), task.ID); wins != 1 || got.Revision != 2 {
		t.Fatalf("wins=%d revision=%d", wins, got.Revision)
	}
}

func TestRebindManagedTaskScope(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	task := f.create(f.agentSpec())
	if err := f.st.RegisterChat(ctx, RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.st.SnapshotScope(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	foreign := fresh
	foreign.NamespaceID = "22222222-2222-4222-8222-222222222222"
	_, err = f.st.RebindManagedTaskScope(ctx, "owner", task.ID, 1, foreign)
	wantErr(t, err, ErrScopeIdentityChanged)
	got, err := f.st.RebindManagedTaskScope(ctx, "owner", task.ID, 1, fresh)
	if err != nil || got.Revision != 2 || got.Conversation.Scope.RegistrationRevision != fresh.RegistrationRevision {
		t.Fatalf("rebind: %+v %v", got, err)
	}
}

func TestDeleteTaskTombstonesManagedAndGuardsLegacyMutators(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	task := f.create(f.agentSpec())
	legacyID, err := f.st.CreateTask(ctx, TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "p", ScheduleType: "interval", ScheduleValue: "1h", NextRun: baseTime})
	if err != nil {
		t.Fatal(err)
	}
	// The legacy scheduler query never sees managed rows; the managed one never sees legacy.
	due, err := f.st.GetDueTasks(ctx, baseTime+1)
	if err != nil || len(due) != 1 || due[0].ID != legacyID {
		t.Fatalf("legacy due: %+v %v", due, err)
	}
	md, err := f.st.DueManagedTasks(ctx, baseTime+1, 10)
	if err != nil || len(md) != 1 || md[0].ID != task.ID {
		t.Fatalf("managed due: %+v %v", md, err)
	}
	// Legacy mutators refuse managed definitions: no revision bypass.
	wantErr(t, f.st.UpdateTaskStatus(ctx, task.ID, "paused", 0, 0), ErrManagedTask)
	wantErr(t, f.st.UpdateTaskDefinition(ctx, TaskRecord{ID: task.ID, ChatFolder: "owner", Prompt: "x", ScheduleType: "once", ScheduleValue: "v"}), ErrManagedTask)
	wantErr(t, f.st.UpdateTaskStatus(ctx, "nope", "paused", 0, 0), ErrTaskNotFound)

	if err = f.st.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.st.GetManagedTask(ctx, task.ID)
	if err != nil || !got.Tombstoned() || got.Revision != 2 || got.Status != "done" || got.NextRun != 0 || got.TerminalReason != "deleted" {
		t.Fatalf("tombstone: %+v %v", got, err)
	}
	if list, _ := f.st.ListTasksByChat(ctx, "owner"); len(list) != 1 || list[0].ID != legacyID {
		t.Fatalf("tombstone visible in the active panel: %+v", list)
	}
	if md, _ = f.st.DueManagedTasks(ctx, baseTime+1, 10); len(md) != 0 {
		t.Fatal("tombstone is still due")
	}
	if err = f.st.DeleteTask(ctx, task.ID); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	wantErr(t, f.st.DeleteManagedTask(ctx, "owner", task.ID, 2, "deleted"), ErrTaskTombstoned)
	_, err = f.st.UpdateManagedTask(ctx, "owner", task.ID, 2, TaskPatch{})
	wantErr(t, err, ErrTaskTombstoned)
	// Raw deletion and resurrection are rejected by the schema.
	if execErr(f.st, `DELETE FROM scheduled_tasks WHERE id=?`, task.ID) == nil {
		t.Fatal("raw delete of a managed definition was accepted")
	}
	if execErr(f.st, `UPDATE scheduled_tasks SET deleted_at=NULL, status='active', next_run=5 WHERE id=?`, task.ID) == nil {
		t.Fatal("tombstone was revived")
	}
	// Recreating a similar task gets a new ID and no inherited history.
	again := f.create(f.agentSpec())
	if again.ID == task.ID {
		t.Fatal("task ID reused")
	}
	if occ, _ := f.st.ListTaskOccurrences(ctx, again.ID, 10); len(occ) != 0 {
		t.Fatal("history inherited")
	}
	// Legacy deletion stays physical.
	if err = f.st.DeleteTask(ctx, legacyID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.GetTask(ctx, legacyID); err == nil {
		t.Fatal("legacy row not deleted")
	}
}

func TestLegacyDeleteRefusedWhenManagedRowsReferenceIt(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	legacyID, err := f.st.CreateTask(ctx, TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "p", ScheduleType: "interval", ScheduleValue: "1h", NextRun: baseTime})
	if err != nil {
		t.Fatal(err)
	}
	sc := f.conv.Scope
	mustExec(t, f.st, `INSERT INTO task_occurrences(occurrence_id,task_id,definition_revision,nominal_at,namespace_id,namespace_epoch,registration_revision,chat_jid,chat_folder,thread_id,visibility,decision,reason,created_at)
 VALUES('o1',?,1,1,?,?,?,?,?,0,'conversation','skipped','test',1)`, legacyID, sc.NamespaceID, sc.NamespaceEpoch, sc.RegistrationRevision, sc.JID, sc.Folder)
	wantErr(t, f.st.DeleteTask(ctx, legacyID), ErrTaskHasManagedEvidence)
	// Even bypassing the check, the foreign key restricts the raw delete.
	if execErr(f.st, `DELETE FROM scheduled_tasks WHERE id=?`, legacyID) == nil {
		t.Fatal("foreign key did not restrict the parent delete")
	}
	if _, err = f.st.GetTask(ctx, legacyID); err != nil {
		t.Fatal(err)
	}
}
