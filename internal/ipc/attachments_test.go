package ipc

import (
	"strings"
	"testing"

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
// request is refused with artifacts_disabled, the user is told where the file
// is, and nothing of the request (not even its text) is sent.
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
	text := f.wantNotice("/workspace/chat/banks/report.pdf", codeArtifactsDisabled)
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
			f.wantNotice(c.path, c.code)
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
