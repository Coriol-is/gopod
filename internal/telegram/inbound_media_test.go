package telegram

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// inboundFixture is an owner Bot on a fake API whose queue hands every
// enqueued item to items. getFile answers with file_path
// "<dir>/<file_id><ext>", where filePaths maps a file_id to that path
// (default "photos/<file_id>.jpg").
type inboundFixture struct {
	b     *Bot
	fake  *fakeTelegram
	items chan queue.Item
}

func newInboundFixture(t *testing.T, filePaths map[string]string) *inboundFixture {
	t.Helper()
	t.Chdir(t.TempDir()) // a zero Runner saves uploads under ./owner
	fake := &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
		switch method {
		case "getFile":
			id := form["file_id"]
			p, ok := filePaths[id]
			if !ok {
				p = "photos/" + id + ".jpg"
			}
			return http.StatusOK, `{"ok":true,"result":{"file_id":"` + id + `","file_unique_id":"u` + id + `","file_path":"` + p + `"}}`
		case "sendMessage":
			return sentMessage("555")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
	b, _ := newStreamFixture(t, fake)
	b.ownerChatID = streamChat
	b.logins = newLoginSessions()
	b.runner = &runner.Runner{}
	items := make(chan queue.Item, 4)
	q := queue.New(context.Background(), func(_ context.Context, batch []queue.Item) error {
		for _, it := range batch {
			items <- it
		}
		return nil
	}, 1, nil, b.log)
	t.Cleanup(q.Close)
	b.queue = q
	return &inboundFixture{b: b, fake: fake, items: items}
}

// next returns the next queued item, failing after a timeout.
func (f *inboundFixture) next(t *testing.T) queue.Item {
	t.Helper()
	select {
	case it := <-f.items:
		return it
	case <-time.After(3 * time.Second):
		t.Fatal("nothing was queued")
		return queue.Item{}
	}
}

// none fails if anything was queued within a short wait.
func (f *inboundFixture) none(t *testing.T) {
	t.Helper()
	select {
	case it := <-f.items:
		t.Fatalf("unexpected queued item: %+v", it)
	case <-time.After(100 * time.Millisecond):
	}
}

func (f *inboundFixture) handle(m *models.Message) {
	f.b.defaultHandler(context.Background(), nil, &models.Update{Message: m})
}

// fakeWhisper is an OpenAI transcription endpoint answering text and
// recording the uploaded file names.
type fakeWhisper struct {
	mu    sync.Mutex
	names []string
}

func newFakeWhisper(t *testing.T, b *Bot, text string) *fakeWhisper {
	t.Helper()
	w := &fakeWhisper{}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			if fhs := r.MultipartForm.File["file"]; len(fhs) > 0 {
				w.mu.Lock()
				w.names = append(w.names, fhs[0].Filename)
				w.mu.Unlock()
			}
		}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(rw, `{"text":"`+text+`"}`)
	}))
	t.Cleanup(srv.Close)
	b.whisperURL = srv.URL
	t.Setenv("OPENAI_API_KEY", "test-key")
	return w
}

func (w *fakeWhisper) uploaded() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.names...)
}

func albumPart(id int, fileID, caption string) *models.Message {
	m := threadedMsg(7, "")
	m.ID = id
	m.MediaGroupID = "alb1"
	m.Caption = caption
	m.Photo = []models.PhotoSize{{FileID: fileID + "-small"}, {FileID: fileID, Width: 2, Height: 2}}
	return m
}

func TestAlbum_ThreePhotosOneTurn(t *testing.T) {
	f := newInboundFixture(t, nil)
	clock := newManualClock()
	f.b.albumClock = clock

	f.handle(albumPart(11, "p1", "compare these"))
	clock.Advance(600 * time.Millisecond)
	f.handle(albumPart(13, "p3", ""))
	clock.Advance(600 * time.Millisecond) // 1.2 s since the first part
	f.handle(albumPart(12, "p2", ""))
	clock.Advance(999 * time.Millisecond)
	f.none(t) // each part restarts the 1 s window

	clock.Advance(time.Millisecond)
	it := f.next(t)
	f.none(t) // one turn for the whole album

	if it.MessageID != 11 || it.ThreadID != 7 {
		t.Errorf("item message/thread = %d/%d, want 11/7", it.MessageID, it.ThreadID)
	}
	if !strings.HasPrefix(it.Text, "compare these") {
		t.Errorf("text does not start with the caption: %q", it.Text)
	}
	if n := strings.Count(it.Text, "/workspace/chat/uploads/"); n != 3 {
		t.Errorf("text names %d uploads, want 3: %q", n, it.Text)
	}
	if it.FilePath == "" || !strings.Contains(it.Text, it.FilePath) {
		t.Errorf("FilePath %q is not one of the listed uploads", it.FilePath)
	}
	// In message order, each the largest size.
	var got []string
	for _, c := range f.fake.callsTo("getFile") {
		got = append(got, c.Form["file_id"])
	}
	if strings.Join(got, ",") != "p1,p2,p3" {
		t.Errorf("downloaded %v, want p1,p2,p3", got)
	}
	// Three distinct files, even when downloaded in the same millisecond.
	seen := map[string]bool{}
	for _, line := range strings.Split(it.Text, "/workspace/chat/uploads/")[1:] {
		name := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ']' || r == ' ' || r == '\n' })[0]
		if seen[name] {
			t.Errorf("upload name %q used twice", name)
		}
		seen[name] = true
	}
}

func TestAlbum_SeparateGroupsSeparateTurns(t *testing.T) {
	f := newInboundFixture(t, nil)
	clock := newManualClock()
	f.b.albumClock = clock

	a := albumPart(11, "p1", "")
	b := albumPart(21, "q1", "")
	b.MediaGroupID = "alb2"
	f.handle(a)
	f.handle(b)
	clock.Advance(time.Second)
	first, second := f.next(t), f.next(t)
	if first.MessageID == second.MessageID {
		t.Errorf("both albums queued as message %d", first.MessageID)
	}
	if !strings.Contains(first.Text, "/workspace/chat/uploads/") {
		t.Errorf("no upload in the prompt without a caption: %q", first.Text)
	}
}

func TestAudioFile_Transcribed(t *testing.T) {
	f := newInboundFixture(t, map[string]string{"a1": "music/file_3.mp3"})
	w := newFakeWhisper(t, f.b, "buy milk")

	m := threadedMsg(0, "")
	m.Audio = &models.Audio{FileID: "a1", FileName: "memo.m4a", Duration: 3}
	f.handle(m)
	it := f.next(t)
	if it.Text != "buy milk" {
		t.Errorf("queued text = %q, want the transcript", it.Text)
	}
	if it.IsVoice {
		t.Error("an audio file must not switch the reply to voice")
	}
	if got := w.uploaded(); len(got) != 1 || got[0] != "memo.m4a" {
		t.Errorf("whisper got %v, want [memo.m4a]", got)
	}

	// A caption stays the prompt; the transcript is attached to it.
	m = threadedMsg(0, "")
	m.ID = 12
	m.Caption = "summarize"
	m.Audio = &models.Audio{FileID: "a1"}
	f.handle(m)
	it = f.next(t)
	if !strings.HasPrefix(it.Text, "summarize") || !strings.Contains(it.Text, "buy milk") {
		t.Errorf("captioned audio text = %q", it.Text)
	}
	if got := w.uploaded(); got[1] != "audio.mp3" {
		t.Errorf("unnamed audio uploaded as %q, want audio.mp3", got[1])
	}
}

func TestVideoNote_Transcribed(t *testing.T) {
	f := newInboundFixture(t, map[string]string{"v1": "video_notes/file_9.mp4"})
	w := newFakeWhisper(t, f.b, "hello there")

	m := threadedMsg(7, "")
	m.VideoNote = &models.VideoNote{FileID: "v1", Duration: 4, Length: 240}
	f.handle(m)
	it := f.next(t)
	if it.Text != "hello there" || it.ThreadID != 7 {
		t.Errorf("queued %q in thread %d, want the transcript in 7", it.Text, it.ThreadID)
	}
	if got := w.uploaded(); len(got) != 1 || got[0] != "video_note.mp4" {
		t.Errorf("whisper got %v, want [video_note.mp4]", got)
	}
}

func TestAudioFile_TranscriptionFailureAnswers(t *testing.T) {
	f := newInboundFixture(t, nil)
	t.Setenv("OPENAI_API_KEY", "")
	m := threadedMsg(7, "")
	m.Audio = &models.Audio{FileID: "a1"}
	f.handle(m)
	f.none(t)
	sends := f.fake.callsTo("sendMessage")
	if len(sends) != 1 || sends[0].Form["message_thread_id"] != "7" {
		t.Fatalf("want one failure reply in thread 7, got %+v", sends)
	}
}

func TestSticker_Ignored(t *testing.T) {
	f := newInboundFixture(t, nil)
	m := threadedMsg(0, "")
	m.Sticker = &models.Sticker{FileID: "s1", Emoji: "👍"}
	m.ReplyToMessage = &models.Message{ID: 3, From: &models.User{ID: 5, FirstName: "Ann"}, Text: "deal?"}
	f.handle(m)
	f.none(t)
	for _, method := range []string{"sendMessage", "setMessageReaction", "getFile", "sendMessageDraft"} {
		if n := len(f.fake.callsTo(method)); n != 0 {
			t.Errorf("%s called %d times for a sticker", method, n)
		}
	}
}

func TestReply_QueuedWithContextHeader(t *testing.T) {
	f := newInboundFixture(t, nil)
	m := threadedMsg(0, "explain this")
	m.ReplyToMessage = &models.Message{ID: 3, From: &models.User{ID: 99, FirstName: "gopod", IsBot: true}, Text: "Use a mutex."}
	f.handle(m)
	it := f.next(t)
	want := "[in reply to gopod (bot): \"Use a mutex.\"]\n\nexplain this"
	if it.Text != want {
		t.Errorf("queued text = %q, want %q", it.Text, want)
	}
}

func TestSendDocument_ShowsUploadAction(t *testing.T) {
	fake := &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
		if method == "sendDocument" {
			return sentMessage("556")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
	b := newFakeBot(t, fake)
	if _, err := b.sendDocument(context.Background(), streamChat, 7, "a.txt", []byte("x"), ""); err != nil {
		t.Fatalf("sendDocument: %v", err)
	}
	acts := fake.callsTo("sendChatAction")
	if len(acts) != 1 || acts[0].Form["action"] != string(models.ChatActionUploadDocument) || acts[0].Form["message_thread_id"] != "7" {
		t.Errorf("chat actions = %+v, want one upload_document in thread 7", acts)
	}
}
