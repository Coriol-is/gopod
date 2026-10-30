package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestProactivePresetRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	if _, ok, err := st.GetProactivePreset(ctx, "gmail.morning"); err != nil || ok {
		t.Fatalf("empty store: ok=%v err=%v", ok, err)
	}
	want := ProactivePreset{PresetID: "gmail.morning", TaskID: "preset-gmail.morning", Settings: `{"freq":"daily"}`, TemplateVersion: 2, AppliedHash: "abc", UpdatedAt: 5}
	if err := st.PutProactivePreset(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.GetProactivePreset(ctx, "gmail.morning")
	if err != nil || !ok || got != want {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
	want.TemplateVersion = 3
	if err = st.PutProactivePreset(ctx, want); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListProactivePresets(ctx)
	if err != nil || len(list) != 1 || list[0].TemplateVersion != 3 {
		t.Fatalf("upsert must replace: %+v err=%v", list, err)
	}
}

func TestProactiveSettings(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if _, ok, err := st.GetProactiveSetting(ctx, "tz"); err != nil || ok {
		t.Fatalf("unset: ok=%v err=%v", ok, err)
	}
	if err := st.SetProactiveSetting(ctx, "tz", "Asia/Tokyo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProactiveSetting(ctx, "tz", "UTC"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := st.GetProactiveSetting(ctx, "tz"); err != nil || !ok || v != "UTC" {
		t.Fatalf("got %q ok=%v err=%v", v, ok, err)
	}
	if err := st.DeleteProactiveSetting(ctx, "tz"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.GetProactiveSetting(ctx, "tz"); ok {
		t.Fatal("delete did not remove the key")
	}
}

func TestInTaskTxIsAtomicAcrossTaskAndPresetRows(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	boom := errors.New("boom")
	err := st.InTaskTx(ctx, func(ctx context.Context) error {
		if _, err := st.CreateTask(ctx, TaskRecord{ID: "preset-x", ChatFolder: "owner", ChatJID: "tg:1", Prompt: "p", ScheduleType: "cron", ScheduleValue: "0 8 * * *"}); err != nil {
			return err
		}
		if err := st.PutProactivePreset(ctx, ProactivePreset{PresetID: "x", TaskID: "preset-x"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err = st.GetTask(ctx, "preset-x"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("task survived rollback: %v", err)
	}
	if _, ok, _ := st.GetProactivePreset(ctx, "x"); ok {
		t.Fatal("preset row survived rollback")
	}

	err = st.InTaskTx(ctx, func(ctx context.Context) error {
		return st.InTaskTx(ctx, func(ctx context.Context) error { // nested joins the outer tx
			_, err := st.CreateTask(ctx, TaskRecord{ID: "preset-y", ChatFolder: "owner", ChatJID: "tg:1", Prompt: "p", ScheduleType: "cron", ScheduleValue: "0 8 * * *"})
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.GetTask(ctx, "preset-y"); err != nil {
		t.Fatalf("committed task missing: %v", err)
	}
}

func TestProactiveSchemaIsAdditiveOnLegacyDB(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db := openPreTaskSchema(t, path)
	if _, err := db.ExecContext(ctx, `INSERT INTO scheduled_tasks(id,chat_folder,chat_jid,prompt,schedule_type,schedule_value,next_run,status,created_at) VALUES('legacy-1','owner','tg:1','p','cron','0 9 * * *',7,'active',5)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // second Open proves restart-idempotence
		st, err := Open(ctx, path, silentLogger())
		if err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
		got, err := st.GetTask(ctx, "legacy-1")
		if err != nil || got.Prompt != "p" || got.NextRun != 7 {
			t.Fatalf("legacy row changed: %+v %v", got, err)
		}
		if n := countRows(t, st, `SELECT count(*) FROM proactive_presets`) + countRows(t, st, `SELECT count(*) FROM proactive_settings`); n != 0 {
			t.Fatalf("migration fabricated %d rows", n)
		}
		// scheduled_tasks (trigger-guarded) must not gain any feature column.
		cols, err := st.db.QueryContext(ctx, `SELECT name FROM pragma_table_info('scheduled_tasks')`)
		if err != nil {
			t.Fatal(err)
		}
		for cols.Next() {
			var name string
			if err = cols.Scan(&name); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(name, "preset") || strings.Contains(name, "proactive") {
				t.Fatalf("scheduled_tasks gained column %q", name)
			}
		}
		cols.Close()
		_ = st.Close()
	}
}
