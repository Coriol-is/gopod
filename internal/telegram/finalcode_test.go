package telegram

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// codeFake answers sendDocument with id 321 (or a 400 when docFails),
// sendMessage and edits with id 555.
func codeFake(docFails bool) *fakeTelegram {
	return &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
		switch method {
		case "sendDocument":
			if docFails {
				return apiError(400, "Bad Request: file must be non-empty")
			}
			return sentMessage("321")
		case "sendMessage", "editMessageText":
			return sentMessage("555")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
}

// streamAll runs streamReply over one chunk of text and returns its error.
func streamAll(t *testing.T, b *Bot, item queue.Item, text string) error {
	t.Helper()
	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result})
	}()
	chunks <- text
	close(chunks)
	result <- runner.RunResult{FullText: text}
	close(result)
	select {
	case err := <-errc:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("streamReply did not return")
		return nil
	}
}

// checkOneReply asserts the turn recorded exactly one reply, with id 555.
func checkOneReply(t *testing.T, b *Bot, item queue.Item) {
	t.Helper()
	row, err := b.store.GetTurn(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ReplyMsgID != 555 {
		t.Errorf("reply_msg_id = %d, want 555 (the text message, not a document)", row.ReplyMsgID)
	}
	if n := countBotMessages(t, b, item); n != 1 {
		t.Errorf("recorded replies = %d, want 1", n)
	}
}

func TestFinalReply_GiantBlockGoesAsDocument(t *testing.T) {
	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	reply := "Wrote the parser:\n" + fence("go", 100) + "\nTests pass."

	if err := streamAll(t, b, item, reply); err != nil {
		t.Fatalf("streamReply: %v", err)
	}
	docs := fake.callsTo("sendDocument")
	if len(docs) != 1 {
		t.Fatalf("sendDocument calls = %d, want 1", len(docs))
	}
	if f := docs[0].Form; f["document.filename"] != "snippet.go" || !strings.HasPrefix(f["document.content"], "line 1 <x>\n") {
		t.Errorf("document form = %v", f)
	}
	msgs := fake.callsTo("sendMessage")
	if len(msgs) != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", len(msgs))
	}
	if got, want := msgs[0].Form["text"], "Wrote the parser:\n📎 snippet.go (100 lines)\nTests pass."; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	checkOneReply(t, b, item)
}

func TestFinalReply_MediumBlockCollapsed(t *testing.T) {
	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	if err := streamAll(t, b, item, "Here:\n"+fence("py", 20)); err != nil {
		t.Fatalf("streamReply: %v", err)
	}
	if n := len(fake.callsTo("sendDocument")); n != 0 {
		t.Errorf("sendDocument calls = %d, want 0", n)
	}
	msgs := fake.callsTo("sendMessage")
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0].Form["text"], "Here:\n<blockquote expandable><pre>line 1 ") {
		t.Fatalf("sendMessage = %v, want one collapsed block", msgs)
	}
	checkOneReply(t, b, item)
}

func TestFinalReply_DocumentFailureKeepsCodeInline(t *testing.T) {
	fake := codeFake(true)
	b, item := newStreamFixture(t, fake)
	if err := streamAll(t, b, item, "Here:\n"+fence("go", 100)); err != nil {
		t.Fatalf("streamReply: %v", err)
	}
	if n := len(fake.callsTo("sendDocument")); n != 1 {
		t.Errorf("sendDocument calls = %d, want 1", n)
	}
	var text strings.Builder
	for _, c := range fake.callsTo("sendMessage") {
		text.WriteString(c.Form["text"])
	}
	got := text.String()
	if !strings.Contains(got, "<blockquote expandable><pre>line 1 &lt;x&gt;") || !strings.Contains(got, "line 100 &lt;x&gt;") {
		t.Errorf("code not inline after a failed document: %q", got)
	}
	if strings.Contains(got, "📎") {
		t.Errorf("note for an unsent document: %q", got)
	}
	checkOneReply(t, b, item)
}

func TestFinalReply_EditPathSendsDocument(t *testing.T) {
	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	b.markDraftsUnsupported(item.ChatID)
	if err := streamAll(t, b, item, "Done:\n"+fence("sh", 70)); err != nil {
		t.Fatalf("streamReply: %v", err)
	}
	if n := len(fake.callsTo("sendDocument")); n != 1 {
		t.Errorf("sendDocument calls = %d, want 1", n)
	}
	edits := fake.callsTo("editMessageText")
	if len(edits) == 0 {
		t.Fatal("no final edit")
	}
	if got := edits[len(edits)-1].Form["text"]; got != "Done:\n📎 snippet.sh (70 lines)" {
		t.Errorf("final edit = %q", got)
	}
}

func TestFinalReply_GoneTopicSendsDocumentToMain(t *testing.T) {
	fake := docFake(true) // every send into thread 7 fails: topic deleted
	b, item := newStreamFixture(t, fake)
	item.ThreadID = 7
	b.markThreadGone(item.ChatID, 7) // the draft path learned it mid-turn
	if err := streamAll(t, b, item, "Here:\n"+fence("go", 100)); err != nil {
		t.Fatalf("streamReply: %v", err)
	}
	inThread, inMain := callsIn(fake, "sendDocument")
	if len(inThread) != 0 || len(inMain) != 1 {
		t.Errorf("documents in thread/main = %d/%d, want 0/1", len(inThread), len(inMain))
	}
	_, msgs := callsIn(fake, "sendMessage")
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0].Form["text"], "[The topic") ||
		!strings.Contains(msgs[0].Form["text"], "📎 snippet.go (100 lines)") {
		t.Errorf("main-chat reply = %v", msgs)
	}
}

func TestFinalReply_DocumentFollowsThread(t *testing.T) {
	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	item.ThreadID = 7
	if err := streamAll(t, b, item, fence("go", 100)); err != nil {
		t.Fatalf("streamReply: %v", err)
	}
	docs := fake.callsTo("sendDocument")
	if len(docs) != 1 || docs[0].Form["message_thread_id"] != "7" {
		t.Errorf("document not sent into the thread: %v", docs)
	}
}

func TestFinishStopped_UnclosedGiantFence(t *testing.T) {
	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	chunks := make(chan string)
	close(chunks)
	result := make(chan runner.RunResult, 1)
	result <- runner.RunResult{}
	partial := "Writing it now:\n```go\n" + strings.Repeat("x := 1\n", 80) + "y :="

	err := b.finishStopped(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result}, partial)
	if err != queue.ErrStopped {
		t.Fatalf("err = %v, want ErrStopped", err)
	}
	if n := len(fake.callsTo("sendDocument")); n != 1 {
		t.Errorf("sendDocument calls = %d, want 1", n)
	}
	msgs := fake.callsTo("sendMessage")
	if len(msgs) != 1 || msgs[0].Form["text"] != "Writing it now:\n📎 snippet.go (81 lines)" {
		t.Errorf("stopped reply = %v", msgs)
	}
	checkOneReply(t, b, item)
}

func TestStripCodeBlocks(t *testing.T) {
	in := "Changed two files.\n```go\nfunc a() {}\n```\nTests pass.\n```\nunclosed"
	if got, want := stripCodeBlocks(in), "Changed two files.\nTests pass."; got != want {
		t.Errorf("stripCodeBlocks = %q, want %q", got, want)
	}
}
