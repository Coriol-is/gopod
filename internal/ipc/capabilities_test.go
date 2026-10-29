package ipc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

func testScope() store.ConversationScope {
	return store.ConversationScope{
		Scope:      store.ScopeRef{NamespaceID: "11111111-1111-4111-8111-111111111111", NamespaceEpoch: 1, RegistrationRevision: 1, JID: "tg:1", Folder: "owner"},
		Visibility: store.VisibilityConversation,
	}
}

func TestOutputRootPolicy(t *testing.T) {
	base := resolved(t)
	data := filepath.Join(base, "data")
	chats := filepath.Join(base, "chats")
	for _, d := range []string{data, chats, filepath.Join(chats, "owner"), filepath.Join(chats, ".ssh"),
		filepath.Join(data, "shared-namespaces", "n", "e2", "workspace"), filepath.Join(data, "ipc", "owner")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(chats, "owner"), filepath.Join(chats, "link")); err != nil {
		t.Fatal(err)
	}
	p := OutputRootPolicy{DataDir: data, ChatsDir: chats}
	if err := p.Validate(filepath.Join(chats, "owner")); err != nil {
		t.Fatalf("ordinary root: %v", err)
	}
	denied := map[string]string{
		"data dir":           data,
		"chats dir itself":   chats,
		"ipc namespace":      filepath.Join(data, "ipc", "owner"),
		"group workspace":    filepath.Join(data, "shared-namespaces", "n", "e2", "workspace"),
		"shared-namespaces":  filepath.Join(data, "shared-namespaces"),
		"hidden ssh":         filepath.Join(chats, ".ssh"),
		"symlink":            filepath.Join(chats, "link"),
		"missing":            filepath.Join(chats, "nope"),
		"outside chats":      base,
		"nested below chat":  filepath.Join(chats, "owner", "sub"),
		"relative":           "chats/owner",
		"unclean":            chats + "/owner/../owner",
		"external root":      "/etc",
		"parent of data dir": filepath.Dir(data),
	}
	for name, root := range denied {
		if err := p.Validate(root); !errors.Is(err, ErrOutputRootDenied) {
			t.Errorf("%s (%s): %v", name, root, err)
		}
	}
	// A data dir inside the chats tree must never be offered as an output root.
	nested := OutputRootPolicy{DataDir: filepath.Join(chats, "owner"), ChatsDir: chats}
	if err := nested.Validate(filepath.Join(chats, "owner")); !errors.Is(err, ErrOutputRootDenied) {
		t.Errorf("root containing the data dir: %v", err)
	}
}

func TestMapPath(t *testing.T) {
	g := Grant{OutputRoot: "/data/chats/owner"}
	ok := map[string]string{
		ContainerOutputRoot + "/a.csv":      "a.csv",
		ContainerOutputRoot + "/out/b.txt":  "out/b.txt",
		ContainerOutputRoot + "/dir/x y.md": "dir/x y.md",
	}
	for in, want := range ok {
		if got, err := g.MapPath(in); err != nil || got != want {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
	for _, in := range []string{
		"", "/", ContainerOutputRoot, ContainerOutputRoot + "/", "a.csv", "/etc/passwd", "/data/chats/owner/a.csv",
		ContainerOutputRoot + "/../a", ContainerOutputRoot + "/a/../b", ContainerOutputRoot + "//a", ContainerOutputRoot + "/./a",
		ContainerOutputRoot + "/.hidden", ContainerOutputRoot + "/d/.git/config", ContainerOutputRoot + "/memory/MEMORY.md",
		ContainerOutputRoot + "/x.sqlite", ContainerOutputRoot + "/x.DB", ContainerOutputRoot + "/store.sqlite-wal",
		ContainerOutputRoot + "/credentials.json", ContainerOutputRoot + "/id_rsa", ContainerOutputRoot + "/a\x00b",
		"/workspace/chatter/a", "/workspace/chat2/a",
	} {
		if got, err := g.MapPath(in); !errors.Is(err, ErrPathDenied) {
			t.Errorf("%q accepted as %q (%v)", in, got, err)
		}
	}
	// a custom container root is honoured, the default is not.
	g2 := Grant{OutputRoot: "/data/chats/owner", ContainerRoot: "/workspace/out"}
	if _, err := g2.MapPath("/workspace/out/a"); err != nil {
		t.Error(err)
	}
	if _, err := g2.MapPath(ContainerOutputRoot + "/a"); err == nil {
		t.Error("default root accepted with a custom root")
	}
}

func TestCapabilityLifecycle(t *testing.T) {
	base := resolved(t)
	chats := filepath.Join(base, "chats")
	if err := os.MkdirAll(filepath.Join(chats, "owner"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	c := NewCapabilities(OutputRootPolicy{DataDir: filepath.Join(base, "data"), ChatsDir: chats}, func() time.Time { return now }, staticResolver(map[string]string{"owner": "tg:1", "other": "tg:9"}))
	if err := os.MkdirAll(filepath.Join(chats, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := Grant{Folder: "owner", RunRef: "run-1", Actor: "owner", Destination: testScope(), Owner: store.OwnerLegacyAgentIPC,
		OutputRoot: filepath.Join(chats, "owner"), ExpiresAt: now.Add(time.Hour)}

	bad := []Grant{g, g, g, g, g, g, g, g, g, g}
	bad[0].Folder = ""
	bad[1].RunRef = ""
	bad[2].Actor = ""
	bad[3].ExpiresAt = now
	bad[4].Owner = "x"
	bad[5].Destination.Scope.NamespaceID = "x"
	bad[6].OutputRoot = "/etc"
	bad[7].OutputRoot = filepath.Join(chats, "other")     // another chat's directory
	bad[8].Destination.Scope.JID = "tg:9"                 // a chat that is not the actor's
	bad[9].ExpiresAt = now.Add(MaxGrantTTL + time.Second) // over the TTL cap
	for i, b := range bad {
		if _, err := c.Issue(b); err == nil {
			t.Errorf("invalid grant %d accepted", i)
		}
	}

	tok, err := c.Issue(g)
	if err != nil || len(tok) < 32 {
		t.Fatalf("issue: %q %v", tok, err)
	}
	if tok2, _ := c.Issue(g); tok2 == tok {
		t.Fatal("tokens must be unique")
	}
	got, err := c.Resolve(tok, "owner")
	if err != nil || got.RunRef != "run-1" || got.Destination.Scope.JID != "tg:1" {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	for _, c2 := range []struct{ tok, folder string }{{tok, "group-a"}, {"", "owner"}, {"nope", "owner"}, {strings.Repeat("a", 200), "owner"}} {
		if _, err = c.Resolve(c2.tok, c2.folder); !errors.Is(err, ErrStaleCapability) {
			t.Errorf("%+v: %v", c2, err)
		}
	}
	if n := c.EndRun("other"); n != 0 {
		t.Fatalf("EndRun of another run revoked %d", n)
	}
	if n := c.EndRun("run-1"); n != 2 {
		t.Fatalf("EndRun revoked %d, want 2", n)
	}
	if _, err = c.Resolve(tok, "owner"); !errors.Is(err, ErrStaleCapability) {
		t.Fatalf("after run end: %v", err)
	}
	if n := c.EndRun("run-1"); n != 0 {
		t.Fatalf("second EndRun %d", n)
	}

	tok3, _ := c.Issue(g)
	now = now.Add(2 * time.Hour)
	if _, err = c.Resolve(tok3, "owner"); !errors.Is(err, ErrStaleCapability) {
		t.Fatalf("expired: %v", err)
	}
	now = time.Unix(1000, 0)
	g.ExpiresAt = now.Add(time.Hour)
	tok4, _ := c.Issue(g)
	if n := c.RevokeFolder("owner"); n != 2 { // tok3 became valid again with the clock reset
		t.Fatalf("RevokeFolder %d", n)
	}
	if _, err = c.Resolve(tok4, "owner"); !errors.Is(err, ErrStaleCapability) {
		t.Fatalf("after folder revoke: %v", err)
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := []struct{ in, fallback, want string }{
		{"report.csv", "x", "report.csv"},
		{"../../etc/passwd", "x", "passwd"},
		{`C:\dir\file.txt`, "x", "file.txt"},
		{"a\x00b\x1f.txt", "x", "ab.txt"},
		{"evil\u202egnp.exe", "x", "evilgnp.exe"},
		{"  ..name.. ", "x", "name"},
		{"", "/workspace/chat/out/fallback.md", "fallback.md"},
		{"..", "..", "file"},
		{"", "", "file"},
		{`quo"te:colon.txt`, "x", "quotecolon.txt"},
	}
	for _, c := range cases {
		if got := sanitizeFilename(c.in, c.fallback); got != c.want {
			t.Errorf("%q/%q = %q, want %q", c.in, c.fallback, got, c.want)
		}
	}
	long := strings.Repeat("é", 100) + ".markdown"
	got := sanitizeFilename(long, "")
	if len(got) > maxFilenameBytes || !strings.HasSuffix(got, ".markdown") {
		t.Errorf("long name %q (%d bytes)", got, len(got))
	}
}

func TestCapabilityGraceAndPruning(t *testing.T) {
	base := resolved(t)
	chats := filepath.Join(base, "chats")
	os.MkdirAll(filepath.Join(chats, "owner"), 0o755)
	now := time.Unix(10_000, 0)
	c := NewCapabilities(OutputRootPolicy{DataDir: filepath.Join(base, "data"), ChatsDir: chats}, func() time.Time { return now }, staticResolver(map[string]string{"owner": "tg:1"}))
	g := Grant{Folder: "owner", RunRef: "run-1", Actor: "owner", Destination: testScope(), Owner: store.OwnerLegacyAgentIPC,
		OutputRoot: filepath.Join(chats, "owner"), ExpiresAt: now.Add(time.Hour)}
	tok, _ := c.Issue(g)

	written := now.Add(-time.Second)
	c.EndRun("run-1")
	if _, err := c.ResolveAt(tok, "owner", written); err != nil {
		t.Fatalf("file written before the run ended: %v", err)
	}
	if _, err := c.ResolveAt(tok, "owner", now.Add(time.Second)); !errors.Is(err, ErrStaleCapability) {
		t.Fatalf("file written after the run ended: %v", err)
	}
	if _, err := c.ResolveAt(tok, "owner", time.Time{}); !errors.Is(err, ErrStaleCapability) {
		t.Fatalf("unknown write time: %v", err)
	}
	if _, err := c.Resolve(tok, "owner"); !errors.Is(err, ErrStaleCapability) {
		t.Fatalf("strict resolve after run end: %v", err)
	}
	now = now.Add(DefaultRevokeGrace + time.Second)
	if _, err := c.ResolveAt(tok, "owner", written); !errors.Is(err, ErrStaleCapability) {
		t.Fatalf("after the grace window: %v", err)
	}
	// Issuing prunes expired and revoked-past-grace entries.
	now = now.Add(2 * time.Hour)
	g.ExpiresAt = now.Add(time.Hour)
	c.Issue(g)
	c.mu.Lock()
	n := len(c.byHash)
	c.mu.Unlock()
	if n != 1 {
		t.Fatalf("registry holds %d entries after pruning, want 1", n)
	}
}
