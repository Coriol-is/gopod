package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Coriol-is/gopod/internal/artifactfs"
	"github.com/Coriol-is/gopod/internal/store"
)

type artFixture struct {
	*durableFixture
	chats    string
	blobs    *artifactfs.Blobs
	blobPath string
	caps     *Capabilities
	token    string
	fault    func(point string) error
	faultM   sync.Mutex
}

func resolved(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newArtFixture(t *testing.T) *artFixture {
	t.Helper()
	d := newDurableFixture(t)
	d.root = resolved(t)
	chats := filepath.Join(resolved(t), "chats")
	for _, folder := range []string{"owner", "group-a"} {
		if err := os.MkdirAll(filepath.Join(chats, folder), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	blobPath := filepath.Join(resolved(t), "blobs")
	blobs, err := artifactfs.OpenBlobs(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { blobs.Close() })
	f := &artFixture{durableFixture: d, chats: chats, blobs: blobs, blobPath: blobPath,
		caps: NewCapabilities(OutputRootPolicy{DataDir: d.root, ChatsDir: chats}, nil, staticResolver(d.jids))}
	f.token = f.issue("owner", "tg:1", store.OwnerLegacyAgentIPC, "run-1")
	return f
}

func (f *artFixture) issue(folder, jid string, owner store.DeliveryOwner, run string) string {
	f.t.Helper()
	conv, err := f.st.SnapshotConversation(f.t.Context(), jid, 0, store.VisibilityConversation)
	if err != nil {
		f.t.Fatal(err)
	}
	tok, err := f.caps.Issue(Grant{Folder: folder, RunRef: run, Actor: folder, Destination: conv, Owner: owner,
		OutputRoot: filepath.Join(f.chats, folder), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		f.t.Fatal(err)
	}
	return tok
}

func (f *artFixture) cfg() ArtifactConfig {
	return ArtifactConfig{Store: f.st, Blobs: f.blobs, Caps: f.caps, MinFreeBytes: 1,
		Fault: func(point string) error {
			f.faultM.Lock()
			fn := f.fault
			f.faultM.Unlock()
			if fn == nil {
				return nil
			}
			return fn(point)
		}}
}

func (f *artFixture) watcher() *Watcher {
	return New(Config{DataDir: f.root, OwnerFolder: "owner", NowFn: f.clk.Now,
		Durable:   DurableConfig{Store: f.store, PersonalJID: f.jid},
		Artifacts: f.cfg()},
		f.sink, f.tasks, slog.New(slog.NewTextHandler(io.Discard, nil)), staticResolver(f.jids))
}

func (f *artFixture) setFault(point string, err error, once bool) {
	var mu sync.Mutex
	fired := false
	fn := func(p string) error {
		mu.Lock()
		defer mu.Unlock()
		if p != point || (once && fired) {
			return nil
		}
		fired = true
		return err
	}
	f.faultM.Lock()
	f.fault = fn
	f.faultM.Unlock()
	f.blobs.Fault = func(p string) error { return fn(p) }
}

func (f *artFixture) clearFault() {
	f.faultM.Lock()
	f.fault = nil
	f.faultM.Unlock()
	f.blobs.Fault = nil
}

func (f *artFixture) write(rel, content string) string {
	f.t.Helper()
	p := filepath.Join(f.chats, "owner", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *artFixture) req(files ...string) map[string]any {
	var fs []map[string]any
	for _, p := range files {
		fs = append(fs, map[string]any{"path": ContainerOutputRoot + "/" + p, "filename": filepath.Base(p), "mime": "text/csv"})
	}
	return map[string]any{"version": 1, "requestId": "req-00000001", "capability": f.token, "text": "report attached", "files": fs}
}

func (f *artFixture) sendV1(folder, name string, body map[string]any) {
	f.t.Helper()
	writeIPC(f.t, f.root, folder, "messages", name, body)
}

func (f *artFixture) rows(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.st.DB().QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *artFixture) prepDirs() []string {
	f.t.Helper()
	l, err := f.blobs.ListPreparations()
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, d := range l {
		out = append(out, d.Name)
	}
	return out
}

// assertNoLeak requires that a rejected or aborted request left no reservation,
// no live artifact and no blob.
func (f *artFixture) assertNoLeak(label string) {
	f.t.Helper()
	cfg := f.cfg()
	if _, err := cfg.finishDeleting(f.t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		f.t.Fatalf("%s: finish deleting: %v", label, err)
	}
	if n := f.rows(`SELECT count(*) FROM artifact_reservations WHERE state='active'`); n != 0 {
		f.t.Errorf("%s: %d active reservations", label, n)
	}
	if n := f.rows(`SELECT count(*) FROM artifacts`); n != 0 {
		f.t.Errorf("%s: %d artifacts remain", label, n)
	}
	if d := f.prepDirs(); len(d) != 0 {
		f.t.Errorf("%s: blob dirs remain %v", label, d)
	}
	if n := f.deliveries(); n != 0 {
		f.t.Errorf("%s: %d deliveries", label, n)
	}
}

func (f *artFixture) wantError(name, code string) {
	f.t.Helper()
	rc := f.receipt("owner", strings.TrimSuffix(name, ".json")+".result.json")
	if rc["status"] != "error" || rc["error"] != code {
		f.t.Fatalf("%s receipt = %v, want error %s", name, rc, code)
	}
}

func TestArtifacts_HappyPathSnapshotsAndAttaches(t *testing.T) {
	f := newArtFixture(t)
	src := f.write("out/report.csv", "a,b\n1,2\n")
	f.sendV1("owner", "0001-r.json", f.req("out/report.csv"))
	f.tick(f.watcher())

	if calls := f.sink.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("direct send: %+v", calls)
	}
	rc := f.receipt("owner", "0001-r.result.json")
	if rc["status"] != "queued" {
		t.Fatalf("receipt %v", rc)
	}
	var kinds string
	if err := f.st.DB().QueryRow(`SELECT group_concat(kind) FROM (SELECT kind FROM outbox_parts ORDER BY ordinal)`).Scan(&kinds); err != nil || kinds != "text,upload" {
		t.Fatalf("plan %q %v", kinds, err)
	}
	if f.rows(`SELECT count(*) FROM artifact_refs`) != 1 || f.rows(`SELECT count(*) FROM artifacts WHERE state='ready'`) != 1 {
		t.Fatal("artifact not ready and referenced")
	}
	var key, sha string
	var size int64
	var fn, mt string
	if err := f.st.DB().QueryRow(`SELECT relative_blob_key,sha256,size,sanitized_filename,media_type FROM artifacts`).Scan(&key, &sha, &size, &fn, &mt); err != nil {
		t.Fatal(err)
	}
	if fn != "report.csv" || mt != "text/csv" {
		t.Fatalf("filename %q mime %q", fn, mt)
	}
	// The source is removed after the receipt: the snapshot is what gets sent.
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	data, err := f.blobs.ReadVerified(key, size, sha, 1<<20)
	if err != nil || string(data) != "a,b\n1,2\n" {
		t.Fatalf("snapshot after source removal: %q %v", data, err)
	}
	if got := dirEntries(t, f.dir("owner", "messages")); len(got) != 0 {
		t.Fatalf("messages = %v", got)
	}
	if n := f.rows(`SELECT count(*) FROM artifact_reservations WHERE state='active'`); n != 0 {
		t.Fatalf("reservation not released: %d", n)
	}
}

func TestArtifacts_TextOnlyV1UsesNoArtifacts(t *testing.T) {
	f := newArtFixture(t)
	body := f.req()
	body["files"] = nil
	f.sendV1("owner", "0001-t.json", body)
	f.tick(f.watcher())
	if rc := f.receipt("owner", "0001-t.result.json"); rc["status"] != "queued" {
		t.Fatalf("receipt %v", rc)
	}
	if f.rows(`SELECT count(*) FROM artifacts`) != 0 || f.deliveries() != 1 {
		t.Fatal("text-only V1 must publish without artifacts")
	}
}

func TestArtifacts_DisabledKeepsLegacyPath(t *testing.T) {
	f := newArtFixture(t)
	f.write("r.csv", "x")
	f.sendV1("owner", "0001-d.json", f.req("r.csv"))
	w := New(Config{DataDir: f.root, OwnerFolder: "owner", NowFn: f.clk.Now, Durable: DurableConfig{Store: f.store, PersonalJID: f.jid}},
		f.sink, f.tasks, slog.New(slog.NewTextHandler(io.Discard, nil)), staticResolver(f.jids))
	f.tick(w)
	if f.rows(`SELECT count(*) FROM artifacts`) != 0 || f.rows(`SELECT count(*) FROM artifact_reservations`) != 0 {
		t.Fatal("artifact tables touched while disabled")
	}
}

func TestArtifacts_StrictSchema(t *testing.T) {
	f := newArtFixture(t)
	f.write("r.csv", "x")
	mut := func(fn func(m map[string]any)) map[string]any { m := f.req("r.csv"); fn(m); return m }
	file := func(m map[string]any) map[string]any { return m["files"].([]map[string]any)[0] }
	cases := []struct {
		name string
		body map[string]any
		code string
	}{
		{"unknown field", mut(func(m map[string]any) { m["owner"] = true }), "invalid_request"},
		{"unknown file field", mut(func(m map[string]any) { file(m)["thread"] = 5 }), "invalid_request"},
		{"bad version", mut(func(m map[string]any) { m["version"] = 2 }), "invalid_request"},
		{"no request id", mut(func(m map[string]any) { delete(m, "requestId") }), "invalid_request"},
		{"empty", mut(func(m map[string]any) { m["text"] = ""; m["files"] = nil }), "invalid_request"},
		{"voice without text", mut(func(m map[string]any) { m["voice"] = true; m["text"] = ""; m["files"] = nil }), "invalid_request"},
		{"voice", mut(func(m map[string]any) { m["voice"] = true }), "voice_unavailable"},
		{"five files", mut(func(m map[string]any) {
			var fs []map[string]any
			for i := 0; i < 5; i++ {
				fs = append(fs, map[string]any{"path": ContainerOutputRoot + "/r.csv"})
			}
			m["files"] = fs
		}), "invalid_request"},
		{"bad mime", mut(func(m map[string]any) { file(m)["mime"] = "not a mime" }), "invalid_request"},
		{"bad presentation", mut(func(m map[string]any) { file(m)["presentation"] = "video" }), "invalid_request"},
		{"long caption", mut(func(m map[string]any) { file(m)["caption"] = strings.Repeat("x", 1025) }), "invalid_request"},
		{"huge text", mut(func(m map[string]any) { m["text"] = strings.Repeat("x", 49<<10) }), "invalid_request"},
	}
	w := f.watcher()
	for i, c := range cases {
		name := fmt.Sprintf("%04d-s.json", i)
		f.sendV1("owner", name, c.body)
		f.tick(w)
		f.wantError(name, c.code)
	}
	f.assertNoLeak("schema")

	// Trailing data after the JSON value.
	dir := f.dir("owner", "messages")
	raw, _ := json.Marshal(f.req("r.csv"))
	if err := os.WriteFile(filepath.Join(dir, "9999-trail.json"), append(raw, []byte(` {"x":1}`)...), 0o644); err != nil {
		t.Fatal(err)
	}
	f.tick(w)
	if rc := f.receipt("owner", "9999-trail.result.json"); rc["status"] != "error" {
		t.Fatalf("trailing data accepted: %v", rc)
	}
}

func TestArtifacts_PathAttacksRejectedWithoutLeaks(t *testing.T) {
	f := newArtFixture(t)
	root := filepath.Join(f.chats, "owner")
	f.write("ok.txt", "fine")
	f.write("sub/inner.txt", "inner")
	outside := filepath.Join(filepath.Dir(f.chats), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Symlink(outside, filepath.Join(root, "final-link.txt")))
	must(os.Symlink(filepath.Dir(f.chats), filepath.Join(root, "up")))
	must(os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "inside")))
	must(os.Link(filepath.Join(root, "ok.txt"), filepath.Join(root, "hard.txt")))
	must(unix.Mkfifo(filepath.Join(root, "fifo"), 0o644))
	f.write(".secret/token.txt", "x")
	f.write("memory/MEMORY.md", "x")
	f.write("state.sqlite", "x")
	f.write("state.sqlite-wal", "x")
	f.write(".env", "x")

	cases := []struct{ agentPath, code string }{
		{ContainerOutputRoot + "/final-link.txt", "path_denied"},
		{ContainerOutputRoot + "/up/outside.txt", "path_denied"},
		{ContainerOutputRoot + "/inside/inner.txt", "path_denied"},
		{ContainerOutputRoot + "/hard.txt", "hardlink_denied"},
		{ContainerOutputRoot + "/fifo", "not_regular_file"},
		{ContainerOutputRoot + "/../outside.txt", "path_denied"},
		{ContainerOutputRoot + "/sub/../ok.txt", "path_denied"},
		{"/etc/passwd", "path_denied"},
		{outside, "path_denied"},
		{"/workspace/ipc/messages/x.json", "path_denied"},
		{"relative.txt", "path_denied"},
		{ContainerOutputRoot + "/.secret/token.txt", "path_denied"},
		{ContainerOutputRoot + "/memory/MEMORY.md", "path_denied"},
		{ContainerOutputRoot + "/state.sqlite", "path_denied"},
		{ContainerOutputRoot + "/state.sqlite-wal", "path_denied"},
		{ContainerOutputRoot + "/.env", "path_denied"},
		{ContainerOutputRoot + "/missing.txt", "source_missing"},
		{ContainerOutputRoot + "/sub", "not_regular_file"},
	}
	w := f.watcher()
	for i, c := range cases {
		name := fmt.Sprintf("%04d-p.json", i)
		body := f.req()
		body["files"] = []map[string]any{{"path": c.agentPath}}
		f.sendV1("owner", name, body)
		f.tick(w)
		f.wantError(name, c.code)
	}
	f.assertNoLeak("path attacks")
	// the ordinary file still works
	f.sendV1("owner", "9000-ok.json", f.req("sub/inner.txt"))
	f.tick(w)
	if rc := f.receipt("owner", "9000-ok.result.json"); rc["status"] != "queued" {
		t.Fatalf("ordinary file after attacks: %v", rc)
	}
}

func TestArtifacts_OversizeAndEmptyFiles(t *testing.T) {
	f := newArtFixture(t)
	if err := f.st.SetArtifactLimits(store.ArtifactLimits{MaxFileBytes: 100, MaxRequestBytes: 150}); err != nil {
		t.Fatal(err)
	}
	f.write("big.bin", strings.Repeat("x", 101))
	f.write("empty.bin", "")
	f.write("a.bin", strings.Repeat("a", 90))
	f.write("b.bin", strings.Repeat("b", 90))
	w := f.watcher()
	f.sendV1("owner", "0001-big.json", f.req("big.bin"))
	f.sendV1("owner", "0002-empty.json", f.req("empty.bin"))
	f.sendV1("owner", "0003-agg.json", f.req("a.bin", "b.bin")) // 180 > 150 aggregate
	f.tick(w)
	f.tick(w) // the per-tick preparation budget is 2
	f.wantError("0001-big.json", "file_too_large")
	f.wantError("0002-empty.json", "empty_file")
	f.wantError("0003-agg.json", "file_too_large")
	f.assertNoLeak("oversize")
}

func TestArtifacts_QuotaExhaustionRetriesThenRejectsWithoutLeak(t *testing.T) {
	f := newArtFixture(t)
	if err := f.st.SetArtifactLimits(store.ArtifactLimits{MaxFileBytes: 100, MaxRequestBytes: 100, MaxNamespaceBytes: 150, MaxGlobalBytes: 150}); err != nil {
		t.Fatal(err)
	}
	// Another run holds 100 reserved bytes in this namespace.
	src, _ := f.st.SnapshotScope(t.Context(), "owner")
	if _, err := f.st.ReserveArtifacts(t.Context(), store.ArtifactReservation{Scope: src, OwnerRef: "other", Files: 1, Bytes: 100}); err != nil {
		t.Fatal(err)
	}
	f.write("a.bin", "data")
	w := f.watcher()
	f.sendV1("owner", "0001-q.json", f.req("a.bin"))
	f.tick(w)
	if rc := f.receipt("owner", "0001-q.result.json"); rc["status"] != "retrying" || rc["error"] != "quota_exceeded" {
		t.Fatalf("first pass: %v", rc)
	}
	for i := 0; i < 3; i++ {
		f.clk.advance(time.Minute)
		f.tick(w)
	}
	f.wantError("0001-q.json", "quota_exceeded")
	if f.deliveries() != 0 || f.rows(`SELECT count(*) FROM artifacts`) != 1 {
		t.Fatal("quota rejection leaked rows (only the other run's artifact may exist)")
	}
}

func TestArtifacts_DiskFull(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.bin", "data")
	w := f.watcher()

	f.blobs.SetFreeBytesForTest(func() (uint64, error) { return 10, nil })
	f.sendV1("owner", "0001-d.json", f.req("a.bin"))
	for i := 0; i < 4; i++ {
		f.tick(w)
		f.clk.advance(time.Minute)
	}
	f.wantError("0001-d.json", "storage_full")
	f.assertNoLeak("floor")

	// ENOSPC while copying: the partial snapshot and reservation are cleaned up.
	f.blobs.SetFreeBytesForTest(func() (uint64, error) { return 1 << 40, nil })
	f.setFault("copy_mid", artifactfs.ErrStorageFull, false)
	f.sendV1("owner", "0002-d.json", f.req("a.bin"))
	for i := 0; i < 4; i++ {
		f.tick(w)
		f.clk.advance(time.Minute)
	}
	f.wantError("0002-d.json", "storage_full")
	f.assertNoLeak("enospc")
}

func TestArtifacts_SourceChangeDuringCopyFailsRequest(t *testing.T) {
	f := newArtFixture(t)
	p := f.write("grow.bin", strings.Repeat("x", 200000))
	f.blobs.Fault = func(point string) error {
		if point == "copy_mid" {
			fh, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
			fh.WriteString("late write")
			fh.Close()
		}
		return nil
	}
	f.sendV1("owner", "0001-g.json", f.req("grow.bin"))
	f.tick(f.watcher())
	f.wantError("0001-g.json", "source_changed")
	f.assertNoLeak("source race")
}

func TestArtifacts_CrashAtEveryBoundaryConverges(t *testing.T) {
	points := []string{"after_reserve", "after_prep_mkdir", "copy_mid", "copy_end", "after_file_fsync", "after_rename",
		"after_dir_fsync", "before_mark_ready", "after_mark_ready", "before_publish", "after_publish"}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			f := newArtFixture(t)
			f.write("a.csv", "1,2,3")
			f.sendV1("owner", "0001-c.json", f.req("a.csv"))

			f.setFault(point, artifactfs.ErrSimulatedCrash, true)
			f.tick(f.watcher()) // "process dies" at the injected point
			f.clearFault()
			if point != "after_publish" {
				if f.deliveries() != 0 {
					t.Fatalf("crash at %s published a delivery", point)
				}
			} else if f.deliveries() != 1 {
				t.Fatal("after_publish: the commit must be durable")
			}
			// The request file is still there; no receipt claims success.
			if point != "after_publish" {
				if got := dirEntries(t, f.dir("owner", "messages")); len(got) != 1 {
					t.Fatalf("request lost at %s: %v", point, got)
				}
			}

			// Restart: a fresh watcher converges on exactly one delivery.
			f.tick(f.watcher())
			if rc := f.receipt("owner", "0001-c.result.json"); rc["status"] != "queued" {
				t.Fatalf("after restart: %v", rc)
			}
			if f.deliveries() != 1 || f.rows(`SELECT count(*) FROM artifact_refs`) != 1 {
				t.Fatalf("deliveries=%d refs=%d", f.deliveries(), f.rows(`SELECT count(*) FROM artifact_refs`))
			}

			// Reconcile after the preparation deadline removes whatever the
			// crash left, and nothing the live delivery needs.
			cfg := f.cfg()
			rep, err := cfg.Reconcile(t.Context(), time.Now().Add(30*time.Minute), slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			if rep.Quarantined != 0 {
				t.Fatalf("reconcile quarantined a live artifact: %+v", rep)
			}
			if n := f.rows(`SELECT count(*) FROM artifact_reservations WHERE state='active'`); n != 0 {
				t.Fatalf("%d reservations still active", n)
			}
			if n := f.rows(`SELECT count(*) FROM artifacts`); n != 1 {
				t.Fatalf("%d artifacts, want exactly the published one", n)
			}
			if d := f.prepDirs(); len(d) != 1 {
				t.Fatalf("blob dirs %v, want exactly the published one", d)
			}
			var key, sha string
			var size int64
			if err = f.st.DB().QueryRow(`SELECT relative_blob_key,sha256,size FROM artifacts WHERE state='ready'`).Scan(&key, &sha, &size); err != nil {
				t.Fatal(err)
			}
			if data, err := f.blobs.ReadVerified(key, size, sha, 1<<20); err != nil || string(data) != "1,2,3" {
				t.Fatalf("published snapshot: %q %v", data, err)
			}
		})
	}
}

func TestArtifacts_AcceptedRequestSurvivesRunEnd(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	f.sendV1("owner", "0001-e.json", f.req("a.csv"))
	f.setFault("after_publish", artifactfs.ErrSimulatedCrash, true)
	f.tick(f.watcher())
	f.clearFault()
	if f.caps.EndRun("run-1") != 1 {
		t.Fatal("capability was not active")
	}
	// The committed request converges on its delivery even though the run ended.
	f.tick(f.watcher())
	if rc := f.receipt("owner", "0001-e.result.json"); rc["status"] != "queued" {
		t.Fatalf("accepted request must not be revoked: %v", rc)
	}
	// An unaccepted request written after the run ended fails closed.
	time.Sleep(30 * time.Millisecond) // coarse filesystem timestamps
	f.sendV1("owner", "0002-late.json", f.req("a.csv"))
	f.tick(f.watcher())
	f.wantError("0002-late.json", "stale_capability")
	if f.deliveries() != 1 {
		t.Fatalf("deliveries %d", f.deliveries())
	}
}

func TestArtifacts_StaleAndForeignCapabilities(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	w := f.watcher()

	body := f.req("a.csv")
	body["capability"] = "0123456789abcdef"
	f.sendV1("owner", "0001-bad.json", body)

	// A token issued to another folder cannot be used from the owner folder.
	groupTok := f.issue("group-a", "tg:-2", store.OwnerLegacyAgentIPC, "run-g")
	body = f.req("a.csv")
	body["capability"] = groupTok
	f.sendV1("owner", "0002-foreign.json", body)

	// Registration change after issue: the capability's ScopeRef is stale.
	f.f2Rename()
	f.sendV1("owner", "0003-scope.json", f.req("a.csv"))
	f.tick(w)
	f.wantError("0001-bad.json", "stale_capability")
	f.wantError("0002-foreign.json", "stale_capability")
	f.wantError("0003-scope.json", "recipient_unavailable")
	f.assertNoLeak("stale")
}

func (f *artFixture) f2Rename() {
	f.t.Helper()
	if err := f.st.RegisterChat(f.t.Context(), store.RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "renamed"}); err != nil {
		f.t.Fatal(err)
	}
}

func TestArtifacts_GroupAndOwnerAuthorityDenied(t *testing.T) {
	f := newArtFixture(t)
	os.WriteFile(filepath.Join(f.chats, "group-a", "g.txt"), []byte("g"), 0o644)
	w := f.watcher()

	// Only the owner folder reaches the artifact route: a group folder's V1
	// file keeps the legacy text path (which refuses it: no chatJid).
	gTok := f.issue("group-a", "tg:-2", store.OwnerLegacyAgentIPC, "run-g")
	writeIPC(t, f.root, "group-a", "messages", "0001-g.json", map[string]any{"version": 1, "requestId": "req-00000002", "capability": gTok,
		"files": []map[string]any{{"path": ContainerOutputRoot + "/g.txt"}}})
	// Owner capability aimed at a group chat.
	body := f.req()
	body["files"] = nil
	body["chatJid"] = "tg:-2"
	f.sendV1("owner", "0002-x.json", body)
	// Controller-owned delivery cannot take a second publication through IPC.
	ctrlTok := f.issue("owner", "tg:1", store.OwnerControllerResult, "run-c")
	body = f.req()
	body["capability"] = ctrlTok
	body["files"] = nil
	f.sendV1("owner", "0003-c.json", body)
	f.tick(w)

	for _, c := range []struct{ folder, name string }{{"owner", "0002-x"}, {"owner", "0003-c"}} {
		rc := f.receipt(c.folder, c.name+".result.json")
		if rc["status"] != "error" || rc["error"] != "recipient_unavailable" {
			t.Fatalf("%s: %v", c.name, rc)
		}
	}
	if rc := f.receipt("group-a", "0001-g.result.json"); rc["status"] != "error" {
		t.Fatalf("group V1 request: %v", rc)
	}
	f.assertNoLeak("authority")
	if f.rows(`SELECT count(*) FROM artifact_reservations`) != 0 {
		t.Fatal("a group folder reached the artifact route")
	}
	if calls := f.sink.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("direct send %+v", calls)
	}
}

func TestArtifacts_DuplicateAndConflictingRequests(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	w := f.watcher()
	f.sendV1("owner", "0001-d.json", f.req("a.csv"))
	f.tick(w)
	// Same filename, same bytes: the same delivery, nothing re-prepared.
	f.sendV1("owner", "0001-d.json", f.req("a.csv"))
	f.tick(w)
	if rc := f.receipt("owner", "0001-d.result.json"); rc["status"] != "queued" || rc["duplicate"] != true {
		t.Fatalf("duplicate receipt %v", rc)
	}
	if f.deliveries() != 1 || f.rows(`SELECT count(*) FROM artifacts`) != 1 || len(f.prepDirs()) != 1 {
		t.Fatal("duplicate re-prepared or republished")
	}
	// Same filename, different bytes: conflict, original untouched.
	body := f.req("a.csv")
	body["text"] = "different"
	f.sendV1("owner", "0001-d.json", body)
	f.tick(w)
	if rc := f.receipt("owner", "0001-d.conflict.result.json"); rc["error"] != "request_conflict" {
		t.Fatalf("conflict receipt %v", rc)
	}
	if f.deliveries() != 1 {
		t.Fatal("conflict published")
	}
}

func TestArtifacts_ConcurrentWatchersConverge(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "payload")
	f.sendV1("owner", "0001-race.json", f.req("a.csv"))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		w := f.watcher()
		go func() {
			defer wg.Done()
			w.tickOnce(context.Background())
		}()
	}
	wg.Wait()
	if f.deliveries() != 1 {
		t.Fatalf("deliveries %d", f.deliveries())
	}
	cfg := f.cfg()
	if _, err := cfg.finishDeleting(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if f.rows(`SELECT count(*) FROM artifacts`) != 1 || len(f.prepDirs()) != 1 || f.rows(`SELECT count(*) FROM artifact_reservations WHERE state='active'`) != 0 {
		t.Fatalf("artifacts=%d dirs=%v", f.rows(`SELECT count(*) FROM artifacts`), f.prepDirs())
	}
}

func TestArtifacts_ReconcileQuarantinesMissingBlobAndFailsPendingPart(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	f.sendV1("owner", "0001-m.json", f.req("a.csv"))
	w := f.watcher()
	f.tick(w)
	var key string
	if err := f.st.DB().QueryRow(`SELECT relative_blob_key FROM artifacts`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.blobPath, key)); err != nil {
		t.Fatal(err)
	}
	cfg := f.cfg()
	rep, err := cfg.Reconcile(t.Context(), time.Now(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || rep.Quarantined != 1 {
		t.Fatalf("reconcile %+v %v", rep, err)
	}
	var upload, text string
	f.st.DB().QueryRow(`SELECT state||':'||reason_code FROM outbox_parts WHERE kind='upload'`).Scan(&upload)
	f.st.DB().QueryRow(`SELECT state FROM outbox_parts WHERE kind='text'`).Scan(&text)
	if upload != "failed:artifact_quarantined" || text != "pending" {
		t.Fatalf("parts upload=%s text=%s", upload, text)
	}
	f.tick(w) // receipt follows durable state
	var st string
	f.st.DB().QueryRow(`SELECT state FROM delivery_groups`).Scan(&st)
	if rc := f.receipt("owner", "0001-m.result.json"); rc["deliveryState"] != st {
		t.Fatalf("receipt %v state %s", rc, st)
	}
}

func TestArtifacts_UncertainReferencePinsArtifactFromGC(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	f.sendV1("owner", "0001-u.json", f.req("a.csv"))
	f.tick(f.watcher())
	if _, err := f.st.DB().Exec(`UPDATE outbox_parts SET state='uncertain' WHERE kind='upload'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`UPDATE outbox_parts SET state='sent' WHERE kind='text'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB().Exec(`UPDATE delivery_groups SET state='uncertain', unresolved=1`); err != nil {
		t.Fatal(err)
	}
	cfg := f.cfg()
	cfg.Retention = time.Hour
	rep, err := cfg.Reconcile(t.Context(), time.Now().Add(5*365*24*time.Hour), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if rep.GCMarked != 0 || f.rows(`SELECT count(*) FROM artifacts WHERE state='ready'`) != 1 || len(f.prepDirs()) != 1 {
		t.Fatalf("uncertain artifact was collected: %+v", rep)
	}
	// Once the delivery is terminal and old, it is collected and its blob removed.
	if _, err = f.st.DB().Exec(`UPDATE outbox_parts SET state='sent'; UPDATE delivery_groups SET state='sent', unresolved=0`); err != nil {
		t.Fatal(err)
	}
	rep, err = cfg.Reconcile(t.Context(), time.Now().Add(5*365*24*time.Hour), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || rep.GCMarked != 1 || rep.DeletedBlobs != 1 {
		t.Fatalf("terminal artifact: %+v %v", rep, err)
	}
	if f.rows(`SELECT count(*) FROM artifacts`) != 0 || len(f.prepDirs()) != 0 {
		t.Fatal("terminal artifact not removed")
	}
}

func TestArtifacts_ReconcileRemovesOnlyUnknownOldDirectories(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	f.sendV1("owner", "0001-o.json", f.req("a.csv"))
	f.tick(f.watcher())
	known := f.prepDirs()[0]

	oldUnknown := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	freshUnknown := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	for _, n := range []string{oldUnknown, freshUnknown} {
		if err := f.blobs.NewPreparation(n); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(f.blobPath, oldUnknown), old, old); err != nil {
		t.Fatal(err)
	}
	// A foreign, badly named entry is never touched.
	if err := os.Mkdir(filepath.Join(f.blobPath, "not-a-uuid"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := f.cfg()
	rep, err := cfg.Reconcile(t.Context(), time.Now(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || rep.OrphanDirs != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	have := map[string]bool{}
	for _, d := range f.prepDirs() {
		have[d] = true
	}
	if have[oldUnknown] || !have[freshUnknown] || !have[known] {
		t.Fatalf("dirs %v", have)
	}
	if _, err = os.Stat(filepath.Join(f.blobPath, "not-a-uuid")); err != nil {
		t.Fatalf("foreign entry removed: %v", err)
	}
}

func TestArtifacts_ReconcileIsIdempotentAndNoopWhenDisabled(t *testing.T) {
	f := newArtFixture(t)
	cfg := f.cfg()
	for i := 0; i < 2; i++ {
		rep, err := cfg.Reconcile(t.Context(), time.Now(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil || rep != (ReconcileReport{}) {
			t.Fatalf("%+v %v", rep, err)
		}
	}
	if rep, err := (ArtifactConfig{}).Reconcile(t.Context(), time.Now(), slog.Default()); err != nil || rep != (ReconcileReport{}) {
		t.Fatal("disabled reconcile did something")
	}
}

func TestArtifacts_LastActionFileThenEndRunBeforePollIsAdmitted(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	f.sendV1("owner", "0001-last.json", f.req("a.csv"))
	if f.caps.EndRun("run-1") != 1 { // the run ends before the watcher polls
		t.Fatal("capability was not active")
	}
	f.tick(f.watcher())
	if rc := f.receipt("owner", "0001-last.result.json"); rc["status"] != "queued" {
		t.Fatalf("file written while the run was active must be admitted: %v", rc)
	}
	// Past the grace window the same situation fails closed and is tombstoned.
	f2 := newArtFixture(t)
	f2.write("a.csv", "x")
	f2.caps.Grace = time.Millisecond
	f2.sendV1("owner", "0001-slow.json", f2.req("a.csv"))
	f2.caps.EndRun("run-1")
	time.Sleep(20 * time.Millisecond)
	f2.tick(f2.watcher())
	f2.wantError("0001-slow.json", "stale_capability")
}

func TestArtifacts_PolicyRevocationHasNoGrace(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	f.sendV1("owner", "0001-p.json", f.req("a.csv"))
	f.caps.RevokeFolder("owner")
	f.tick(f.watcher())
	f.wantError("0001-p.json", "stale_capability")
}

func TestArtifacts_FileFloodIsBudgetedPerTick(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	for i := 0; i < 5; i++ {
		f.sendV1("owner", fmt.Sprintf("000%d-b.json", i), f.req("a.csv"))
	}
	w := f.watcher()
	f.tick(w)
	if n := f.deliveries(); n != 2 {
		t.Fatalf("first tick published %d, want the budget of 2", n)
	}
	if got := dirEntries(t, f.dir("owner", "messages")); len(got) != 3 {
		t.Fatalf("unprocessed requests must stay queued untouched: %v", got)
	}
	f.tick(w)
	f.tick(w)
	if n := f.deliveries(); n != 5 {
		t.Fatalf("after three ticks %d deliveries", n)
	}
}

func TestArtifacts_RunRateCapRejectsWithCode(t *testing.T) {
	f := newArtFixture(t)
	if err := f.st.SetArtifactLimits(store.ArtifactLimits{MaxRunFiles: 1}); err != nil {
		t.Fatal(err)
	}
	f.write("a.csv", "x")
	f.sendV1("owner", "0001-r.json", f.req("a.csv"))
	f.sendV1("owner", "0002-r.json", f.req("a.csv"))
	f.tick(f.watcher())
	if rc := f.receipt("owner", "0001-r.result.json"); rc["status"] != "queued" {
		t.Fatalf("first: %v", rc)
	}
	f.wantError("0002-r.json", "rate_limited")
	if f.deliveries() != 1 {
		t.Fatalf("deliveries %d", f.deliveries())
	}
}

func TestArtifacts_MissingPreparationDirectoryIsRetriedNotTombstoned(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	f.sendV1("owner", "0001-h.json", f.req("a.csv"))
	f.setFault("after_prep_mkdir", nil, true)
	fired := false
	f.blobs.Fault = func(point string) error {
		if point == "after_prep_mkdir" && !fired {
			fired = true
			dirs, _ := f.blobs.ListPreparations()
			for _, d := range dirs {
				f.blobs.RemovePreparation(d.Name) // host-side directory vanishes
			}
		}
		return nil
	}
	w := f.watcher()
	f.tick(w)
	f.clearFault()
	if rc := f.receipt("owner", "0001-h.result.json"); rc["status"] != "retrying" || rc["error"] != "durable_unavailable" {
		t.Fatalf("host fault must be a retry: %v", rc)
	}
	if f.rows(`SELECT count(*) FROM ipc_message_requests WHERE outcome='rejected'`) != 0 {
		t.Fatal("host fault became a permanent tombstone")
	}
	f.clk.advance(time.Minute)
	f.tick(w)
	if rc := f.receipt("owner", "0001-h.result.json"); rc["status"] != "queued" {
		t.Fatalf("retry: %v", rc)
	}
}

func TestArtifacts_QuarantineSweepHeldForEmptyRootThenRestoredBlobUnquarantines(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "alpha")
	f.write("b.csv", "bravo")
	f.sendV1("owner", "0001-a.json", f.req("a.csv"))
	f.sendV1("owner", "0002-b.json", f.req("b.csv"))
	f.tick(f.watcher())
	cfg := f.cfg()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Root recreated empty: every ready row looks missing. Hold, do not quarantine.
	dirs, _ := f.blobs.ListPreparations()
	saved := map[string][]byte{}
	var keys []string
	rows, _ := f.st.DB().Query(`SELECT relative_blob_key FROM artifacts`)
	for rows.Next() {
		var k string
		rows.Scan(&k)
		keys = append(keys, k)
	}
	rows.Close()
	for _, k := range keys {
		b, err := os.ReadFile(filepath.Join(f.blobPath, k))
		if err != nil {
			t.Fatal(err)
		}
		saved[k] = b
	}
	for _, d := range dirs {
		if err := f.blobs.RemovePreparation(d.Name); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := cfg.Reconcile(t.Context(), time.Now(), log)
	if err != nil || rep.Quarantined != 0 || f.rows(`SELECT count(*) FROM artifacts WHERE state='ready'`) != 2 {
		t.Fatalf("empty root must hold the sweep: %+v %v", rep, err)
	}

	// One blob lost while the root is otherwise intact: that artifact is quarantined.
	restoreAll := func() {
		for k, b := range saved {
			prep, _, _ := strings.Cut(k, "/")
			os.MkdirAll(filepath.Join(f.blobPath, prep), 0o700)
			if err := os.WriteFile(filepath.Join(f.blobPath, k), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	restoreAll()
	if err = os.Remove(filepath.Join(f.blobPath, keys[0])); err != nil {
		t.Fatal(err)
	}
	rep, err = cfg.Reconcile(t.Context(), time.Now(), log)
	if err != nil || rep.Quarantined != 1 {
		t.Fatalf("one missing blob: %+v %v", rep, err)
	}
	// The blob comes back (restore from backup): it verifies and is un-quarantined.
	if err = os.WriteFile(filepath.Join(f.blobPath, keys[0]), saved[keys[0]], 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err = cfg.Reconcile(t.Context(), time.Now(), log)
	if err != nil || rep.Restored != 1 || f.rows(`SELECT count(*) FROM artifacts WHERE state='ready'`) != 2 {
		t.Fatalf("restore: %+v %v", rep, err)
	}
	// A wrong-content blob is never restored.
	if _, err = f.st.QuarantineArtifact(t.Context(), mustArtifactID(t, f, keys[0]), "blob_corrupt"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.blobPath, keys[0]), []byte("WRONG"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rep, _ = cfg.Reconcile(t.Context(), time.Now(), log); rep.Restored != 0 {
		t.Fatalf("restored a blob with the wrong content: %+v", rep)
	}
}

func mustArtifactID(t *testing.T, f *artFixture, key string) string {
	t.Helper()
	var id string
	if err := f.st.DB().QueryRow(`SELECT artifact_id FROM artifacts WHERE relative_blob_key=?`, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestArtifacts_HardlinkedBlobInPrivateStoreStillSends(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	f.sendV1("owner", "0001-h.json", f.req("a.csv"))
	f.tick(f.watcher())
	var key string
	f.st.DB().QueryRow(`SELECT relative_blob_key FROM artifacts`).Scan(&key)
	if err := os.Link(filepath.Join(f.blobPath, key), filepath.Join(f.blobPath, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	cfg := f.cfg()
	rep, err := cfg.Reconcile(t.Context(), time.Now(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || rep.Quarantined != 0 {
		t.Fatalf("hardlinked private blob quarantined: %+v %v", rep, err)
	}
}

func TestArtifacts_RetentionDeletesTerminalBlobsAfterHoursKeepsPending(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	f.sendV1("owner", "0001-t.json", f.req("a.csv"))
	f.tick(f.watcher())
	cfg := f.cfg()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Pending: pinned however old.
	if rep, _ := cfg.Reconcile(t.Context(), time.Now().Add(72*time.Hour), log); rep.GCMarked != 0 || len(f.prepDirs()) != 1 {
		t.Fatalf("pending reference collected: %+v", rep)
	}
	if _, err := f.st.DB().Exec(`UPDATE outbox_parts SET state='sent'; UPDATE delivery_groups SET state='sent', unresolved=0`); err != nil {
		t.Fatal(err)
	}
	if rep, _ := cfg.Reconcile(t.Context(), time.Now().Add(time.Hour), log); rep.GCMarked != 0 {
		t.Fatalf("collected one hour after terminal with the default retention: %+v", rep)
	}
	rep, err := cfg.Reconcile(t.Context(), time.Now().Add(store.DefaultArtifactRetention+time.Hour), log)
	if err != nil || rep.GCMarked != 1 || rep.DeletedBlobs != 1 || len(f.prepDirs()) != 0 {
		t.Fatalf("terminal blob not removed after the default retention: %+v %v", rep, err)
	}
}

func TestArtifacts_FinishDeletingPagesPastFailingRows(t *testing.T) {
	old := reconcileBatch
	reconcileBatch = 2
	defer func() { reconcileBatch = old }()
	f := newArtFixture(t)
	if err := f.st.SetArtifactLimits(store.ArtifactLimits{MaxRunFiles: 100, MaxHourFiles: 100}); err != nil {
		t.Fatal(err)
	}
	src, _ := f.st.SnapshotScope(t.Context(), "owner")
	var keys []string
	for i := 0; i < 5; i++ {
		p, err := f.st.ReserveArtifacts(t.Context(), store.ArtifactReservation{Scope: src, OwnerRef: fmt.Sprintf("r%d", i), Files: 1, Bytes: 10})
		if err != nil {
			t.Fatal(err)
		}
		if err = f.st.AbandonPreparation(t.Context(), p.ID, "x"); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, p.BlobKeys[0])
	}
	// Make the blob removal of the three lowest ids fail: a symlinked preparation directory.
	ids := map[string]string{}
	rows, _ := f.st.DB().Query(`SELECT artifact_id, relative_blob_key FROM artifacts ORDER BY artifact_id`)
	var ordered []string
	for rows.Next() {
		var id, k string
		rows.Scan(&id, &k)
		ids[id] = k
		ordered = append(ordered, id)
	}
	rows.Close()
	outside := resolved(t)
	for _, id := range ordered[:3] {
		prep, _, _ := strings.Cut(ids[id], "/")
		if err := os.Symlink(outside, filepath.Join(f.blobPath, prep)); err != nil {
			t.Fatal(err)
		}
	}
	cfg := f.cfg()
	n, err := cfg.finishDeleting(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil || n != 2 {
		t.Fatalf("deleted %d (%v), want the two healthy rows behind three failing ones", n, err)
	}
	_ = keys
}

// A request file's mtime is agent-controlled. A file that appears after the run
// ended must be refused even when its mtime is set into the past.
func TestArtifacts_BackdatedMtimeAfterEndRunIsRefused(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	w := f.watcher()
	f.tick(w) // an empty sweep: the host has now seen the channel without the file
	if f.caps.EndRun("run-1") != 1 {
		t.Fatal("capability was not active")
	}
	time.Sleep(20 * time.Millisecond)
	f.tick(w) // a later empty sweep, after the run ended
	time.Sleep(20 * time.Millisecond)
	f.sendV1("owner", "0001-late.json", f.req("a.csv"))
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(f.dir("owner", "messages"), "0001-late.json"), old, old); err != nil {
		t.Fatal(err)
	}
	f.tick(w)
	f.wantError("0001-late.json", "stale_capability")
	if f.deliveries() != 0 {
		t.Fatalf("deliveries %d, want 0", f.deliveries())
	}
}

// A request the watcher saw while the run was active (here deferred by the
// per-tick budget) is still admitted inside the grace window after the run ends.
func TestArtifacts_RequestSeenBeforeEndRunIsAdmittedInGrace(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	w := f.watcher()
	f.tick(w)
	time.Sleep(20 * time.Millisecond)
	for i := 0; i < 3; i++ {
		f.sendV1("owner", fmt.Sprintf("000%d-b.json", i), f.req("a.csv"))
	}
	f.tick(w) // two published, the third is observed and deferred
	if n := f.deliveries(); n != 2 {
		t.Fatalf("first tick published %d, want 2", n)
	}
	f.caps.EndRun("run-1")
	time.Sleep(20 * time.Millisecond)
	f.tick(w)
	if n := f.deliveries(); n != 3 {
		t.Fatalf("a request observed before the end was lost: %d deliveries", n)
	}
}
