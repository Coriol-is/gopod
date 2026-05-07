// Package ipc implements a filesystem-mediated container → host
// backchannel for the gopod agent. See
// docs/superpowers/specs/2026-05-07-m7-ipc-design.md for the full
// design.
package ipc

import (
	"context"
	"log/slog"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// MessageSink delivers an agent-authored Telegram message identified
// by its destination chat JID.
type MessageSink interface {
	Send(ctx context.Context, chatJID, text string) error
}

// TaskSink receives schedule/cancel operations for the gopod
// scheduler. The watcher does no validation of TaskRecord fields
// before calling Schedule; the sink is the validation seam.
type TaskSink interface {
	Schedule(ctx context.Context, t store.TaskRecord) error
	Cancel(ctx context.Context, taskID string) error
}

// FolderResolver maps a chat folder name to the registered Telegram
// JID for that chat. ok=false signals "no registration for this
// folder", which the authz layer treats as a permanent failure.
type FolderResolver func(folder string) (jid string, ok bool)

// Config bundles every knob the watcher exposes. Defaults are filled
// in by New when fields are zero-valued.
type Config struct {
	DataDir      string          // ${GOPOD_DATA_DIR}; ipc lives at <DataDir>/ipc
	OwnerFolder  string          // e.g. "owner"
	Tick         time.Duration   // default 1s
	MaxRetries   int             // default 3
	Retention    int             // default 200 per .processed / .failed
	RetryBackoff []time.Duration // default [5s, 15s, 45s]
	NowFn        func() time.Time
}

// New constructs a Watcher. The returned value is ready to Run.
func New(cfg Config, msgs MessageSink, tasks TaskSink, log *slog.Logger, resolve FolderResolver) *Watcher {
	if cfg.Tick == 0 {
		cfg.Tick = time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.Retention == 0 {
		cfg.Retention = 200
	}
	if len(cfg.RetryBackoff) == 0 {
		cfg.RetryBackoff = []time.Duration{5 * time.Second, 15 * time.Second, 45 * time.Second}
	}
	if cfg.NowFn == nil {
		cfg.NowFn = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Watcher{
		cfg:     cfg,
		msgs:    msgs,
		tasks:   tasks,
		log:     log,
		resolve: resolve,
		retries: make(map[string]retryEntry),
	}
}

type retryEntry struct {
	attempts    int
	nextRetryAt time.Time
}

// Watcher walks data/ipc/*/messages and data/ipc/*/tasks every Tick
// and dispatches what it finds.
type Watcher struct {
	cfg     Config
	msgs    MessageSink
	tasks   TaskSink
	log     *slog.Logger
	resolve FolderResolver
	retries map[string]retryEntry

	// handleFolderHook overrides handleFolder when non-nil. Tests use
	// it to assert traversal without exercising the dispatch pipeline.
	handleFolderHook func(ctx context.Context, folder string)
}

// Run blocks until ctx is cancelled. Run does not return its own
// errors — the watcher's contract is to keep going regardless of
// per-file failures, logging at warn / error level.
func (w *Watcher) Run(ctx context.Context) error {
	t := time.NewTicker(w.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			w.tickOnce(ctx)
		}
	}
}

// Reserved channels.
//
// data/ipc/<chat>/input/ is created by EnsureChatDirs at chat
// bootstrap and is mounted into the agent at /workspace/ipc/input.
// gopod's host-side has no producer for this channel in v1; the
// directory exists so that a future feature (e.g. a /inject slash
// command, an external skill) can drop a JSON payload of the shape
//
//	{ "text": "<follow-up text>" }
//
// for the agent to read on its next turn. Path-based authz applies as
// for any other channel: the parent folder is the source identity, so
// only owner-side writers may target a different chat folder.
//
// _close has been intentionally dropped from the M7 spec — gopod uses
// one-shot `docker exec` per turn rather than a long-lived inner
// agent loop, so there is no consumer.
