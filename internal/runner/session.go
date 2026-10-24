package runner

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

// Conversation sessions.
//
// Every conversation (chat folder, Telegram thread; thread 0 is the main
// chat) runs in its own agent session, named by an explicit id stored
// in the sessions table under the chat's current provider. The
// container and its home directory are shared by all conversations of a
// chat, so implicit resume (`claude --continue`, `codex exec resume
// --last`) would pick whichever conversation ran last; resuming by id
// keeps them apart. Sessions are looked up per provider, so a /provider
// switch never hands one CLI the other's id.
//
// Lifecycle of a conversation's row:
//
//   - No row: a new session. Claude gets a fresh UUID via --session-id
//     and the row is written before the run. Claude refuses --session-id
//     once a transcript exists, so even a failed or stopped first turn
//     must be followed by --resume. Codex picks its own thread id; the
//     run is a plain `codex exec` and the id is read back from its
//     output (captureSession). If none is found the next turn starts a
//     new conversation again.
//   - Row: resume by id, after SessionExistsCmd confirms the transcript
//     is still there. A row whose transcript is definitely gone (a
//     Claude first turn that died before writing, files removed by
//     hand) is dropped and the turn starts a new session.
//   - ClearSession deletes the row and that session's files only.
//
// Upgrade migration (adoptLegacySession): before the first explicit-id
// turn of any conversation in a (chat, provider), the session that
// implicit resume would have picked is adopted as the main
// conversation's, so main keeps its history and a thread session can
// never be mistaken for it. A router_state marker records that this was
// done; without it, a /clear in main followed by a main turn would
// re-adopt the newest transcript, which may be a thread's.
//
// All of this runs under the chat lock (lockChat), so conversations of
// one chat never race on their rows. Without a store (tests, early
// wiring) the runner falls back to the implicit zero SessionRef.

// adoptedStateKey marks that the legacy session of (folder, provider)
// was adopted (or found absent) for the main conversation.
func adoptedStateKey(folder, provider string) string {
	return "session_adopted:" + folder + ":" + provider
}

// resolveSession returns the session the next turn of (folder, thread)
// runs in, creating or adopting the conversation's row as needed.
// containerID is the chat's running container.
func (r *Runner) resolveSession(ctx context.Context, containerID, folder string, thread int, prov AgentProvider) (SessionRef, error) {
	if r.store == nil {
		return SessionRef{}, nil
	}
	r.lockChat(folder)
	defer r.unlockChat(folder)

	if err := r.adoptLegacySession(ctx, containerID, folder, prov); err != nil {
		return SessionRef{}, err
	}
	id, ok, err := r.liveSession(ctx, containerID, folder, thread, prov)
	if err != nil {
		return SessionRef{}, err
	}
	if ok {
		return SessionRef{ID: id, Resume: true}, nil
	}

	id = uuid.NewString()
	if !prov.AnnouncesSessionID() {
		// Stored before the run: see the lifecycle notes above.
		if err := r.store.PutSession(ctx, folder, thread, prov.Name(), id); err != nil {
			return SessionRef{}, fmt.Errorf("runner: storing new session: %w", err)
		}
	}
	r.log.Info("starting new agent session",
		slog.String("chat", folder),
		slog.Int("thread", thread),
		slog.String("provider", prov.Name()))
	return SessionRef{ID: id}, nil
}

// liveSession returns the stored session id of (folder, thread) if its
// transcript can still be resumed. A row that cannot be (bad id,
// transcript gone) is deleted and reported as absent.
func (r *Runner) liveSession(ctx context.Context, containerID, folder string, thread int, prov AgentProvider) (string, bool, error) {
	id, ok, err := r.store.GetSession(ctx, folder, thread, prov.Name())
	if err != nil {
		return "", false, fmt.Errorf("runner: reading session: %w", err)
	}
	if !ok {
		return "", false, nil
	}
	if validSessionID(id) && !r.sessionGone(ctx, containerID, prov, id) {
		return id, true, nil
	}
	r.log.Warn("stored agent session cannot be resumed; starting a new one",
		slog.String("chat", folder),
		slog.Int("thread", thread),
		slog.String("provider", prov.Name()),
		slog.String("session", id))
	if err := r.store.DeleteSession(ctx, folder, thread, prov.Name()); err != nil {
		return "", false, fmt.Errorf("runner: dropping dead session: %w", err)
	}
	return "", false, nil
}

// sessionGone reports whether session id definitely has no transcript.
// Any doubt (exec failure, unexpected exit code) answers false: dropping
// a live row would lose the conversation, while resuming a missing one
// only fails this turn.
func (r *Runner) sessionGone(ctx context.Context, containerID string, prov AgentProvider, id string) bool {
	res, err := r.ex.Exec(ctx, containerID, prov.SessionExistsCmd(id), nil)
	if err != nil {
		r.log.Warn("checking agent session failed; assuming it exists",
			slog.String("session", id), slog.Any("err", err))
		return false
	}
	switch res.ExitCode {
	case 0:
		return false
	case 1:
		return true
	default:
		r.log.Warn("checking agent session failed; assuming it exists",
			slog.String("session", id),
			slog.Int("exit", res.ExitCode),
			slog.String("stderr", tailString(res.Stderr, 300)))
		return false
	}
}

// adoptLegacySession runs the upgrade migration for (folder, provider)
// once: if the main conversation has no resumable row, the session
// implicit resume would have picked becomes main's. Caller holds the
// chat lock.
//
// A failed lookup is an error, not "no session": recording the marker
// without adopting would start main afresh and lose its history.
func (r *Runner) adoptLegacySession(ctx context.Context, containerID, folder string, prov AgentProvider) error {
	key := adoptedStateKey(folder, prov.Name())
	done, err := r.store.GetState(ctx, key)
	if err != nil {
		return fmt.Errorf("runner: reading session adoption marker: %w", err)
	}
	if done != "" {
		return nil
	}
	_, ok, err := r.liveSession(ctx, containerID, folder, 0, prov)
	if err != nil {
		return err
	}
	if !ok {
		res, err := r.ex.Exec(ctx, containerID, prov.LatestSessionIDCmd(), nil)
		if err != nil {
			return fmt.Errorf("runner: finding legacy session: %w", err)
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("runner: finding legacy session: exit %d (stderr=%q)",
				res.ExitCode, tailString(res.Stderr, 300))
		}
		switch id := strings.TrimSpace(res.Stdout); {
		case validSessionID(id):
			if err := r.store.PutSession(ctx, folder, 0, prov.Name(), id); err != nil {
				return fmt.Errorf("runner: adopting legacy session: %w", err)
			}
			r.log.Info("adopted legacy agent session for the main conversation",
				slog.String("chat", folder),
				slog.String("provider", prov.Name()),
				slog.String("session", id))
		case id != "":
			r.log.Warn("ignoring malformed legacy session id",
				slog.String("chat", folder), slog.String("id", tailString(id, 80)))
		}
	}
	if err := r.store.SetState(ctx, key, "1"); err != nil {
		return fmt.Errorf("runner: recording session adoption: %w", err)
	}
	return nil
}

// announcesNewSession reports whether a run with ref starts a
// conversation whose id the provider prints (see captureSession).
func announcesNewSession(prov AgentProvider, ref SessionRef) bool {
	return ref.ID != "" && !ref.Resume && prov.AnnouncesSessionID()
}

// captureSession stores the session id a new conversation's run
// announced. It runs whatever the run's outcome: a failed or stopped
// turn may still have created the session, and liveSession discards the
// row next turn if it did not.
//
// Only stderr is read, and its first matching line wins. In plain mode
// (the only mode RunCmd uses) Codex prints the `session id:` header on
// stderr before anything else, while stdout is the model's reply: a
// reply that prints a `session id:` line (prompt injection, or the agent
// listing ~/.codex/sessions) must never decide which session this
// conversation resumes. Should RunCmd ever switch to `--json`, where
// thread.started on stdout is protocol output, this needs revisiting.
func (r *Runner) captureSession(ctx context.Context, folder string, thread int, prov AgentProvider, stderr string) {
	ctx = context.WithoutCancel(ctx) // a stopped turn still records its session
	for _, line := range strings.Split(stderr, "\n") {
		id := prov.ExtractSessionID(line)
		if id == "" {
			continue
		}
		r.lockChat(folder)
		err := r.store.PutSession(ctx, folder, thread, prov.Name(), id)
		r.unlockChat(folder)
		if err != nil {
			r.log.Error("storing announced agent session failed",
				slog.String("chat", folder), slog.Int("thread", thread), slog.Any("err", err))
		}
		return
	}
	r.log.Warn("agent announced no session id; the next turn starts a new conversation",
		slog.String("chat", folder),
		slog.Int("thread", thread),
		slog.String("provider", prov.Name()))
}

// clearConversation deletes the session of (folder, thread): its row
// and its files, nothing else. With no row there is nothing to clear,
// except that main may still have an unadopted legacy session, which
// is adopted first so that /clear in main clears what main resumes.
func (r *Runner) clearConversation(ctx context.Context, containerID, folder string, thread int, prov AgentProvider) error {
	r.lockChat(folder)
	defer r.unlockChat(folder)

	if thread == 0 {
		if err := r.adoptLegacySession(ctx, containerID, folder, prov); err != nil {
			return err
		}
	}
	id, ok, err := r.store.GetSession(ctx, folder, thread, prov.Name())
	if err != nil {
		return fmt.Errorf("runner: reading session: %w", err)
	}
	if !ok {
		return nil
	}
	// The row goes first: once it is gone the next turn starts afresh,
	// even if deleting the files below fails.
	if err := r.store.DeleteSession(ctx, folder, thread, prov.Name()); err != nil {
		return fmt.Errorf("runner: deleting session row: %w", err)
	}
	if !validSessionID(id) {
		return nil // nothing that could be safely named on disk
	}
	res, err := r.ex.Exec(ctx, containerID, prov.ClearSessionCmd(SessionRef{ID: id}), nil)
	if err != nil {
		return fmt.Errorf("runner: deleting session files: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("runner: deleting session files: exit %d (stderr=%q)",
			res.ExitCode, tailString(res.Stderr, 300))
	}
	r.log.Info("cleared agent session",
		slog.String("chat", folder),
		slog.Int("thread", thread),
		slog.String("provider", prov.Name()),
		slog.String("session", id))
	return nil
}

// needsClear reports, without touching the container, whether
// ClearSession has anything to do for (folder, thread): a row, or for
// main an adoption that has not run yet.
func (r *Runner) needsClear(ctx context.Context, folder string, thread int, prov AgentProvider) (bool, error) {
	_, ok, err := r.store.GetSession(ctx, folder, thread, prov.Name())
	if err != nil {
		return false, fmt.Errorf("runner: reading session: %w", err)
	}
	if ok || thread != 0 {
		return ok, nil
	}
	done, err := r.store.GetState(ctx, adoptedStateKey(folder, prov.Name()))
	if err != nil {
		return false, fmt.Errorf("runner: reading session adoption marker: %w", err)
	}
	return done == "", nil
}
