package telegram

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/artifactfs"
	"github.com/Coriol-is/gopod/internal/store"
)

// fakeSpeech is a scripted backend. There is no network anywhere in these tests.
type fakeSpeech struct {
	name   string
	calls  atomic.Int32
	mu     sync.Mutex
	creds  []string
	texts  []string
	script func(ctx context.Context, n int, text string) ([]byte, error)
}

func (f *fakeSpeech) Name() string {
	if f.name == "" {
		return "fake-tts"
	}
	return f.name
}

func (f *fakeSpeech) Synthesize(ctx context.Context, credential, text string) ([]byte, error) {
	n := int(f.calls.Add(1))
	f.mu.Lock()
	f.creds = append(f.creds, credential)
	f.texts = append(f.texts, text)
	f.mu.Unlock()
	if f.script != nil {
		return f.script(ctx, n, text)
	}
	return []byte("OggS-" + text), nil
}

type countingCreds struct {
	reads atomic.Int32
	value string
}

func (c *countingCreds) Credential(ref string) string {
	c.reads.Add(1)
	if ref == "ref-1" {
		return c.value
	}
	return ""
}

type speechFixture struct {
	t        *testing.T
	st       *store.Store
	blobs    *artifactfs.Blobs
	backend  *fakeSpeech
	creds    *countingCreds
	prep     *SpeechPreparer
	personal store.ConversationScope
	group    store.ConversationScope
}

func newSpeechFixture(t *testing.T) *speechFixture {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, c := range []store.RegisteredChat{
		{JID: "tg:1001", Folder: "owner", IsOwner: true, AddedAt: 1},
		{JID: "tg:-100200", Folder: "grp", AddedAt: 1},
	} {
		if err = st.RegisterChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	personal, err := st.SnapshotConversation(ctx, "tg:1001", 0, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.SnapshotConversation(ctx, "tg:-100200", 0, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := artifactfs.OpenBlobs(filepath.Join(resolvedDir(t), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { blobs.Close() })
	f := &speechFixture{t: t, st: st, blobs: blobs, backend: &fakeSpeech{}, creds: &countingCreds{value: "secret-credential-value"}, personal: personal, group: group}
	f.prep = &SpeechPreparer{
		Store: st, Blobs: blobs, Backend: f.backend, Credentials: f.creds,
		Policy:       SpeechPolicy{Grants: []SpeechGrant{{JID: "tg:1001", Backend: "fake-tts", CredentialRef: "ref-1", MaxChars: 100, BilledCostAccepted: true}}},
		MaxFileBytes: store.DefaultMaxArtifactFileBytes,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return f
}

func (f *speechFixture) req(key, text string) SpeechRequest {
	return SpeechRequest{Conversation: f.personal, IntentKey: key, Text: text}
}

func (f *speechFixture) generation(key string) store.Generation {
	f.t.Helper()
	g, created, err := f.st.BeginGeneration(f.t.Context(), f.personal.Scope, store.GenerationVoice, key, "fake-tts")
	if err != nil || created {
		f.t.Fatalf("expected an existing generation for %q: %+v created=%v err=%v", key, g, created, err)
	}
	return g
}

func TestSpeechPrepareStoresExactBytesAsReadyArtifact(t *testing.T) {
	f := newSpeechFixture(t)
	got, err := f.prep.Prepare(t.Context(), f.req("k1", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Reused || got.Ref != store.ArtifactRef(got.ArtifactID) {
		t.Fatalf("prepared = %+v", got)
	}
	art, err := f.st.GetArtifact(t.Context(), got.ArtifactID)
	if err != nil || art.State != store.ArtifactReady || art.MediaType != "audio/ogg" || art.Size != int64(len("OggS-hello")) {
		t.Fatalf("artifact = %+v, %v", art, err)
	}
	data, err := f.blobs.ReadVerified(art.BlobKey, art.Size, art.SHA256, 1<<20)
	if err != nil || string(data) != "OggS-hello" {
		t.Fatalf("blob = %q, %v", data, err)
	}
	if g := f.generation("k1"); g.State != store.GenerationReady || g.ArtifactID != got.ArtifactID {
		t.Fatalf("ledger = %+v", g)
	}
	if f.backend.calls.Load() != 1 || f.backend.creds[0] != "secret-credential-value" {
		t.Fatalf("backend calls = %d", f.backend.calls.Load())
	}
}

func TestSpeechUnauthorizedIsRejectedBeforeAnyEffect(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *speechFixture, r *SpeechRequest)
	}{
		{"group scope", func(f *speechFixture, r *SpeechRequest) { r.Conversation = f.group }},
		{"no grants", func(f *speechFixture, _ *SpeechRequest) { f.prep.Policy = SpeechPolicy{} }},
		{"other backend", func(f *speechFixture, _ *SpeechRequest) { f.backend.name = "other-tts" }},
		{"over the cost bound", func(_ *speechFixture, r *SpeechRequest) { r.Text = string(bytes.Repeat([]byte("x"), 101)) }},
		{"billed cost not accepted", func(f *speechFixture, _ *SpeechRequest) { f.prep.Policy.Grants[0].BilledCostAccepted = false }},
		{"no credential reference", func(f *speechFixture, _ *SpeechRequest) { f.prep.Policy.Grants[0].CredentialRef = "" }},
		{"zero cost bound", func(f *speechFixture, _ *SpeechRequest) { f.prep.Policy.Grants[0].MaxChars = 0 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newSpeechFixture(t)
			r := f.req("k", "hello")
			c.mutate(f, &r)
			_, err := f.prep.Prepare(t.Context(), r)
			if !errors.Is(err, ErrSpeechUnauthorized) {
				t.Fatalf("err = %v, want unauthorized", err)
			}
			if f.backend.calls.Load() != 0 {
				t.Fatal("the backend was called")
			}
			if f.creds.reads.Load() != 0 {
				t.Fatal("the credential was read before authorization")
			}
			if ns, _, _ := f.st.ArtifactUsage(t.Context(), f.personal.Scope.NamespaceID); ns != 0 {
				t.Fatalf("reserved %d bytes", ns)
			}
			if _, created, _ := f.st.BeginGeneration(t.Context(), f.personal.Scope, store.GenerationVoice, "k", "fake-tts"); !created {
				t.Fatal("an unauthorized request left a ledger row")
			}
		})
	}
}

func TestSpeechMissingCredentialIsSilentAndCostsNothing(t *testing.T) {
	f := newSpeechFixture(t)
	f.creds.value = ""
	if _, err := f.prep.Prepare(t.Context(), f.req("k", "hello")); !errors.Is(err, ErrSpeechNoCredential) {
		t.Fatalf("err = %v", err)
	}
	if f.backend.calls.Load() != 0 {
		t.Fatal("the backend was called")
	}
}

func TestSpeechAmbiguousOutcomesAreNeverRetried(t *testing.T) {
	cases := map[string]func(ctx context.Context, n int, text string) ([]byte, error){
		"transport error": func(context.Context, int, string) ([]byte, error) { return nil, errors.New("connection reset") },
		"server error":    func(context.Context, int, string) ([]byte, error) { return nil, &ttsStatusError{Status: 502} },
		"empty result":    func(context.Context, int, string) ([]byte, error) { return nil, nil },
		"oversized": func(context.Context, int, string) ([]byte, error) {
			return make([]byte, 9<<20), nil
		},
		"timeout": func(ctx context.Context, _ int, _ string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSpeechFixture(t)
			f.backend.script = script
			f.prep.CallTimeout = 50 * time.Millisecond
			_, err := f.prep.Prepare(t.Context(), f.req("k", "hello"))
			if !errors.Is(err, ErrPreparationUnknown) {
				t.Fatalf("err = %v, want preparation_unknown", err)
			}
			for i := 0; i < 3; i++ {
				if _, err = f.prep.Prepare(t.Context(), f.req("k", "hello")); !errors.Is(err, ErrPreparationUnknown) {
					t.Fatalf("repeat err = %v", err)
				}
			}
			if n := f.backend.calls.Load(); n != 1 {
				t.Fatalf("backend called %d times, want exactly 1", n)
			}
			if g := f.generation("k"); g.State != store.GenerationUnknown {
				t.Fatalf("ledger = %+v", g)
			}
			// A new, explicit intent is a new key and may call again.
			f.backend.script = nil
			if _, err = f.prep.Prepare(t.Context(), f.req("k-new", "hello")); err != nil {
				t.Fatalf("new intent: %v", err)
			}
		})
	}
}

func TestSpeechCancelBeforeTheCallNeverCalls(t *testing.T) {
	f := newSpeechFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.prep.Prepare(ctx, f.req("k", "hello")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if f.backend.calls.Load() != 0 {
		t.Fatal("the backend was called after cancellation")
	}
}

func TestSpeechCancelDuringTheCallIsUnknownAndRecorded(t *testing.T) {
	f := newSpeechFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	f.backend.script = func(c context.Context, _ int, _ string) ([]byte, error) {
		cancel()
		<-c.Done()
		return nil, c.Err()
	}
	_, err := f.prep.Prepare(ctx, f.req("k", "hello"))
	if !errors.Is(err, ErrPreparationUnknown) {
		t.Fatalf("err = %v", err)
	}
	// The outcome is persisted although the attempt context ended.
	if g := f.generation("k"); g.State != store.GenerationUnknown || g.ReasonCode != "canceled_during_call" {
		t.Fatalf("ledger = %+v", g)
	}
	f.backend.script = nil
	if _, err = f.prep.Prepare(t.Context(), f.req("k", "hello")); !errors.Is(err, ErrPreparationUnknown) {
		t.Fatalf("repeat = %v", err)
	}
	if f.backend.calls.Load() != 1 {
		t.Fatalf("calls = %d", f.backend.calls.Load())
	}
}

func TestSpeechExplicitRefusalFailsTheIntentWithoutRebill(t *testing.T) {
	f := newSpeechFixture(t)
	f.backend.script = func(context.Context, int, string) ([]byte, error) {
		return nil, &SpeechRefusal{Code: "backend_quota"}
	}
	_, err := f.prep.Prepare(t.Context(), f.req("k", "hello"))
	if !errors.Is(err, ErrSpeechRefused) {
		t.Fatalf("err = %v", err)
	}
	if g := f.generation("k"); g.State != store.GenerationFailed || g.ReasonCode != "backend_quota" {
		t.Fatalf("ledger = %+v", g)
	}
	// Same key: no new call; the failure is reported again.
	if _, err = f.prep.Prepare(t.Context(), f.req("k", "hello")); !errors.Is(err, ErrGenerationFailed) {
		t.Fatalf("repeat = %v", err)
	}
	if f.backend.calls.Load() != 1 {
		t.Fatalf("calls = %d", f.backend.calls.Load())
	}
}

func TestSpeechRefusalWithHostileCodeIsTreatedAsAmbiguous(t *testing.T) {
	f := newSpeechFixture(t)
	f.backend.script = func(context.Context, int, string) ([]byte, error) {
		return nil, &SpeechRefusal{Code: "Bearer secret-credential-value"}
	}
	if _, err := f.prep.Prepare(t.Context(), f.req("k", "hello")); !errors.Is(err, ErrPreparationUnknown) {
		t.Fatalf("err = %v", err)
	}
	if g := f.generation("k"); g.ReasonCode == "Bearer secret-credential-value" {
		t.Fatal("a raw backend string reached the ledger")
	}
}

func TestSpeechStorageQuotaFailsBeforeAnyCharge(t *testing.T) {
	f := newSpeechFixture(t)
	if err := f.st.SetArtifactLimits(store.ArtifactLimits{MaxNamespaceBytes: 1024}); err != nil {
		t.Fatal(err)
	}
	_, err := f.prep.Prepare(t.Context(), f.req("k", "hello"))
	if !errors.Is(err, store.ErrArtifactQuotaExceeded) {
		t.Fatalf("err = %v", err)
	}
	if f.backend.calls.Load() != 0 {
		t.Fatal("the backend was called although storage was unavailable")
	}
	if _, created, _ := f.st.BeginGeneration(t.Context(), f.personal.Scope, store.GenerationVoice, "k", "fake-tts"); !created {
		t.Fatal("a quota failure must not consume the intent")
	}
}

func TestSpeechSnapshotFailureAfterTheCallIsUnknownNotRetried(t *testing.T) {
	f := newSpeechFixture(t)
	f.blobs.Fault = func(p string) error {
		if p == "after_file_fsync" {
			return errors.New("disk fault")
		}
		return nil
	}
	if _, err := f.prep.Prepare(t.Context(), f.req("k", "hello")); !errors.Is(err, ErrPreparationUnknown) {
		t.Fatalf("err = %v", err)
	}
	f.blobs.Fault = nil
	if _, err := f.prep.Prepare(t.Context(), f.req("k", "hello")); !errors.Is(err, ErrPreparationUnknown) {
		t.Fatalf("repeat = %v", err)
	}
	if f.backend.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", f.backend.calls.Load())
	}
}

func TestSpeechConcurrentSameKeyCallsTheBackendOnce(t *testing.T) {
	f := newSpeechFixture(t)
	release := make(chan struct{})
	f.backend.script = func(context.Context, int, string) ([]byte, error) {
		<-release
		return []byte("OggS"), nil
	}
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.prep.Prepare(t.Context(), f.req("same", "hello"))
		}()
	}
	waitFor(t, "the one call to start", func() bool { return f.backend.calls.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	ok, busy := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			ok++
		case errors.Is(e, ErrSpeechInProgress):
			busy++
		default:
			t.Errorf("unexpected error %v", e)
		}
	}
	if f.backend.calls.Load() != 1 || ok < 1 {
		t.Fatalf("calls=%d ok=%d busy=%d", f.backend.calls.Load(), ok, busy)
	}
}

func TestSpeechReadyIntentIsReusedWithoutACall(t *testing.T) {
	f := newSpeechFixture(t)
	first, err := f.prep.Prepare(t.Context(), f.req("k", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.prep.Prepare(t.Context(), f.req("k", "hello"))
	if err != nil || !again.Reused || again.ArtifactID != first.ArtifactID {
		t.Fatalf("again = %+v, %v", again, err)
	}
	if f.backend.calls.Load() != 1 {
		t.Fatalf("calls = %d", f.backend.calls.Load())
	}
}

func TestSpeechRemovedClipIsNeverRegenerated(t *testing.T) {
	f := newSpeechFixture(t)
	first, err := f.prep.Prepare(t.Context(), f.req("k", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.AbandonPreparation(t.Context(), first.PreparationID, "expired_test"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.prep.Prepare(t.Context(), f.req("k", "hello")); !errors.Is(err, ErrAudioUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if f.backend.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (no rebill)", f.backend.calls.Load())
	}
}

func TestSpeechRestartTurnsInterruptedCallIntoUnknown(t *testing.T) {
	f := newSpeechFixture(t)
	// A crash after the intent was recorded and before the outcome.
	if _, created, err := f.st.BeginGeneration(t.Context(), f.personal.Scope, store.GenerationVoice, "k", "fake-tts"); err != nil || !created {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if n, err := f.st.SweepGenerations(t.Context(), 0); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	if _, err := f.prep.Prepare(t.Context(), f.req("k", "hello")); !errors.Is(err, ErrPreparationUnknown) {
		t.Fatalf("err = %v", err)
	}
	if f.backend.calls.Load() != 0 {
		t.Fatal("restart re-ran a paid call")
	}
}

func TestOpenAISpeechClassifiesFailures(t *testing.T) {
	cases := []struct {
		status int
		code   string // "" means ambiguous
	}{{401, "backend_credentials"}, {403, "backend_credentials"}, {429, "backend_quota"}, {400, "backend_rejected"}, {500, ""}, {502, ""}}
	for _, c := range cases {
		err := classifyTTSError(&ttsStatusError{Status: c.status})
		var ref *SpeechRefusal
		got := ""
		if errors.As(err, &ref) {
			got = ref.Code
		}
		if got != c.code {
			t.Errorf("status %d: code %q, want %q", c.status, got, c.code)
		}
	}
}
