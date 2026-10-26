package groupsandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tarBytes(h *tar.Header, body string) []byte {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	_ = w.WriteHeader(h)
	_, _ = w.Write([]byte(body))
	_ = w.Close()
	return b.Bytes()
}
func TestSnapshotBoundary(t *testing.T) {
	good := tarBytes(&tar.Header{Name: "chat/file.txt", Mode: 0600, Size: 2, Typeflag: tar.TypeReg}, "ok")
	if _, err := canonicalArchive(bytes.NewReader(good), 1000, 1000); err != nil {
		t.Fatal(err)
	}
	for _, h := range []*tar.Header{
		{Name: "chat/../owner", Typeflag: tar.TypeReg},
		{Name: "/owner", Typeflag: tar.TypeReg},
		{Name: "chat/link", Typeflag: tar.TypeSymlink, Linkname: "/private"},
		{Name: "chat/link", Typeflag: tar.TypeLink, Linkname: "../private"},
		{Name: "chat/socket", Typeflag: tar.TypeFifo},
	} {
		if _, err := canonicalArchive(bytes.NewReader(tarBytes(h, "")), 1000, 1000); err == nil {
			t.Fatalf("accepted %#v", h)
		}
	}
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	e := &Executor{Root: root, UID: 1000, GID: 1000}
	if err := e.save("namespace-one", bytes.NewReader(good)); err != nil {
		t.Fatal(err)
	}
	got, err := e.snapshot("namespace-one")
	if err != nil || !bytes.Contains(got, []byte("ok")) {
		t.Fatal("snapshot restore failed", err)
	}
	if _, err = e.snapshot("namespace-two"); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(root, "namespace-two"))
	_ = os.Symlink(filepath.Join(root, "namespace-one"), filepath.Join(root, "namespace-two"))
	if _, err = e.snapshot("namespace-two"); err == nil {
		t.Fatal("followed namespace symlink")
	}
}

func TestMCPLeaseAndStrictArguments(t *testing.T) {
	m := &MCP{}
	revoked := false
	calls := 0
	token, revoke, err := m.Grant(context.Background(), Lease{Scope: "namespace-one", Expires: time.Now().Add(time.Minute), Validate: func(context.Context) error {
		if revoked {
			return ErrUnavailable
		}
		return nil
	}, Request: func(_ context.Context, args json.RawMessage) (string, error) { calls++; return "scoped", nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	request := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		m.ServeHTTP(w, r)
		return w
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group_request","arguments":{"kind":"memory_search","payload":{}}}}`
	if w := request("/mcp/foreign", body); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request("/mcp/"+token, body); w.Code != 200 || !strings.Contains(w.Body.String(), "scoped") || calls != 1 {
		t.Fatal(w.Body.String())
	}
	revoked = true
	if w := request("/mcp/"+token, body); w.Code != 403 || calls != 1 {
		t.Fatal("stale lease used")
	}
}

func TestSandboxDenyBeforeDocker(t *testing.T) {
	e := &Executor{}
	if _, err := e.Run(context.Background(), "../private", "cat /secret"); err == nil {
		t.Fatal("invalid scope admitted")
	}
	b := &limitedBuffer{remaining: 4}
	_, _ = b.Write([]byte("12345678"))
	if b.String() != "1234" {
		t.Fatal("unbounded output")
	}
}
