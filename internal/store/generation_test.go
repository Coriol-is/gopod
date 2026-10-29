package store

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestBeginGenerationIsOncePerKey(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	g, created, err := f.st.BeginGeneration(ctx, f.conv.Scope, GenerationVoice, "k1", "fake-tts")
	if err != nil || !created || g.State != GenerationGenerating {
		t.Fatalf("first = %+v created=%v err=%v", g, created, err)
	}
	g2, created, err := f.st.BeginGeneration(ctx, f.conv.Scope, GenerationVoice, "k1", "fake-tts")
	if err != nil || created || g2.ID != g.ID {
		t.Fatalf("second = %+v created=%v err=%v", g2, created, err)
	}
	if _, created, _ = f.st.BeginGeneration(ctx, f.conv.Scope, GenerationVoice, "k2", "fake-tts"); !created {
		t.Fatal("a new key is a new intent")
	}
}

func TestBeginGenerationConcurrentCallersOneWinner(t *testing.T) {
	f := newDeliveryFixture(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, err := f.st.BeginGeneration(t.Context(), f.conv.Scope, GenerationVoice, "same", "b")
			if err != nil {
				t.Error(err)
			}
			if created {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners = %d, want 1", winners)
	}
}

func TestGenerationOutcomesAreFinal(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	g, _, _ := f.st.BeginGeneration(ctx, f.conv.Scope, GenerationVoice, "k", "b")
	if err := f.st.MarkGenerationUnknown(ctx, g.ID, "timeout"); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		f.st.CompleteGeneration(ctx, g.ID, "a"),
		f.st.FailGeneration(ctx, g.ID, "x"),
		f.st.MarkGenerationUnknown(ctx, g.ID, "again"),
	} {
		if !errors.Is(err, ErrGenerationState) {
			t.Fatalf("terminal rewrite = %v", err)
		}
	}
	got, _ := f.st.GetGeneration(ctx, g.ID)
	if got.State != GenerationUnknown || got.ReasonCode != "timeout" {
		t.Fatalf("row = %+v", got)
	}
	// An unknown outcome still blocks the same key: no silent second call.
	if _, created, _ := f.st.BeginGeneration(ctx, f.conv.Scope, GenerationVoice, "k", "b"); created {
		t.Fatal("unknown outcome must block the key")
	}
	if err := f.st.FailGeneration(ctx, "missing", "x"); !errors.Is(err, ErrGenerationNotFound) {
		t.Fatalf("missing = %v", err)
	}
}

func TestGenerationValidationAndStaleScope(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	if _, _, err := f.st.BeginGeneration(ctx, f.conv.Scope, "other", "k", "b"); !errors.Is(err, ErrInvalidGeneration) {
		t.Fatalf("kind = %v", err)
	}
	if _, _, err := f.st.BeginGeneration(ctx, f.conv.Scope, GenerationVoice, "", "b"); !errors.Is(err, ErrInvalidGeneration) {
		t.Fatalf("key = %v", err)
	}
	g, _, _ := f.st.BeginGeneration(ctx, f.conv.Scope, GenerationVoice, "k", "b")
	if err := f.st.FailGeneration(ctx, g.ID, "Has Spaces And Secrets"); !errors.Is(err, ErrInvalidGeneration) {
		t.Fatalf("reason = %v", err)
	}
	stale := f.conv.Scope
	stale.NamespaceEpoch += 9
	if _, _, err := f.st.BeginGeneration(ctx, stale, GenerationVoice, "k9", "b"); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("stale = %v", err)
	}
}

func TestSweepGenerationsOnlyTouchesInProgress(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	a, _, _ := f.st.BeginGeneration(ctx, f.conv.Scope, GenerationVoice, "a", "b")
	b, _, _ := f.st.BeginGeneration(ctx, f.conv.Scope, GenerationVoice, "b", "b")
	if err := f.st.FailGeneration(ctx, b.ID, "refused"); err != nil {
		t.Fatal(err)
	}
	if n, err := f.st.SweepGenerations(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("young sweep = %d, %v", n, err)
	}
	if n, err := f.st.SweepGenerations(ctx, 0); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	ga, _ := f.st.GetGeneration(ctx, a.ID)
	gb, _ := f.st.GetGeneration(ctx, b.ID)
	if ga.State != GenerationUnknown || ga.ReasonCode != "interrupted" || gb.State != GenerationFailed {
		t.Fatalf("a=%+v b=%+v", ga, gb)
	}
}

func TestGenerationUploadIsClaimedOnce(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	g, _, _ := f.st.BeginGeneration(ctx, f.conv.Scope, GenerationVoice, "k", "b")
	if err := f.st.ClaimGenerationUpload(ctx, g.ID); !errors.Is(err, ErrGenerationState) {
		t.Fatalf("claim before ready = %v", err)
	}
	if err := f.st.CompleteGeneration(ctx, g.ID, "art"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ClaimGenerationUpload(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ClaimGenerationUpload(ctx, g.ID); !errors.Is(err, ErrUploadClaimed) {
		t.Fatalf("second claim = %v", err)
	}
	if err := f.st.FinishGenerationUpload(ctx, g.ID, UploadSent); err != nil {
		t.Fatal(err)
	}
	if err := f.st.FinishGenerationUpload(ctx, g.ID, UploadUnknown); !errors.Is(err, ErrGenerationState) {
		t.Fatalf("outcome rewrite = %v", err)
	}
	if err := f.st.ClaimGenerationUpload(ctx, g.ID); !errors.Is(err, ErrUploadClaimed) {
		t.Fatalf("claim after sent = %v", err)
	}
	got, _ := f.st.GetGeneration(ctx, g.ID)
	if got.UploadState != UploadSent {
		t.Fatalf("upload state = %s", got.UploadState)
	}
}

func TestSweepTurnsAbandonedUploadClaimIntoUnknown(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	g, _, _ := f.st.BeginGeneration(ctx, f.conv.Scope, GenerationVoice, "k", "b")
	_ = f.st.CompleteGeneration(ctx, g.ID, "art")
	_ = f.st.ClaimGenerationUpload(ctx, g.ID)
	if n, err := f.st.SweepGenerations(ctx, 0); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	got, _ := f.st.GetGeneration(ctx, g.ID)
	if got.UploadState != UploadUnknown {
		t.Fatalf("upload state = %s", got.UploadState)
	}
}
