package groupruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

const (
	autoRetryBackoff = 5 * time.Minute
	autoNoticeGap    = time.Hour
	autoTimeout      = 30 * time.Second
	maxLabelRunes    = 48
)

var errNotApplicable = errors.New("group preparation not applicable")

// autoState holds the in-memory coordination for automatic preparation. Durable
// truth stays in the store (epoch readiness, disabled flags); losing this state
// on restart only repeats an idempotent, resumable step.
type autoState struct {
	mu       sync.Mutex
	inflight map[string]chan struct{}
	failedAt map[string]time.Time
	noticeAt map[string]time.Time
	stages   map[string]string
	now      func() time.Time
	// spawn starts background reconciliation; tests run it inline.
	spawn func(func())
}

func (a *autoState) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// EnsurePrepared runs the same resumable Prepare + Enable steps the owner would
// run by hand, for a registered group that has no ready clean epoch. It is a
// no-op outside registered mode, for the owner chat, for ready groups and for
// groups the owner disabled. force skips the failure backoff (owner-initiated).
// Concurrent calls for one group share a single execution. Failures leave the
// group denied; nothing is deleted and no foreground work is stopped.
func (m *Manager) EnsurePrepared(ctx context.Context, jid string, force bool) (string, error) {
	c, err := m.autoCandidate(ctx, jid)
	if err != nil {
		if errors.Is(err, errNotApplicable) {
			return "", nil
		}
		return "", ErrUnavailable
	}
	a := &m.auto
	a.mu.Lock()
	if wait := a.inflight[jid]; wait != nil {
		a.mu.Unlock()
		return m.awaitPreparation(ctx, jid, wait)
	}
	if at, failed := a.failedAt[jid]; failed && !force && a.clock().Sub(at) < autoRetryBackoff {
		a.mu.Unlock()
		return "", ErrUnavailable
	}
	if a.inflight == nil {
		a.inflight = map[string]chan struct{}{}
	}
	done := make(chan struct{})
	a.inflight[jid] = done
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.inflight, jid)
		a.mu.Unlock()
		close(done)
	}()
	return m.runPreparation(ctx, c)
}

func (m *Manager) awaitPreparation(ctx context.Context, jid string, wait chan struct{}) (string, error) {
	select {
	case <-wait:
	case <-ctx.Done():
		return "", ErrUnavailable
	}
	if cur, err := m.Store.GetRegistered(ctx, jid); err != nil || !cur.GroupEpochReady {
		return "", ErrUnavailable
	}
	return "", nil
}

func (m *Manager) autoCandidate(ctx context.Context, jid string) (store.RegisteredChat, error) {
	if m.Store == nil || m.Docker == nil || !m.Config.registered() || !strings.HasPrefix(jid, "tg:-") {
		return store.RegisteredChat{}, errNotApplicable
	}
	c, err := m.Store.GetRegistered(ctx, jid)
	if err != nil {
		if errors.Is(err, store.ErrChatNotRegistered) {
			return c, errNotApplicable
		}
		return c, err
	}
	if !groupChat(c) || c.GroupEpochReady {
		return c, errNotApplicable
	}
	owner, err := m.Store.GetState(ctx, groupOwnerDisabledKey+c.NamespaceID)
	if err != nil {
		return c, err
	}
	if owner == "1" {
		return c, errNotApplicable
	}
	return c, nil
}

func (m *Manager) runPreparation(ctx context.Context, c store.RegisteredChat) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, autoTimeout)
	defer cancel()
	step := func(ctx context.Context) (string, error) { return m.prepareAndEnable(ctx, c.JID) }
	var text string
	var err error
	if m.Slot != nil {
		text, err = m.Slot(ctx, c.Folder, step)
	} else {
		text, err = step(ctx)
	}
	a := &m.auto
	if err != nil {
		a.mu.Lock()
		if a.failedAt == nil {
			a.failedAt = map[string]time.Time{}
		}
		a.failedAt[c.JID] = a.clock()
		a.mu.Unlock()
		m.log("group preparation failed", c.JID)
		m.notifyOnce(ctx, c, fmt.Sprintf("Group %s could not be prepared automatically; it stays denied. Check /isolation status %s.", groupLabel(c), c.JID))
		return "", ErrUnavailable
	}
	a.mu.Lock()
	delete(a.failedAt, c.JID)
	a.mu.Unlock()
	m.notifyOnce(ctx, c, text)
	return text, nil
}

// prepareAndEnable is the owner's /isolation prepare followed by enable, reusing
// every existing check. Prepare leaves the namespace disabled until Enable passes.
func (m *Manager) prepareAndEnable(ctx context.Context, jid string) (string, error) {
	paused, err := m.prepareCore(ctx, jid)
	if err != nil {
		m.setStage(jid, "prepare")
		return "", err
	}
	if _, err = m.enableCore(ctx, jid); err != nil {
		m.setStage(jid, "enable")
		return "", err
	}
	m.setStage(jid, "")
	c, _ := m.Store.GetRegistered(ctx, jid)
	return fmt.Sprintf("Group %s prepared: %d old schedules paused, old state retired not deleted.", groupLabel(c), paused), nil
}

// notifyOnce sends a private owner notice, at most one per group per autoNoticeGap
// for failures; success notices are sent once because readiness is durable.
func (m *Manager) notifyOnce(ctx context.Context, c store.RegisteredChat, text string) {
	if m.Notify == nil {
		return
	}
	a := &m.auto
	key := c.JID + "|" + text[:min(len(text), 24)]
	a.mu.Lock()
	if a.noticeAt == nil {
		a.noticeAt = map[string]time.Time{}
	}
	if at, ok := a.noticeAt[key]; ok && a.clock().Sub(at) < autoNoticeGap {
		a.mu.Unlock()
		return
	}
	a.noticeAt[key] = a.clock()
	a.mu.Unlock()
	m.Notify(ctx, text)
}

func (m *Manager) log(msg, jid string) {
	if m.Log != nil {
		m.Log.Warn(msg, "jid", jid, "stage", m.lastStage(jid))
	}
}

func (m *Manager) setStage(jid, stage string) {
	a := &m.auto
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stages == nil {
		a.stages = map[string]string{}
	}
	if stage == "" {
		delete(a.stages, jid)
		return
	}
	a.stages[jid] = stage
}

// RecordStage remembers the last structural admission stage that refused a turn,
// for owner diagnostics only.
func (m *Manager) RecordStage(jid, stage string) { m.setStage(jid, "admission:"+stage) }

func (m *Manager) lastStage(jid string) string {
	a := &m.auto
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.stages[jid]; s != "" {
		return "last refusal at " + s
	}
	return ""
}

func groupLabel(c store.RegisteredChat) string {
	name := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, c.Name)
	if r := []rune(name); len(r) > maxLabelRunes {
		name = string(r[:maxLabelRunes])
	}
	if name == "" {
		return c.JID
	}
	return fmt.Sprintf("%q (%s)", name, c.JID)
}

// kickPrepare starts background preparation the first time an unready registered
// group is seen at admission. It is cheap when nothing applies.
func (m *Manager) kickPrepare(c store.RegisteredChat) {
	if m.Store == nil || m.Docker == nil || !m.Config.registered() || !groupChat(c) || c.GroupEpochReady || m.Config.Qualification == "" || len(m.Config.Providers) == 0 {
		return
	}
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), autoTimeout)
		defer cancel()
		_, _ = m.EnsurePrepared(ctx, c.JID, false)
	}
	if m.auto.spawn != nil {
		m.auto.spawn(run)
		return
	}
	go run()
}

// ReconcileAll prepares every registered group that has no ready clean epoch.
// Intended for startup; each group is handled independently and fails closed.
func (m *Manager) ReconcileAll(ctx context.Context) {
	if m.Store == nil || !m.Config.registered() {
		return
	}
	rows, err := m.Store.ListRegistered(ctx)
	if err != nil {
		return
	}
	for _, c := range rows {
		if groupChat(c) && !c.GroupEpochReady {
			_, _ = m.EnsurePrepared(ctx, c.JID, false)
		}
	}
}
