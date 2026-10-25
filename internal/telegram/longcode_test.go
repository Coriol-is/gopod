package telegram

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestSplitLongCode_ShortAndMediumBlocksStay(t *testing.T) {
	md := "a\n" + fence("go", 5) + "\nb\n" + fence("go", docOverLines) + "\nc"
	r := splitLongCode(md)
	if len(r.docs) != 0 {
		t.Fatalf("docs = %d, want 0", len(r.docs))
	}
	if got := r.text(nil); got != md {
		t.Errorf("text changed\ngot:  %q\nwant: %q", got, md)
	}
}

func TestSplitLongCode_GiantBlocksBecomeNotes(t *testing.T) {
	big := fence("go", docOverLines+40)                                   // 100 lines
	wide := "```python\n" + strings.Repeat("x", docOverChars+1) + "\n```" // 1 line, too long
	md := "Here:\n" + big + "\nand\n" + wide + "\n" + fence("rust", 3) + "\nbye"
	r := splitLongCode(md)
	if len(r.docs) != 2 {
		t.Fatalf("docs = %d, want 2", len(r.docs))
	}
	d := r.docs[0]
	if d.Name != "snippet.go" || d.Lines != 100 {
		t.Errorf("doc 0 = %s, %d lines; want snippet.go, 100", d.Name, d.Lines)
	}
	if !strings.HasPrefix(d.Body, "line 1 <x>\n") || !strings.HasSuffix(d.Body, "line 100 <x>\n") || strings.Contains(d.Body, "```") {
		t.Errorf("doc 0 body is not the bare code: %q...", d.Body[:40])
	}
	if r.docs[1].Name != "snippet-2.py" || r.docs[1].Lines != 1 {
		t.Errorf("doc 1 = %s, %d lines; want snippet-2.py, 1", r.docs[1].Name, r.docs[1].Lines)
	}

	want := "Here:\n📎 snippet.go (100 lines)\nand\n📎 snippet-2.py (1 line)\n" + fence("rust", 3) + "\nbye"
	if got := r.text([]bool{true, true}); got != want {
		t.Errorf("all sent\ngot:  %q\nwant: %q", got, want)
	}
	// A document that failed keeps its block inline, as written.
	if got := r.text([]bool{false, true}); !strings.Contains(got, big) || strings.Contains(got, "snippet.go") {
		t.Errorf("failed doc not inlined: %q", got[:80])
	}
}

func TestSplitLongCode_UnclosedFence(t *testing.T) {
	md := "stopped mid-way\n```sh\n" + strings.Repeat("echo hi\n", 70)
	r := splitLongCode(md)
	if len(r.docs) != 1 || r.docs[0].Name != "snippet.sh" {
		t.Fatalf("docs = %+v, want one snippet.sh", r.docs)
	}
	if got := r.text([]bool{true}); got != "stopped mid-way\n📎 snippet.sh (71 lines)" {
		t.Errorf("text = %q", got)
	}
	if got := r.text([]bool{false}); got != md {
		t.Errorf("failed doc text = %q, want the original", got)
	}
}

func TestSnippetName(t *testing.T) {
	cases := []struct {
		fence string
		n     int
		want  string
	}{
		{"```go", 1, "snippet.go"},
		{"```Python", 1, "snippet.py"},
		{"``` py title=x", 1, "snippet.py"},
		{"```ts", 3, "snippet-3.ts"},
		{"```yml", 1, "snippet.yaml"},
		{"```", 1, "snippet.txt"},
		{"```brainfuck", 2, "snippet-2.txt"},
	}
	for _, c := range cases {
		if got := snippetName(fenceLang(c.fence), c.n); got != c.want {
			t.Errorf("snippetName(%q, %d) = %q, want %q", c.fence, c.n, got, c.want)
		}
	}
}

// docFake answers sendDocument and sendMessage; documents into thread 7
// fail as a deleted topic when gone is set.
func docFake(gone bool) *fakeTelegram {
	return &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
		if gone && form["message_thread_id"] == "7" {
			return apiError(400, "Bad Request: message thread not found")
		}
		if method == "sendDocument" || method == "sendMessage" {
			return sentMessage("321")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
}

func TestSendDocument_IntoThread(t *testing.T) {
	fake := docFake(false)
	b := newFakeBot(t, fake)
	id, err := b.sendDocument(context.Background(), streamChat, 7, "snippet.go", []byte("package x\n"), "1 line")
	if err != nil || id != 321 {
		t.Fatalf("sendDocument = %d, %v", id, err)
	}
	calls := fake.callsTo("sendDocument")
	if len(calls) != 1 {
		t.Fatalf("sendDocument calls = %d, want 1", len(calls))
	}
	f := calls[0].Form
	if f["message_thread_id"] != "7" || f["document.filename"] != "snippet.go" ||
		f["document.content"] != "package x\n" || f["caption"] != "1 line" {
		t.Errorf("form = %v", f)
	}
}

func TestSendDocument_TopicGoneFallsBackToMain(t *testing.T) {
	fake := docFake(true)
	b := newFakeBot(t, fake)
	ctx := context.Background()
	id, err := b.sendDocument(ctx, streamChat, 7, "snippet.go", []byte("package x\n"), "")
	if err != nil || id != 321 {
		t.Fatalf("sendDocument = %d, %v", id, err)
	}
	inThread, inMain := callsIn(fake, "sendDocument")
	if len(inThread) != 1 || len(inMain) != 1 {
		t.Fatalf("calls in thread/main = %d/%d, want 1/1", len(inThread), len(inMain))
	}
	if inMain[0].Form["document.content"] != "package x\n" {
		t.Errorf("retry to main lost the file: %v", inMain[0].Form)
	}
	if !b.threadGone(streamChat, 7) {
		t.Error("thread not marked gone")
	}
	// Known gone: straight to main.
	if _, err := b.sendDocument(ctx, streamChat, 7, "snippet-2.go", []byte("x"), ""); err != nil {
		t.Fatal(err)
	}
	if inThread, inMain = callsIn(fake, "sendDocument"); len(inThread) != 1 || len(inMain) != 2 {
		t.Errorf("calls in thread/main = %d/%d, want 1/2", len(inThread), len(inMain))
	}
}

func TestSendDocument_OtherErrorReturned(t *testing.T) {
	fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) {
		return apiError(400, "Bad Request: file is too big")
	}}
	b := newFakeBot(t, fake)
	if _, err := b.sendDocument(context.Background(), streamChat, 7, "a.txt", []byte("x"), ""); err == nil {
		t.Fatal("want error")
	}
	if n := len(fake.callsTo("sendDocument")); n != 1 {
		t.Errorf("sendDocument calls = %d, want 1 (no resend to main)", n)
	}
	if b.threadGone(streamChat, 7) {
		t.Error("an unrelated 400 marked the thread gone")
	}
}
