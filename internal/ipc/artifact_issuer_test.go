package ipc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// fakeScopes returns a fixed snapshot per JID and records the requests.
type fakeScopes struct {
	byJID map[string]store.ConversationScope
	err   error
	asked []string
}

func (f *fakeScopes) SnapshotConversation(_ context.Context, jid string, thread int, v store.Visibility) (store.ConversationScope, error) {
	f.asked = append(f.asked, jid)
	if f.err != nil {
		return store.ConversationScope{}, f.err
	}
	sc, ok := f.byJID[jid]
	if !ok {
		return store.ConversationScope{}, store.ErrStaleScope
	}
	sc.ThreadID, sc.Visibility = thread, v
	return sc, nil
}

type issuerFixture struct {
	iss    *RunIssuer
	caps   *Capabilities
	scopes *fakeScopes
	now    *time.Time
	chats  string
}

func newIssuerFixture(t *testing.T) *issuerFixture {
	t.Helper()
	base := resolved(t)
	chats := filepath.Join(base, "chats")
	for _, f := range []string{"owner", "friend", "group"} {
		if err := os.MkdirAll(filepath.Join(chats, f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	jids := map[string]string{"owner": "tg:1", "friend": "tg:9", "group": "tg:-100123"}
	now := time.Unix(1000, 0)
	clock := &now
	caps := NewCapabilities(OutputRootPolicy{DataDir: filepath.Join(base, "data"), ChatsDir: chats},
		func() time.Time { return *clock }, staticResolver(jids))
	scopeOf := func(jid, folder string) store.ConversationScope {
		return store.ConversationScope{Scope: store.ScopeRef{NamespaceID: "11111111-1111-4111-8111-111111111111", NamespaceEpoch: 1, RegistrationRevision: 1, JID: jid, Folder: folder}}
	}
	scopes := &fakeScopes{byJID: map[string]store.ConversationScope{
		"tg:1": scopeOf("tg:1", "owner"), "tg:9": scopeOf("tg:9", "friend"), "tg:-100123": scopeOf("tg:-100123", "group"),
	}}
	return &issuerFixture{caps: caps, scopes: scopes, now: clock, chats: chats,
		iss: &RunIssuer{Caps: caps, Scopes: scopes, OwnerJID: "tg:1", ChatsDir: chats, Resolve: staticResolver(jids), Now: func() time.Time { return *clock }}}
}

func TestIssuerGrantsTheOwnerChatABoundCapability(t *testing.T) {
	f := newIssuerFixture(t)
	tok, end := f.iss.IssueArtifact("owner", 5)
	if tok == "" || end == nil {
		t.Fatal("owner chat got no capability")
	}
	g, err := f.caps.Resolve(tok, "owner")
	if err != nil {
		t.Fatal(err)
	}
	want := f.scopes.byJID["tg:1"]
	want.ThreadID, want.Visibility = 5, store.VisibilityConversation
	if g.Destination != want {
		t.Fatalf("destination = %+v, want the scope snapshot %+v", g.Destination, want)
	}
	if g.Owner != store.OwnerLegacyAgentIPC || g.Folder != "owner" || g.OutputRoot != filepath.Join(f.chats, "owner") || g.RunRef == "" {
		t.Fatalf("grant = %+v", g)
	}
	if !g.ExpiresAt.After(f.now.Add(time.Hour)) || g.ExpiresAt.After(f.now.Add(MaxGrantTTL)) {
		t.Fatalf("expiry %v is not a long, capped backstop", g.ExpiresAt)
	}
	// The token is for this folder only.
	if _, err := f.caps.Resolve(tok, "friend"); !errors.Is(err, ErrStaleCapability) {
		t.Fatalf("other folder resolved: %v", err)
	}
}

func TestIssuerRefusesEverythingButTheOwnersPrivateChat(t *testing.T) {
	f := newIssuerFixture(t)
	for _, folder := range []string{"friend", "group", "unknown", ""} {
		if tok, end := f.iss.IssueArtifact(folder, 0); tok != "" || end == nil {
			t.Errorf("%q: token %q (end nil: %v), want none and a no-op end", folder, tok, end == nil)
		} else {
			end()
		}
	}
	if len(f.scopes.asked) != 0 {
		t.Fatalf("scope snapshots taken for non-owner folders: %v", f.scopes.asked)
	}
	// An owner JID that is not a private user chat is a misconfiguration: nothing issues.
	for _, bad := range []string{"tg:-100123", "tg:0", "tg:", "", "1", "tg:abc"} {
		f.iss.OwnerJID = bad
		if tok, _ := f.iss.IssueArtifact("owner", 0); tok != "" {
			t.Errorf("OwnerJID %q issued a capability", bad)
		}
	}
	f.iss.OwnerJID = "tg:-100123"
	if tok, _ := f.iss.IssueArtifact("group", 0); tok != "" {
		t.Error("a group issued a capability even when named as the owner chat")
	}
}

func TestIssuerFailsClosed(t *testing.T) {
	cases := map[string]func(*issuerFixture){
		"snapshot error":   func(f *issuerFixture) { f.scopes.err = errors.New("db down") },
		"stale snapshot":   func(f *issuerFixture) { delete(f.scopes.byJID, "tg:1") },
		"no output root":   func(f *issuerFixture) { _ = os.RemoveAll(filepath.Join(f.chats, "owner")) },
		"negative thread":  nil,
		"no registry":      func(f *issuerFixture) { f.iss.Caps = nil },
		"no scopes":        func(f *issuerFixture) { f.iss.Scopes = nil },
		"no resolver":      func(f *issuerFixture) { f.iss.Resolve = nil },
		"nil issuer":       func(f *issuerFixture) { f.iss = nil },
		"relative chats":   func(f *issuerFixture) { f.iss.ChatsDir = "chats" },
		"folder with dots": nil,
	}
	for name, mutate := range cases {
		f := newIssuerFixture(t)
		thread, folder := 0, "owner"
		switch name {
		case "negative thread":
			thread = -1
		case "folder with dots":
			folder = "../owner"
		default:
			mutate(f)
		}
		tok, end := f.iss.IssueArtifact(folder, thread)
		if tok != "" {
			t.Errorf("%s: issued %q", name, tok)
		}
		if end == nil {
			t.Errorf("%s: nil end", name)
		} else {
			end()
		}
	}
}

func TestIssuerRevocationAfterExecEnd(t *testing.T) {
	f := newIssuerFixture(t)
	tok, end := f.iss.IssueArtifact("owner", 0)
	written := f.now.Add(-time.Second)
	if _, err := f.caps.ResolveAt(tok, "owner", written); err != nil {
		t.Fatal(err)
	}
	end()
	end() // idempotent
	// A request written before the exec ended is honoured for the grace window.
	if _, err := f.caps.ResolveAt(tok, "owner", written); err != nil {
		t.Fatalf("request written before the end: %v", err)
	}
	// One written after the end, or with an unknown write time, is stale.
	if _, err := f.caps.ResolveAt(tok, "owner", f.now.Add(time.Second)); !errors.Is(err, ErrStaleCapability) {
		t.Fatalf("request written after the end: %v", err)
	}
	if _, err := f.caps.Resolve(tok, "owner"); !errors.Is(err, ErrStaleCapability) {
		t.Fatalf("unknown write time: %v", err)
	}
	// Past the grace window nothing is honoured.
	*f.now = f.now.Add(DefaultRevokeGrace + time.Second)
	if _, err := f.caps.ResolveAt(tok, "owner", written); !errors.Is(err, ErrStaleCapability) {
		t.Fatalf("after grace: %v", err)
	}
}

func TestIssuerEndOfOneExecKeepsAnotherExecsCapability(t *testing.T) {
	f := newIssuerFixture(t)
	tok1, end1 := f.iss.IssueArtifact("owner", 0)
	tok2, end2 := f.iss.IssueArtifact("owner", 0)
	if tok1 == tok2 {
		t.Fatal("two execs share a token")
	}
	end1()
	*f.now = f.now.Add(DefaultRevokeGrace + time.Second)
	if _, err := f.caps.Resolve(tok2, "owner"); err != nil {
		t.Fatalf("second exec lost its capability: %v", err)
	}
	end2()
}

func TestIssuerCapabilityExpiresAsABackstop(t *testing.T) {
	f := newIssuerFixture(t)
	tok, _ := f.iss.IssueArtifact("owner", 0)
	*f.now = f.now.Add(MaxGrantTTL + time.Minute)
	if _, err := f.caps.Resolve(tok, "owner"); !errors.Is(err, ErrStaleCapability) {
		t.Fatalf("expired capability resolved: %v", err)
	}
}
