package taskreport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

func envelope(token string, mut func(m map[string]any)) []byte {
	m := map[string]any{
		"version": 1, "runId": "run-1", "attemptId": "att-1", "runToken": token,
		"outcome":    map[string]any{"status": "partial", "checks": []string{"c"}, "evidence": []string{"/etc/shadow", "file:///x"}, "limitations": []string{}},
		"checkpoint": map[string]any{"planVersion": 3, "lastSupportedMilestone": "m", "nextSafeStep": "n", "unresolvedEffects": ""},
	}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func codeIs(t *testing.T, err error, want Code) {
	t.Helper()
	got, ok := CodeOf(err)
	if !ok || got != want {
		t.Fatalf("err = %v, want code %s", err, want)
	}
}

func TestParseAcceptsAndCanonicalizes(t *testing.T) {
	env, err := Parse(envelope("tok", nil))
	if err != nil {
		t.Fatal(err)
	}
	p, err := Canonical(env)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != "partial" || p.PlanVersion != 3 || !strings.Contains(p.OutcomeJSON, `"/etc/shadow"`) {
		t.Fatalf("provisional = %+v", p)
	}
	// Evidence is stored verbatim as untrusted text; nothing fetched it.
}

func TestParseNullPartsAndEmptyReport(t *testing.T) {
	env, err := Parse(envelope("tok", func(m map[string]any) { m["checkpoint"] = nil }))
	if err != nil {
		t.Fatal(err)
	}
	if p, err := Canonical(env); err != nil || p.CheckpointJSON != "" || p.OutcomeJSON == "" {
		t.Fatalf("%+v %v", p, err)
	}
	env, err = Parse(envelope("tok", func(m map[string]any) { m["checkpoint"] = nil; m["outcome"] = nil }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Canonical(env); err == nil {
		t.Fatal("a report with neither part must be rejected")
	}
}

func TestParseRejectsEveryViolation(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	list := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "x"
		}
		return out
	}
	outcome := func(f func(o map[string]any)) func(m map[string]any) {
		return func(m map[string]any) {
			o := m["outcome"].(map[string]any)
			f(o)
		}
	}
	cp := func(f func(c map[string]any)) func(m map[string]any) {
		return func(m map[string]any) { f(m["checkpoint"].(map[string]any)) }
	}
	for name, mut := range map[string]func(map[string]any){
		"wrong version":       func(m map[string]any) { m["version"] = 2 },
		"unknown top field":   func(m map[string]any) { m["scope"] = "other" },
		"model sets verified": func(m map[string]any) { m["hostVerified"] = true },
		"unknown outcome":     outcome(func(o map[string]any) { o["delivered"] = true }),
		"bad status":          outcome(func(o map[string]any) { o["status"] = "delivered" }),
		"too many items":      outcome(func(o map[string]any) { o["checks"] = list(MaxListItems + 1) }),
		"long item":           outcome(func(o map[string]any) { o["evidence"] = []string{long(MaxItemBytes + 1)} }),
		"wrong list type":     outcome(func(o map[string]any) { o["checks"] = "one" }),
		"nul byte":            outcome(func(o map[string]any) { o["limitations"] = []string{"a\x00b"} }),
		"plan zero":           cp(func(c map[string]any) { c["planVersion"] = 0 }),
		"plan huge":           cp(func(c map[string]any) { c["planVersion"] = MaxPlanVersion + 1 }),
		"plan string":         cp(func(c map[string]any) { c["planVersion"] = "3" }),
		"long milestone":      cp(func(c map[string]any) { c["lastSupportedMilestone"] = long(MaxCheckpointBytes + 1) }),
		"unknown checkpoint":  cp(func(c map[string]any) { c["owner"] = true }),
		"bad run id":          func(m map[string]any) { m["runId"] = "../x" },
		"empty token":         func(m map[string]any) { m["runToken"] = "" },
	} {
		if _, err := Parse(envelope("tok", mut)); err == nil {
			t.Errorf("%s: accepted", name)
		} else if c, _ := CodeOf(err); c != CodeInvalid {
			t.Errorf("%s: code %s", name, c)
		}
	}
	if _, err := Parse(append(envelope("tok", nil), []byte(` {"x":1}`)...)); err == nil {
		t.Error("trailing JSON accepted")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Error("garbage accepted")
	}
	big := envelope("tok", func(m map[string]any) {
		m["outcome"].(map[string]any)["evidence"] = []string{long(MaxItemBytes)}
		m["pad"] = long(MaxEnvelopeBytes)
	})
	_, err := Parse(big)
	codeIs(t, err, CodeTooLarge)
}

func newGrants(t *testing.T, scopeErr *error) (*Grants, *time.Time) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	g := NewGrants(func() time.Time { return now }, func(store.ScopeRef) error {
		if scopeErr != nil {
			return *scopeErr
		}
		return nil
	})
	return g, &now
}

var testBinding = Binding{Folder: "owner", RunID: "run-1", AttemptID: "att-1",
	Scope: store.ScopeRef{NamespaceID: "n", NamespaceEpoch: 1, RegistrationRevision: 1, JID: "tg:1", Folder: "owner"}}

func TestCapabilityBindsFolderRunAndAttempt(t *testing.T) {
	g, _ := newGrants(t, nil)
	tok, err := g.Issue(testBinding, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, b, err := g.Admit("owner", envelope(tok, nil)); err != nil || b.AttemptID != "att-1" {
		t.Fatalf("live: %v", err)
	}
	// Another namespace presenting a stolen token.
	_, _, err = g.Admit("other", envelope(tok, nil))
	codeIs(t, err, CodeStale)
	// Spoofed run / attempt under a live token.
	_, _, err = g.Admit("owner", envelope(tok, func(m map[string]any) { m["runId"] = "run-2" }))
	codeIs(t, err, CodeIdentity)
	_, _, err = g.Admit("owner", envelope(tok, func(m map[string]any) { m["attemptId"] = "att-2" }))
	codeIs(t, err, CodeIdentity)
	// Guessed token.
	_, _, err = g.Admit("owner", envelope("guess", nil))
	codeIs(t, err, CodeStale)
}

func TestCapabilityIsRevokedAtSettlementAndExpires(t *testing.T) {
	g, now := newGrants(t, nil)
	tok, _ := g.Issue(testBinding, time.Hour)
	if n := g.Revoke("att-1"); n != 1 {
		t.Fatalf("revoked %d", n)
	}
	_, _, err := g.Admit("owner", envelope(tok, nil))
	codeIs(t, err, CodeStale)
	if g.Active() != 0 {
		t.Fatal("revoked grant still active")
	}

	tok2, _ := g.Issue(testBinding, time.Minute)
	*now = now.Add(2 * time.Minute)
	_, _, err = g.Admit("owner", envelope(tok2, nil))
	codeIs(t, err, CodeStale)

	// TTL is capped.
	tok3, _ := g.Issue(testBinding, 100*time.Hour)
	*now = now.Add(MaxGrantTTL + time.Minute)
	_, _, err = g.Admit("owner", envelope(tok3, nil))
	codeIs(t, err, CodeStale)
}

func TestScopeChangeRejectsReport(t *testing.T) {
	scopeErr := error(nil)
	g, _ := newGrants(t, &scopeErr)
	tok, _ := g.Issue(testBinding, time.Hour)
	scopeErr = store.ErrStaleScope
	_, _, err := g.Admit("owner", envelope(tok, nil))
	codeIs(t, err, CodeScopeChanged)
}

func TestIssueRejectsBadBinding(t *testing.T) {
	g, _ := newGrants(t, nil)
	for _, b := range []Binding{{}, {Folder: "f", RunID: "../x", AttemptID: "a"}, {Folder: "f", RunID: "r", AttemptID: ""}} {
		if _, err := g.Issue(b, time.Hour); err == nil {
			t.Errorf("issued for %+v", b)
		}
	}
	if _, err := g.Issue(testBinding, 0); err == nil {
		t.Error("issued with zero ttl")
	}
}

func TestSweeperAdmitsOncePerFileAndNeverFollowsSymlinks(t *testing.T) {
	g, _ := newGrants(t, nil)
	tok, _ := g.Issue(testBinding, time.Hour)
	dir := t.TempDir()
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("01.json", envelope(tok, nil))
	write("02.json", envelope(tok, func(m map[string]any) { m["checkpoint"].(map[string]any)["planVersion"] = 4 }))
	write("03.json", envelope("wrong", nil))
	write("04.json", []byte(strings.Repeat("x", MaxEnvelopeBytes+1)))
	write("note.txt", []byte("ignored"))
	secret := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(secret, envelope(tok, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "05.json")); err != nil {
		t.Fatal(err)
	}
	var got []Provisional
	s := &Sweeper{Grants: g, Dir: dir, Folder: "owner", Record: func(_ context.Context, p Provisional) error {
		got = append(got, p)
		return nil
	}}
	res := s.Sweep(context.Background())
	if res.Admitted != 2 || res.Rejected[CodeStale] != 1 || res.Rejected[CodeTooLarge] != 1 || res.Rejected[CodeInvalid] != 1 {
		t.Fatalf("result = %+v", res)
	}
	if len(got) != 2 || got[0].PlanVersion != 3 || got[1].PlanVersion != 4 {
		t.Fatalf("order/revisions = %+v", got)
	}
	if again := s.Sweep(context.Background()); again.Admitted != 0 || len(again.Rejected) != 0 {
		t.Fatalf("second sweep re-admitted: %+v", again)
	}
	// Nothing was deleted or moved.
	if entries, _ := os.ReadDir(dir); len(entries) != 6 {
		t.Fatalf("sweeper changed the directory: %d entries", len(entries))
	}
	// After settlement every later file is late.
	g.Revoke("att-1")
	write("06.json", envelope(tok, nil))
	if late := s.Sweep(context.Background()); late.Admitted != 0 || late.Rejected[CodeStale] != 1 {
		t.Fatalf("late report: %+v", late)
	}
}

func TestSweeperRetriesWhenRecordFails(t *testing.T) {
	g, _ := newGrants(t, nil)
	tok, _ := g.Issue(testBinding, time.Hour)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "01.json"), envelope(tok, nil), 0o600)
	fail := true
	var n int
	s := &Sweeper{Grants: g, Dir: dir, Folder: "owner", Record: func(context.Context, Provisional) error {
		n++
		if fail {
			return errors.New("busy")
		}
		return nil
	}}
	if r := s.Sweep(context.Background()); r.Admitted != 0 {
		t.Fatal(fmt.Sprint(r))
	}
	fail = false
	if r := s.Sweep(context.Background()); r.Admitted != 1 || n != 2 {
		t.Fatalf("retry: %+v n=%d", r, n)
	}
}
