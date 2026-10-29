package telegram

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Coriol-is/gopod/internal/artifactfs"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
)

// recSpeech adapts the legacy-call recorder to the SpeechBackend interface so
// the parity table runs unchanged against the prepared path.
type recSpeech struct{ rec *ttsRecorder }

func (recSpeech) Name() string { return "fake-tts" }
func (r recSpeech) Synthesize(ctx context.Context, credential, text string) ([]byte, error) {
	return r.rec.call(ctx, credential, text)
}

// enablePrepared switches b to immutable preparation with a fake speech backend
// and the real environment-backed credential resolver (the personal key).
func enablePrepared(t *testing.T, b *Bot, backend SpeechBackend) *PreparedDelivery {
	t.Helper()
	ctx := context.Background()
	for _, c := range []store.RegisteredChat{{JID: "tg:1001", Folder: "owner", IsOwner: true, AddedAt: 1}, {JID: "tg:-100200", Folder: "grp", AddedAt: 1}} {
		if err := b.store.RegisterChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	blobs, err := artifactfs.OpenBlobs(filepath.Join(resolvedDir(t), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { blobs.Close() })
	lim := b.store.ArtifactLimits()
	pd := &PreparedDelivery{
		Scopes: b.store, Artifacts: b.store, Blobs: blobs, Uploads: b.store, MaxFileBytes: lim.MaxFileBytes,
		Speech: &SpeechPreparer{
			Store: b.store, Blobs: blobs, Backend: backend, Credentials: NewEnvSpeechCredentials(),
			Policy:       SpeechPolicy{Grants: []SpeechGrant{{JID: "tg:1001", Backend: backend.Name(), CredentialRef: PersonalSpeechCredential, MaxChars: 4000, BilledCostAccepted: true}}},
			MaxFileBytes: lim.MaxFileBytes,
			Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
	b.EnablePreparedDelivery(pd)
	return pd
}

// The prepared path must select, speak and send exactly like the legacy path.
func TestPreparedVoiceKeepsLegacySelectionSemantics(t *testing.T) {
	for _, c := range voiceCases() {
		t.Run(c.name, func(t *testing.T) {
			runVoiceCase(t, c, func(t *testing.T, b *Bot, rec *ttsRecorder) {
				b.tts = func(context.Context, string, string) ([]byte, error) {
					t.Error("the legacy TTS call ran on the prepared path")
					return nil, nil
				}
				enablePrepared(t, b, recSpeech{rec})
			})
		})
	}
}

func TestPreparedVoiceNeverUploadsTwiceForOneTurn(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	SetReplyMode(streamChat, "voice")
	t.Cleanup(func() { SetReplyMode(streamChat, "") })
	fake := voiceFake(false)
	b, item := newStreamFixture(t, fake)
	rec := &ttsRecorder{audio: []byte("OggS-audio")}
	pd := enablePrepared(t, b, recSpeech{rec})
	// The same turn is delivered three times (a queue retry, a duplicate hook).
	for i := 0; i < 3; i++ {
		b.maybeVoiceReply(context.Background(), item, "Hello there")
	}
	if rec.calls() != 1 || len(fake.callsTo("sendVoice")) != 1 {
		t.Fatalf("tts=%d sendVoice=%d, want 1 and 1", rec.calls(), len(fake.callsTo("sendVoice")))
	}
	conv, _ := b.personalConversation(context.Background(), item.ChatID, item.ThreadID)
	g, created, err := b.store.BeginGeneration(context.Background(), conv.Scope, store.GenerationVoice, voiceIntentKey(item, "Hello there"), "fake-tts")
	if err != nil || created || g.UploadState != store.UploadSent {
		t.Fatalf("ledger = %+v created=%v err=%v", g, created, err)
	}
	_ = pd
}

func TestPreparedVoiceAmbiguousUploadIsRecordedAndNotRepeated(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	SetReplyMode(streamChat, "voice")
	t.Cleanup(func() { SetReplyMode(streamChat, "") })
	fake := voiceFake(false)
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendVoice" {
			return 502, `{"ok":false,"error_code":502,"description":"Bad Gateway"}`
		}
		return 200, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake)
	rec := &ttsRecorder{audio: []byte("OggS-audio")}
	enablePrepared(t, b, recSpeech{rec})
	b.maybeVoiceReply(context.Background(), item, "Hello there")
	b.maybeVoiceReply(context.Background(), item, "Hello there")
	if len(fake.callsTo("sendVoice")) != 1 || rec.calls() != 1 {
		t.Fatalf("sendVoice=%d tts=%d, want 1 and 1", len(fake.callsTo("sendVoice")), rec.calls())
	}
	conv, _ := b.personalConversation(context.Background(), item.ChatID, item.ThreadID)
	g, _, _ := b.store.BeginGeneration(context.Background(), conv.Scope, store.GenerationVoice, voiceIntentKey(item, "Hello there"), "fake-tts")
	if g.UploadState != store.UploadUnknown {
		t.Fatalf("upload state = %q, want unknown", g.UploadState)
	}
}

func TestPreparedVoiceAmbiguousTTSIsNotRetriedByASecondDelivery(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	SetReplyMode(streamChat, "voice")
	t.Cleanup(func() { SetReplyMode(streamChat, "") })
	fake := voiceFake(false)
	b, item := newStreamFixture(t, fake)
	rec := &ttsRecorder{err: context.DeadlineExceeded}
	enablePrepared(t, b, recSpeech{rec})
	b.maybeVoiceReply(context.Background(), item, "Hello there")
	b.maybeVoiceReply(context.Background(), item, "Hello there")
	if rec.calls() != 1 || len(fake.callsTo("sendVoice")) != 0 {
		t.Fatalf("tts=%d sendVoice=%d, want 1 and 0", rec.calls(), len(fake.callsTo("sendVoice")))
	}
}

func TestPreparedVoiceStorageQuotaSkipsTheCallButKeepsTheReply(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	SetReplyMode(streamChat, "voice")
	t.Cleanup(func() { SetReplyMode(streamChat, "") })
	fake := voiceFake(false)
	b, item := newStreamFixture(t, fake)
	rec := &ttsRecorder{audio: []byte("OggS")}
	enablePrepared(t, b, recSpeech{rec})
	if err := b.store.SetArtifactLimits(store.ArtifactLimits{MaxNamespaceBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	b.maybeVoiceReply(context.Background(), item, "Hello there")
	if rec.calls() != 0 || len(fake.callsTo("sendVoice")) != 0 {
		t.Fatalf("tts=%d sendVoice=%d, want 0 and 0", rec.calls(), len(fake.callsTo("sendVoice")))
	}
}

func TestPreparedVoiceOtherBackendGrantIsRejectedBeforeTheCall(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k")
	SetReplyMode(streamChat, "voice")
	t.Cleanup(func() { SetReplyMode(streamChat, "") })
	fake := voiceFake(false)
	b, item := newStreamFixture(t, fake)
	rec := &ttsRecorder{audio: []byte("OggS")}
	pd := enablePrepared(t, b, recSpeech{rec})
	pd.Speech.Policy.Grants[0].Backend = "some-other-backend" // no provider fallback
	b.maybeVoiceReply(context.Background(), item, "Hello there")
	if rec.calls() != 0 || len(fake.callsTo("sendVoice")) != 0 {
		t.Fatalf("tts=%d sendVoice=%d", rec.calls(), len(fake.callsTo("sendVoice")))
	}
}

// ---- generated code documents ----

func codeReply() string { return "Here it is:\n" + fence("go", 100) + "\nDone." }

// legacyDoc runs attachLongCode with preparation off and returns what reached
// Telegram and the text that was left.
func legacyDoc(t *testing.T, reply string) (shown string, name string, content string, caption string) {
	t.Helper()
	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	shown = b.attachLongCode(context.Background(), item.ChatID, item.ThreadID, reply)
	docs := fake.callsTo("sendDocument")
	if len(docs) != 1 {
		t.Fatalf("legacy sendDocument calls = %d", len(docs))
	}
	f := docs[0].Form
	return shown, f["document.filename"], f["document.content"], f["caption"]
}

func TestPreparedCodeDocumentBytesAreIdenticalToLegacy(t *testing.T) {
	wantShown, wantName, wantContent, wantCaption := legacyDoc(t, codeReply())

	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	pd := enablePrepared(t, b, recSpeech{&ttsRecorder{}})
	shown := b.attachLongCode(context.Background(), item.ChatID, item.ThreadID, codeReply())
	docs := fake.callsTo("sendDocument")
	if len(docs) != 1 {
		t.Fatalf("sendDocument calls = %d", len(docs))
	}
	f := docs[0].Form
	if shown != wantShown || f["document.filename"] != wantName || f["document.content"] != wantContent || f["caption"] != wantCaption {
		t.Fatalf("prepared upload differs from legacy:\n shown %q vs %q\n name %q vs %q\n caption %q vs %q",
			shown, wantShown, f["document.filename"], wantName, f["caption"], wantCaption)
	}
	// The preparation was released after the upload: nothing stays ready or
	// reserved, and the blob is queued for the reconciler to remove.
	if ready, _ := b.store.ReadyArtifacts(context.Background(), "", 10); len(ready) != 0 {
		t.Fatalf("ready artifacts left: %d", len(ready))
	}
	if del, _ := b.store.ListDeletingArtifacts(context.Background(), "", 10); len(del) != 1 {
		t.Fatalf("deleting artifacts = %d, want 1", len(del))
	}
	_ = pd
}

func mustScope(t *testing.T, b *Bot) store.ScopeRef {
	t.Helper()
	conv, err := b.personalConversation(context.Background(), b.ownerChatID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return conv.Scope
}

func TestPreparedCodeDocumentBytesAreStableOnceSnapshotted(t *testing.T) {
	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	pd := enablePrepared(t, b, recSpeech{&ttsRecorder{}})
	d := splitLongCode(codeReply()).docs[0]
	want := d.Body
	data, release, err := b.codeDocBytes(context.Background(), item.ChatID, item.ThreadID, d)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if string(data) != want {
		t.Fatal("snapshot bytes differ from the rendered block")
	}
	// Tampering with the stored blob after preparation is caught, never uploaded.
	arts, err := pd.Artifacts.(*store.Store).ReadyArtifacts(context.Background(), "", 10)
	if err != nil || len(arts) != 1 {
		t.Fatalf("ready artifacts = %v, %v", arts, err)
	}
	blob := filepath.Join(pd.Blobs.(*artifactfs.Blobs).RootForTest(), arts[0].BlobKey)
	if err = os.WriteFile(blob, []byte(strings.Repeat("x", int(arts[0].Size))), 0o600); err != nil {
		t.Fatal(err)
	}
	art := byteArtifacts{st: pd.Artifacts, blobs: pd.Blobs, maxBytes: pd.MaxFileBytes}
	if _, err = art.blobs.ReadVerified(arts[0].BlobKey, arts[0].Size, arts[0].SHA256, art.maxBytes); err == nil {
		t.Fatal("tampered snapshot verified")
	}
}

func TestPreparedCodeDocumentFailureKeepsTheBlockInline(t *testing.T) {
	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	enablePrepared(t, b, recSpeech{&ttsRecorder{}})
	if err := b.store.SetArtifactLimits(store.ArtifactLimits{MaxNamespaceBytes: 16}); err != nil {
		t.Fatal(err)
	}
	reply := codeReply()
	shown := b.attachLongCode(context.Background(), item.ChatID, item.ThreadID, reply)
	if len(fake.callsTo("sendDocument")) != 0 || shown != reply {
		t.Fatalf("docs=%d shown differs=%v", len(fake.callsTo("sendDocument")), shown != reply)
	}
}

func TestPreparedCodeDocumentLeavesGroupsOnTheLegacyPath(t *testing.T) {
	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	enablePrepared(t, b, recSpeech{&ttsRecorder{}})
	b.attachLongCode(context.Background(), -100200, 0, codeReply())
	if len(fake.callsTo("sendDocument")) != 1 {
		t.Fatalf("group doc calls = %d", len(fake.callsTo("sendDocument")))
	}
	if ns, global, _ := b.store.ArtifactUsage(context.Background(), mustScope(t, b).NamespaceID); ns != 0 || global != 0 {
		t.Fatalf("a group reply reserved artifact bytes: %d/%d", ns, global)
	}
	_ = item
}

// ---- review follow-ups ----

type failingRead struct{ SnapshotWriter }

func (failingRead) ReadVerified(string, int64, string, int64) ([]byte, error) {
	return nil, io.ErrUnexpectedEOF
}

func voiceBot(t *testing.T, key string) (*Bot, queue.Item, *fakeTelegram, *ttsRecorder, *PreparedDelivery) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", key)
	SetReplyMode(streamChat, "voice")
	t.Cleanup(func() { SetReplyMode(streamChat, "") })
	fake := voiceFake(false)
	b, item := newStreamFixture(t, fake)
	rec := &ttsRecorder{audio: []byte("OggS-audio")}
	return b, item, fake, rec, enablePrepared(t, b, recSpeech{rec})
}

func TestPreparedVoiceUnreadableClipReleasesTheClaimSoARetryCanSend(t *testing.T) {
	b, item, fake, rec, pd := voiceBot(t, "k")
	real := pd.Blobs
	pd.Blobs = failingRead{real}
	b.maybeVoiceReply(context.Background(), item, "Hello there")
	if len(fake.callsTo("sendVoice")) != 0 || rec.calls() != 1 {
		t.Fatalf("sendVoice=%d tts=%d", len(fake.callsTo("sendVoice")), rec.calls())
	}
	conv, _ := b.personalConversation(context.Background(), item.ChatID, item.ThreadID)
	g, _, _ := b.store.BeginGeneration(context.Background(), conv.Scope, store.GenerationVoice, voiceIntentKey(item, "Hello there"), "fake-tts")
	if g.UploadState != store.UploadNone {
		t.Fatalf("upload state = %q, want none (released)", g.UploadState)
	}
	// The retry sends the already-paid clip without a second TTS call.
	pd.Blobs = real
	b.maybeVoiceReply(context.Background(), item, "Hello there")
	if len(fake.callsTo("sendVoice")) != 1 || rec.calls() != 1 {
		t.Fatalf("retry: sendVoice=%d tts=%d, want 1 and 1", len(fake.callsTo("sendVoice")), rec.calls())
	}
}

func TestPreparedVoiceShowsTheRecordingIndicatorOnlyWhenACallStarts(t *testing.T) {
	b, item, fake, _, pd := voiceBot(t, "")
	b.maybeVoiceReply(context.Background(), item, "Hello there") // no credential
	if n := len(fake.callsTo("sendChatAction")); n != 0 {
		t.Fatalf("indicator without a credential: %d", n)
	}
	pd.Speech.Policy.Grants[0].MaxChars = 1 // unauthorized (cost bound)
	t.Setenv("OPENAI_API_KEY", "k")
	b.maybeVoiceReply(context.Background(), item, "Hello there")
	if n := len(fake.callsTo("sendChatAction")); n != 0 {
		t.Fatalf("indicator when unauthorized: %d", n)
	}
	pd.Speech.Policy.Grants[0].MaxChars = 4000
	b.maybeVoiceReply(context.Background(), item, "Hello there")
	if n := len(fake.callsTo("sendChatAction")); n != 1 {
		t.Fatalf("indicator on a real call: %d", n)
	}
	// A repeat finds the recorded intent: no call, no indicator.
	b.maybeVoiceReply(context.Background(), item, "Hello there")
	if n := len(fake.callsTo("sendChatAction")); n != 1 {
		t.Fatalf("indicator on a repeat: %d", n)
	}
}

func TestPreparedVoiceSpeaksLongRepliesTruncatedLikeLegacy(t *testing.T) {
	b, item, fake, rec, _ := voiceBot(t, "k")
	long := strings.Repeat("a", 5000)
	b.maybeVoiceReply(context.Background(), item, long)
	if rec.calls() != 1 || len(rec.texts[0]) != 4000 || len(fake.callsTo("sendVoice")) != 1 {
		t.Fatalf("tts=%d len=%d voice=%d", rec.calls(), len(rec.texts[0]), len(fake.callsTo("sendVoice")))
	}
}

func TestSpeechTruncationSemantics(t *testing.T) {
	ascii := strings.Repeat("a", 5000)
	if got := legacySpeechInput(ascii); len(got) != 4000 || truncateSpeech(ascii) != got {
		t.Fatal("ascii truncation differs")
	}
	short := "héllo"
	if legacySpeechInput(short) != short || truncateSpeech(short) != short {
		t.Fatal("short text changed")
	}
	euro := strings.Repeat("€", 2000) // 6000 bytes; byte 4000 falls inside a character
	if l := legacySpeechInput(euro); len(l) != 4000 || utf8.ValidString(l) {
		t.Fatalf("legacy cut changed: len=%d valid=%v", len(l), utf8.ValidString(l))
	}
	if p := truncateSpeech(euro); len(p) != 3999 || !utf8.ValidString(p) || !strings.HasPrefix(legacySpeechInput(euro), p) {
		t.Fatalf("prepared cut len=%d valid=%v", len(p), utf8.ValidString(p))
	}
}
