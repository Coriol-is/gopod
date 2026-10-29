package telegram

// Text-to-speech preparation (CP-003 PR 5).
//
// A synthesis call is billed money and its result can be lost after the money
// is spent. This file is the host's guard around it:
//
//   - Authorization comes first and is explicit: scope, backend, credential
//     reference and cost bound are named by a grant, never inferred from the
//     environment. An unauthorized request fails before the credential is read,
//     before any reservation and before any backend call.
//   - The intent is written to the generated-artifact ledger before the call.
//     An ambiguous outcome (timeout, cancel mid-call, crash, empty or oversized
//     result, a failed write after the bytes arrived) becomes
//     preparation_unknown and blocks the same key forever. It is never retried
//     or re-billed; only a new key, which is a new explicit intent, can call
//     again. Only an explicit refusal that proves no work was done is failed.
//   - There is no provider fallback: the request names one backend and a
//     different one is unauthorized.
//
// Only fake backends are used by tests; nothing here is a live qualification.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/Coriol-is/gopod/internal/store"
)

// Errors a caller can branch on. The text of a backend error never reaches them.
var (
	// ErrSpeechUnauthorized is a request outside every grant (wrong scope,
	// backend, cost or no billed-cost acceptance). Nothing was called.
	ErrSpeechUnauthorized = errors.New("telegram: speech not authorized")
	// ErrSpeechNoCredential is an authorized request whose credential is not configured.
	ErrSpeechNoCredential = errors.New("telegram: speech credential not configured")
	// ErrSpeechRefused is an explicit backend refusal; no work was done and the
	// intent is failed. It carries a SpeechRefusal code.
	ErrSpeechRefused = errors.New("telegram: speech backend refused the request")
	// ErrPreparationUnknown means a paid call may have happened and its result
	// is unusable or unknown. It is never retried automatically.
	ErrPreparationUnknown = errors.New("telegram: speech preparation outcome unknown")
	// ErrSpeechInProgress is a second caller for an intent whose call is running.
	ErrSpeechInProgress = errors.New("telegram: speech generation already in progress")
	// ErrGenerationFailed is a repeat of an intent that already failed; a new
	// explicit intent needs a new key.
	ErrGenerationFailed = errors.New("telegram: speech intent already failed")
	// ErrAudioUnavailable is a ready generation whose artifact was since removed.
	// The audio is not regenerated.
	ErrAudioUnavailable = errors.New("telegram: generated audio no longer available")
)

// SpeechRefusal is returned by a backend for an explicit rejection that proves
// the request did no work (bad credential, quota exhausted, invalid input).
// Code is a fixed lowercase machine code, for example "backend_quota".
type SpeechRefusal struct{ Code string }

func (e *SpeechRefusal) Error() string { return "speech refused: " + e.Code }

// SpeechBackend synthesizes audio. credential is resolved by the host only
// after authorization; a backend must not read the environment itself.
type SpeechBackend interface {
	// Name is the stable backend label a grant names.
	Name() string
	// Synthesize returns audio bytes. Any error other than *SpeechRefusal is
	// treated as an ambiguous outcome.
	Synthesize(ctx context.Context, credential, text string) ([]byte, error)
}

// SpeechCredentials resolves a credential reference to its secret. An empty
// result means not configured. It is called only after authorization.
type SpeechCredentials interface {
	Credential(ref string) string
}

// SpeechGrant is one explicit authorization: exactly this destination chat,
// exactly this backend, this credential reference and this cost bound.
type SpeechGrant struct {
	// JID is the one destination chat; the thread is not part of the grant
	// (a topic of the same chat is the same person).
	JID string
	// Backend is the only backend that may be called for this destination.
	Backend string
	// CredentialRef names the credential in SpeechCredentials, never its value.
	CredentialRef string
	// MaxChars bounds the characters sent to the backend (the cost bound).
	MaxChars int
	// BilledCostAccepted must be true: the owner accepts that the backend
	// charges for each call. A grant without it authorizes nothing.
	BilledCostAccepted bool
}

// SpeechPolicy authorizes requests against explicit grants. The zero value
// authorizes nothing.
type SpeechPolicy struct{ Grants []SpeechGrant }

// speechAuthorization is what a passed check yields.
type speechAuthorization struct{ CredentialRef string }

// authorize checks destination, backend and cost. It has no side effects.
func (p SpeechPolicy) authorize(conv store.ConversationScope, backend string, chars int) (speechAuthorization, error) {
	for _, g := range p.Grants {
		if g.JID == "" || g.JID != conv.Scope.JID {
			continue
		}
		switch {
		case !g.BilledCostAccepted:
			return speechAuthorization{}, fmt.Errorf("%w: billed cost not accepted", ErrSpeechUnauthorized)
		case g.Backend == "" || g.Backend != backend:
			return speechAuthorization{}, fmt.Errorf("%w: backend", ErrSpeechUnauthorized)
		case g.CredentialRef == "":
			return speechAuthorization{}, fmt.Errorf("%w: credential", ErrSpeechUnauthorized)
		case g.MaxChars <= 0 || chars > g.MaxChars:
			return speechAuthorization{}, fmt.Errorf("%w: cost bound", ErrSpeechUnauthorized)
		}
		return speechAuthorization{CredentialRef: g.CredentialRef}, nil
	}
	return speechAuthorization{}, fmt.Errorf("%w: scope", ErrSpeechUnauthorized)
}

// GenerationLedger is the store surface of the generated-artifact ledger.
type GenerationLedger interface {
	BeginGeneration(ctx context.Context, sc store.ScopeRef, kind, key, backend string) (store.Generation, bool, error)
	CompleteGeneration(ctx context.Context, id, artifactID string) error
	FailGeneration(ctx context.Context, id, reason string) error
	MarkGenerationUnknown(ctx context.Context, id, reason string) error
}

// SpeechStore is everything the preparer needs from the store.
type SpeechStore interface {
	PreparationStore
	GenerationLedger
}

// Defaults for SpeechPreparer.
const (
	defaultSpeechTimeout  = 90 * time.Second
	defaultSpeechReserve  = 8 << 20 // upper bound reserved for one clip
	speechReservationTTL  = time.Hour
	speechMediaType       = "audio/ogg"
	speechFilename        = "reply.ogg"
	speechOwnerRefPrefix  = "tts:"
	maxSpeechReasonLength = 64
)

// SpeechPreparer turns authorized text into one ready immutable audio artifact.
type SpeechPreparer struct {
	Store       SpeechStore
	Blobs       SnapshotWriter
	Backend     SpeechBackend
	Policy      SpeechPolicy
	Credentials SpeechCredentials
	// CallTimeout bounds the backend call (default 90 s).
	CallTimeout time.Duration
	// MaxAudioBytes bounds the clip and the reservation (default 8 MiB,
	// capped by MaxFileBytes).
	MaxAudioBytes int64
	// MaxFileBytes is the store's per-file limit.
	MaxFileBytes int64
	// ReservationTTL is how long a ready, unpublished clip stays reserved
	// (default one hour); after it the clip expires and is never regenerated.
	ReservationTTL time.Duration
	Log            *slog.Logger
}

// SpeechRequest is one intent. IntentKey identifies it durably (for example the
// turn and a hash of the text); it must not contain the text or a secret.
type SpeechRequest struct {
	Conversation store.ConversationScope
	IntentKey    string
	Text         string
}

// PreparedSpeech is a ready clip. Reused is true when an earlier call already
// produced it and nothing was called this time.
type PreparedSpeech struct {
	GenerationID  string
	PreparationID string
	ArtifactID    string
	Ref           string // opaque artifact reference for an upload part
	Size          int64
	SHA256        string
	Reused        bool
	preparation   store.Preparation
}

func (p *SpeechPreparer) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

func (p *SpeechPreparer) bytesPrep() byteArtifacts {
	maxFile := p.MaxFileBytes
	if maxFile <= 0 {
		maxFile = store.DefaultMaxArtifactFileBytes
	}
	return byteArtifacts{st: p.Store, blobs: p.Blobs, maxBytes: maxFile}
}

func (p *SpeechPreparer) audioBound() int64 {
	n := p.MaxAudioBytes
	if n <= 0 {
		n = defaultSpeechReserve
	}
	if p.MaxFileBytes > 0 {
		n = min(n, p.MaxFileBytes)
	}
	return n
}

// Prepare authorizes, records the intent, calls the backend at most once for
// the key and stores the clip as a ready immutable artifact.
func (p *SpeechPreparer) Prepare(ctx context.Context, req SpeechRequest) (PreparedSpeech, error) {
	if req.IntentKey == "" || req.Text == "" || p.Backend == nil {
		return PreparedSpeech{}, fmt.Errorf("%w: malformed request", ErrSpeechUnauthorized)
	}
	// 1. Authorization: no credential read, reservation or call before this.
	auth, err := p.Policy.authorize(req.Conversation, p.Backend.Name(), utf8.RuneCountInString(req.Text))
	if err != nil {
		return PreparedSpeech{}, err
	}
	credential := ""
	if p.Credentials != nil {
		credential = p.Credentials.Credential(auth.CredentialRef)
	}
	if credential == "" {
		return PreparedSpeech{}, ErrSpeechNoCredential
	}
	sc := req.Conversation.Scope
	art := p.bytesPrep()

	// 2. Reserve storage first: a quota failure must surface before any charge.
	ttl := p.ReservationTTL
	if ttl <= 0 {
		ttl = speechReservationTTL
	}
	prep, err := art.open(ctx, sc, speechOwnerRefPrefix+req.IntentKey, p.audioBound(), ttl)
	if err != nil {
		return PreparedSpeech{}, fmt.Errorf("reserve audio storage: %w", err)
	}
	// 3. Record the intent. Only the first caller of a key may call the backend.
	gen, created, err := p.Store.BeginGeneration(ctx, sc, store.GenerationVoice, req.IntentKey, p.Backend.Name())
	if err != nil {
		art.release(ctx, prep.ID, "ledger_unavailable")
		return PreparedSpeech{}, fmt.Errorf("record speech intent: %w", err)
	}
	if !created {
		art.release(ctx, prep.ID, "duplicate_intent")
		return p.existing(ctx, gen)
	}
	if ctx.Err() != nil {
		// Cancelled before the call: provably nothing was spent.
		p.settle(ctx, gen.ID, store.GenerationFailed, "canceled_before_call")
		art.release(ctx, prep.ID, "canceled")
		return PreparedSpeech{}, ctx.Err()
	}

	// 4. The one backend call. No transaction is open.
	timeout := p.CallTimeout
	if timeout <= 0 {
		timeout = defaultSpeechTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	audio, callErr := p.Backend.Synthesize(cctx, credential, req.Text)
	cancel()
	if callErr != nil {
		var refusal *SpeechRefusal
		if errors.As(callErr, &refusal) && generationReasonOK(refusal.Code) {
			p.settle(ctx, gen.ID, store.GenerationFailed, refusal.Code)
			art.release(ctx, prep.ID, "refused")
			return PreparedSpeech{}, fmt.Errorf("%w: %s", ErrSpeechRefused, refusal.Code)
		}
		reason := "ambiguous_result"
		if ctx.Err() != nil {
			reason = "canceled_during_call"
		}
		p.settle(ctx, gen.ID, store.GenerationUnknown, reason)
		art.release(ctx, prep.ID, "unknown")
		p.log().Warn("speech preparation outcome unknown; will not retry", slog.String("reason", reason), slog.String("generation", gen.ID))
		return PreparedSpeech{}, fmt.Errorf("%w: %s", ErrPreparationUnknown, reason)
	}
	if len(audio) == 0 || int64(len(audio)) > p.audioBound() {
		// The call may have been billed but the clip is unusable.
		p.settle(ctx, gen.ID, store.GenerationUnknown, "unusable_result")
		art.release(ctx, prep.ID, "unknown")
		return PreparedSpeech{}, fmt.Errorf("%w: unusable_result", ErrPreparationUnknown)
	}

	// 5. Persist exactly the bytes that were paid for.
	facts, err := art.fill(ctx, sc, prep, speechMediaType, speechFilename, audio)
	if err != nil {
		p.settle(ctx, gen.ID, store.GenerationUnknown, "snapshot_failed")
		art.release(ctx, prep.ID, "unknown")
		p.log().Warn("speech clip could not be stored; will not retry", slog.String("generation", gen.ID), slog.Any("err", err))
		return PreparedSpeech{}, fmt.Errorf("%w: snapshot_failed", ErrPreparationUnknown)
	}
	wctx, wcancel := context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
	defer wcancel()
	if err = p.Store.CompleteGeneration(wctx, gen.ID, facts.ArtifactID); err != nil {
		// Stays "generating"; the sweep makes it unknown and the clip expires.
		return PreparedSpeech{}, fmt.Errorf("%w: ledger_write_failed", ErrPreparationUnknown)
	}
	return PreparedSpeech{GenerationID: gen.ID, PreparationID: prep.ID, ArtifactID: facts.ArtifactID,
		Ref: store.ArtifactRef(facts.ArtifactID), Size: facts.Size, SHA256: facts.SHA256, preparation: prep}, nil
}

// existing maps a repeated intent to its recorded outcome without any call.
func (p *SpeechPreparer) existing(ctx context.Context, g store.Generation) (PreparedSpeech, error) {
	switch g.State {
	case store.GenerationReady:
		a, err := p.Store.GetArtifact(ctx, g.ArtifactID)
		if err != nil || a.State != store.ArtifactReady {
			return PreparedSpeech{}, ErrAudioUnavailable
		}
		return PreparedSpeech{GenerationID: g.ID, PreparationID: a.PreparationID, ArtifactID: a.ID, Ref: store.ArtifactRef(a.ID),
			Size: a.Size, SHA256: a.SHA256, Reused: true}, nil
	case store.GenerationFailed:
		return PreparedSpeech{}, fmt.Errorf("%w: %s", ErrGenerationFailed, g.ReasonCode)
	case store.GenerationUnknown:
		return PreparedSpeech{}, fmt.Errorf("%w: %s", ErrPreparationUnknown, g.ReasonCode)
	}
	return PreparedSpeech{}, ErrSpeechInProgress
}

// settle writes a terminal ledger state on a context that survives cancellation.
func (p *SpeechPreparer) settle(ctx context.Context, id, state, reason string) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
	defer cancel()
	var err error
	if state == store.GenerationFailed {
		err = p.Store.FailGeneration(wctx, id, reason)
	} else {
		err = p.Store.MarkGenerationUnknown(wctx, id, reason)
	}
	if err != nil {
		p.log().Error("record speech outcome", slog.String("generation", id), slog.String("state", state), slog.Any("err", err))
	}
}

func generationReasonOK(code string) bool {
	if code == "" || len(code) > maxSpeechReasonLength {
		return false
	}
	for _, r := range code {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
