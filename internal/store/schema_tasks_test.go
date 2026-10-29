package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// openPreTaskSchema builds a database exactly as it looked before the task
// reliability schema: every earlier schema step, and not applyTaskSchema.
func openPreTaskSchema(t *testing.T, path string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path, log: silentLogger()}
	for _, stmt := range schemaStatements {
		if _, err = db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.migrateSessions(ctx); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range migrations {
		if _, err = db.ExecContext(ctx, stmt); err != nil && !isDuplicateColumn(err) {
			t.Fatal(err)
		}
	}
	if err = s.initializeNamespaceMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.applyDeliverySchema(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func schemaDump(t *testing.T, st *Store) []string {
	t.Helper()
	rows, err := st.db.QueryContext(context.Background(), `SELECT type||':'||name||':'||IFNULL(sql,'') FROM sqlite_master ORDER BY type,name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func TestLegacyFixtureMigratesAdditivelyAndRestartIdempotently(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db := openPreTaskSchema(t, path)
	const digest = "Summarize the feed.\nKeep the bytes, including trailing space. "
	for _, q := range []string{
		`INSERT INTO registered_chats(jid,name,folder,requires_trigger,is_owner,added_at,namespace_id,namespace_epoch,registration_revision) VALUES('tg:1','n','owner',0,1,1,'11111111-1111-4111-8111-111111111111',1,1)`,
		`INSERT INTO scheduled_tasks(id,chat_folder,chat_jid,prompt,schedule_type,schedule_value,next_run,last_run,status,created_at) VALUES('legacy-1','owner','tg:1','` + digest + `','cron','CRON_TZ=Europe/Belgrade 0 9 * * *',1700000000000,1699999000000,'active',5)`,
		`INSERT INTO scheduled_tasks(id,chat_folder,chat_jid,prompt,schedule_type,schedule_value,next_run,status,created_at) VALUES('legacy-2','owner','tg:1','p','once','2030-01-01T00:00:00Z',0,'done',6)`,
		`INSERT INTO task_run_logs(task_id,run_at,duration_ms,status,result,error,turn_id) VALUES('legacy-1',1699999000000,12,'success','ok',NULL,NULL)`,
		`INSERT INTO turns(source,source_id,chat_folder,chat_id,text,status,created_at) VALUES('task','legacy-1:1699999000000','owner',0,'p','done',7)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(ctx, path, silentLogger())
	if err != nil {
		t.Fatalf("migrating Open: %v", err)
	}
	got, err := st.GetTask(ctx, "legacy-1")
	if err != nil || got.Prompt != digest || got.ScheduleValue != "CRON_TZ=Europe/Belgrade 0 9 * * *" || got.NextRun != 1700000000000 || got.LastRun != 1699999000000 {
		t.Fatalf("legacy row changed: %+v err=%v", got, err)
	}
	var version, rev, thread, ns int
	var kind, session, delivery, vis string
	var literal sql.NullString
	if err = st.db.QueryRowContext(ctx, `SELECT execution_version,definition_revision,thread_id,kind,session_mode,delivery_mode,visibility,literal_text,namespace_epoch FROM scheduled_tasks WHERE id='legacy-1'`).
		Scan(&version, &rev, &thread, &kind, &session, &delivery, &vis, &literal, &ns); err != nil {
		t.Fatal(err)
	}
	if version != 0 || rev != 1 || thread != 0 || kind != "agent" || session != SessionLegacyMain || delivery != TaskDeliveryLegacyIPC || vis != "" || literal.Valid || ns != 0 {
		t.Fatalf("legacy defaults: %d %d %d %s %s %s %q %v %d", version, rev, thread, kind, session, delivery, vis, literal, ns)
	}
	if n := countRows(t, st, `SELECT count(*) FROM task_runs`) + countRows(t, st, `SELECT count(*) FROM task_occurrences`) + countRows(t, st, `SELECT count(*) FROM task_attempts`); n != 0 {
		t.Fatalf("migration fabricated %d ledger rows", n)
	}
	if tasks, err := st.GetDueTasks(ctx, 1700000000001); err != nil || len(tasks) != 1 || tasks[0].ID != "legacy-1" {
		t.Fatalf("legacy scheduler path: %+v %v", tasks, err)
	}
	if _, err = st.GetManagedTask(ctx, "legacy-1"); err == nil || !strings.Contains(err.Error(), "not a managed") {
		t.Fatalf("GetManagedTask on legacy row: %v", err)
	}
	if err = st.UpdateTaskStatus(ctx, "legacy-1", "paused", 0, 1699999000000); err != nil {
		t.Fatalf("legacy status update: %v", err)
	}
	if n := countRows(t, st, `SELECT count(*) FROM task_run_logs`) + countRows(t, st, `SELECT count(*) FROM turns`); n != 2 {
		t.Fatalf("old logs/turns changed: %d", n)
	}
	first := schemaDump(t, st)
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}

	st2, err := Open(ctx, path, silentLogger())
	if err != nil {
		t.Fatalf("restart Open: %v", err)
	}
	defer st2.Close()
	second := schemaDump(t, st2)
	if strings.Join(first, "\n") != strings.Join(second, "\n") {
		t.Fatal("schema changed on restart")
	}
	if got, err := st2.GetTask(ctx, "legacy-1"); err != nil || got.Status != "paused" || got.Prompt != digest {
		t.Fatalf("after restart: %+v %v", got, err)
	}
	// Legacy deletion stays physical while nothing managed references the row.
	if err = st2.DeleteTask(ctx, "legacy-2"); err != nil {
		t.Fatal(err)
	}
	if _, err = st2.GetTask(ctx, "legacy-2"); err == nil {
		t.Fatal("legacy row survived DeleteTask")
	}
}

func TestSchemaConstraintViolations(t *testing.T) {
	f := newDeliveryFixture(t)
	st := f.st
	mgd := f.create(f.agentSpec())

	bad := map[string]string{
		"unknown kind":            `UPDATE scheduled_tasks SET kind='cron' WHERE id='` + mgd.ID + `'`,
		"kind is immutable":       `UPDATE scheduled_tasks SET kind='reminder' WHERE id='` + mgd.ID + `'`,
		"actor is immutable":      `UPDATE scheduled_tasks SET actor_ref='someone' WHERE id='` + mgd.ID + `'`,
		"namespace is immutable":  `UPDATE scheduled_tasks SET namespace_epoch=2 WHERE id='` + mgd.ID + `'`,
		"execution_version flip":  `UPDATE scheduled_tasks SET execution_version=0 WHERE id='` + mgd.ID + `'`,
		"revision skips a step":   `UPDATE scheduled_tasks SET definition_revision=9 WHERE id='` + mgd.ID + `'`,
		"revision zero":           `UPDATE scheduled_tasks SET definition_revision=0 WHERE id='` + mgd.ID + `'`,
		"agent loses prompt":      `UPDATE scheduled_tasks SET prompt='' WHERE id='` + mgd.ID + `'`,
		"agent gains literal":     `UPDATE scheduled_tasks SET literal_text='x' WHERE id='` + mgd.ID + `'`,
		"managed row hard delete": `DELETE FROM scheduled_tasks WHERE id='` + mgd.ID + `'`,
		"bad overlap":             `UPDATE scheduled_tasks SET overlap_policy='always' WHERE id='` + mgd.ID + `'`,
		"queue limit too high":    `UPDATE scheduled_tasks SET queue_limit=11 WHERE id='` + mgd.ID + `'`,
		"empty timezone":          `UPDATE scheduled_tasks SET timezone='' WHERE id='` + mgd.ID + `'`,
		"legacy gets thread":      `INSERT INTO scheduled_tasks(id,chat_folder,chat_jid,prompt,schedule_type,schedule_value,status,created_at,thread_id) VALUES('x','owner','tg:1','p','once','v','active',1,3)`,
		"legacy gets mode":        `INSERT INTO scheduled_tasks(id,chat_folder,chat_jid,prompt,schedule_type,schedule_value,status,created_at,session_mode) VALUES('x','owner','tg:1','p','once','v','active',1,'isolated')`,
		"managed without scope":   `INSERT INTO scheduled_tasks(id,chat_folder,chat_jid,prompt,schedule_type,schedule_value,status,created_at,execution_version) VALUES('x','owner','tg:1','p','once','v','active',1,1)`,
		"reminder with a prompt": `INSERT INTO scheduled_tasks(id,chat_folder,chat_jid,prompt,schedule_type,schedule_value,status,created_at,execution_version,kind,literal_text,session_mode,delivery_mode,namespace_id,namespace_epoch,registration_revision,visibility,actor_ref,timezone,overlap_policy,misfire_policy,dst_fold_policy,queue_limit,catch_up_limit)
  VALUES('x','owner','tg:1','prompt','once','v','active',1,1,'reminder','t','none','controller_result','n',1,1,'conversation','a','UTC','skip','skip','once',1,1)`,
		"reminder over 3500 bytes": `INSERT INTO scheduled_tasks(id,chat_folder,chat_jid,prompt,schedule_type,schedule_value,status,created_at,execution_version,kind,literal_text,session_mode,delivery_mode,namespace_id,namespace_epoch,registration_revision,visibility,actor_ref,timezone,overlap_policy,misfire_policy,dst_fold_policy,queue_limit,catch_up_limit)
  VALUES('x','owner','tg:1','','once','v','active',1,1,'reminder','` + strings.Repeat("a", 3501) + `','none','controller_result','n',1,1,'conversation','a','UTC','skip','skip','once',1,1)`,
	}
	for name, q := range bad {
		if err := execErr(st, q); err == nil {
			t.Errorf("%s: statement was accepted", name)
		}
	}
	if got, err := st.GetManagedTask(f.t.Context(), mgd.ID); err != nil || got.Revision != 1 || got.Kind != TaskKindAgent {
		t.Fatalf("definition changed by rejected statements: %+v %v", got, err)
	}
}
