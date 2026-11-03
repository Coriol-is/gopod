package ipc

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// wantNotice asserts exactly one refusal notice to the owner chat naming the
// path and the code, and returns its text.
func (f *artFixture) wantNotice(path, code string) string {
	f.t.Helper()
	calls := f.sink.callsSnapshot()
	if len(calls) != 1 {
		f.t.Fatalf("want one refusal notice, got %+v", calls)
	}
	c := calls[0]
	if c.chatJID != "tg:1" || !strings.Contains(c.text, path) || !strings.Contains(c.text, "("+code+")") || !strings.Contains(c.text, "Не удалось приложить") {
		f.t.Fatalf("notice = %+v, want path %q and code %q", c, path, code)
	}
	return c.text
}

// GOPOD_OUTBOX_ARTIFACTS=0: the watcher has no artifact route. A V1 file
// request is refused with artifacts_disabled and a generic notice. Nothing
// from the unvalidated request (including paths and text) is sent.
func TestAttachmentDisabledRefusesWithReceiptAndNotice(t *testing.T) {
	f := newArtFixture(t)
	f.write("banks/report.pdf", "%PDF-1.4")
	w := f.durableFixture.watcher() // durable route on, artifacts off
	f.sendV1("owner", "0001-a.json", f.req("banks/report.pdf"))
	f.tick(w)

	f.wantError("0001-a.json", codeArtifactsDisabled)
	rc := f.receipt("owner", "0001-a.result.json")
	if help, _ := rc["help"].(string); !strings.Contains(help, "ATTACHMENTS.md") || !strings.Contains(help, "NOT attached") {
		t.Fatalf("receipt help = %q", rc["help"])
	}
	text := f.wantNotice("", codeArtifactsDisabled)
	if strings.Contains(text, "/workspace/chat/") {
		t.Fatal("untrusted path leaked")
	}
	if strings.Contains(text, "report attached") {
		t.Fatalf("the request text leaked into the notice: %q", text)
	}
	if f.deliveries() != 0 {
		t.Fatal("a refused file request published a delivery")
	}
}

// With the route on, refusals that name a file reach the user too.
func TestAttachmentRouteRefusalsNotifyTheUser(t *testing.T) {
	cases := []struct {
		name, path, code string
		setup            func(f *artFixture) map[string]any
	}{
		{"outside the workspace", "/etc/passwd", codePathOutside, func(f *artFixture) map[string]any {
			b := f.req()
			b["files"] = []map[string]any{{"path": "/etc/passwd"}}
			return b
		}},
		{"too large", "/workspace/chat/big.zip", codeFileTooLarge, func(f *artFixture) map[string]any {
			if err := f.st.SetArtifactLimits(store.ArtifactLimits{MaxFileBytes: 100, MaxRequestBytes: 150}); err != nil {
				f.t.Fatal(err)
			}
			f.write("big.zip", strings.Repeat("x", 200))
			return f.req("big.zip")
		}},
		{"no capability", "/workspace/chat/a.pdf", codeArtifactsDisabled, func(f *artFixture) map[string]any {
			f.write("a.pdf", "x")
			b := f.req("a.pdf")
			delete(b, "capability")
			return b
		}},
		{"photo forced on a PDF", "/workspace/chat/a.pdf", codeUnsupportedType, func(f *artFixture) map[string]any {
			f.write("a.pdf", "x")
			b := f.req()
			b["files"] = []map[string]any{{"path": "/workspace/chat/a.pdf", "as": "photo"}}
			return b
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newArtFixture(t)
			w := f.watcher()
			f.sendV1("owner", "0001-r.json", c.setup(f))
			f.tick(w)
			f.wantError("0001-r.json", c.code)
			path := c.path
			if c.name == "no capability" || c.name == "photo forced on a PDF" {
				path = ""
			}
			text := f.wantNotice(path, c.code)
			if path == "" && strings.Contains(text, c.path) {
				t.Fatal("unvalidated path leaked")
			}
			f.assertNoLeak(c.name)
		})
	}
}

// An accepted attachment sends no notice: the upload part is the answer.
func TestAttachmentAcceptedSendsNoNotice(t *testing.T) {
	f := newArtFixture(t)
	f.write("a.csv", "x")
	w := f.watcher()
	f.sendV1("owner", "0001-ok.json", f.req("a.csv"))
	f.tick(w)
	if rc := f.receipt("owner", "0001-ok.result.json"); rc["status"] == "error" {
		t.Fatalf("receipt = %v", rc)
	}
	if calls := f.sink.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("unexpected notice %+v", calls)
	}
}

func TestAttachmentNoticeSanitizesAndBoundsPaths(t *testing.T) {
	long := "/workspace/chat/" + strings.Repeat("я", 300) + ".pdf"
	text := attachmentNotice([]artifactFile{{Path: "/workspace/chat/a\u202efdp.exe\n"}, {Path: long}, {Path: ""}}, "made_up_code")
	if strings.ContainsAny(text, "\u202e") || strings.Contains(text, "exe\n\n") {
		t.Fatalf("control or bidi characters survived: %q", text)
	}
	if strings.Contains(text, long) || !strings.Contains(text, "…") {
		t.Fatalf("long path not bounded: %q", text)
	}
	if !strings.Contains(text, "(без пути)") || !strings.Contains(text, "(made_up_code)") {
		t.Fatalf("notice = %q", text)
	}
}

// Strict-decode failures have no validated grant: notify generically without
// revealing request paths in another conversation.
func TestAttachmentParseFailureDoesNotRevealPaths(t *testing.T) {
	for name, body := range map[string]map[string]any{
		"unknown top-level field": {"version": 1, "requestId": "req-00000001", "aaa": 1,
			"files": []map[string]any{{"path": "/workspace/chat/a.pdf"}}},
		"typo in a file entry": {"version": 1, "requestId": "req-00000001",
			"files": []map[string]any{{"path": "/workspace/chat/a.pdf", "captoin": "x"}}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newArtFixture(t)
			body["capability"] = f.token
			w := f.watcher()
			f.sendV1("owner", "0001-p.json", body)
			f.tick(w)
			f.wantError("0001-p.json", codeInvalidRequest)
			text := f.wantNotice("", codeInvalidRequest)
			if strings.Contains(text, "/workspace/chat/a.pdf") {
				t.Fatal("parse failure leaked path")
			}
		})
	}
}

type scopedNoticeSink struct {
	*fakeMessageSink
	destinations []store.ConversationScope
}

func (s *scopedNoticeSink) SendConversation(ctx context.Context, d store.ConversationScope, text string) error {
	s.destinations = append(s.destinations, d)
	return s.Send(ctx, d.Scope.JID, text)
}

func TestAttachmentRefusalUsesHostGrantTopic(t *testing.T) {
	f := newArtFixture(t)
	conv, err := f.st.SnapshotConversation(t.Context(), "tg:1", 7, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	f.token, err = f.caps.Issue(Grant{Folder: "owner", Actor: "owner", RunRef: "topic-run", Destination: conv, Owner: store.OwnerLegacyAgentIPC, OutputRoot: filepath.Join(f.chats, "owner"), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	w := f.watcher()
	sink := &scopedNoticeSink{fakeMessageSink: f.sink}
	w.msgs = sink
	f.sendV1("owner", "0001-topic.json", f.req("missing.pdf"))
	f.tick(w)
	f.wantError("0001-topic.json", codeSourceMissing)
	text := f.wantNotice("/workspace/chat/missing.pdf", codeSourceMissing)
	if len(sink.destinations) != 1 || sink.destinations[0] != conv {
		t.Fatalf("wrong host destination: %+v", sink.destinations)
	}
	if strings.Contains(text, "остался") || strings.Contains(attachmentHelp, "stays") {
		t.Fatal("claims nonexistent file remains")
	}
}

func TestAttachmentUntrustedGrantDoesNotRevealPaths(t *testing.T) {
	for _, kind := range []string{"missing", "stale", "foreign destination", "request thread"} {
		t.Run(kind, func(t *testing.T) {
			f := newArtFixture(t)
			w := f.watcher()
			sink := &scopedNoticeSink{fakeMessageSink: f.sink}
			w.msgs = sink
			req := f.req("private-name.pdf")
			switch kind {
			case "missing":
				delete(req, "capability")
			case "stale":
				req["capability"] = "invalid"
			case "foreign destination":
				req["chatJid"] = "tg:999"
			case "request thread":
				req["threadId"] = 999
			}
			f.sendV1("owner", "0001-refused.json", req)
			f.tick(w)
			calls := f.sink.callsSnapshot()
			if len(calls) != 1 || len(sink.destinations) != 0 || calls[0].chatJID != "tg:1" || strings.Contains(calls[0].text, "private-name") {
				t.Fatalf("unsafe refusal: %+v %+v", calls, sink.destinations)
			}
		})
	}
}

func TestAttachmentTopicRefusalDoesNotFallbackWithoutScopedSink(t *testing.T) {
	f := newArtFixture(t)
	conv, err := f.st.SnapshotConversation(t.Context(), "tg:1", 7, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	f.token, err = f.caps.Issue(Grant{Folder: "owner", Actor: "owner", RunRef: "topic-run", Destination: conv, Owner: store.OwnerLegacyAgentIPC, OutputRoot: filepath.Join(f.chats, "owner"), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	w := f.watcher()
	f.sendV1("owner", "0001-topic.json", f.req("missing.pdf"))
	f.tick(w)
	f.wantError("0001-topic.json", codeSourceMissing)
	if len(f.sink.callsSnapshot()) != 0 {
		t.Fatal("topic notice rerouted to main")
	}
}
