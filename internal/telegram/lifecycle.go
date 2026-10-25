package telegram

// Lifecycle notices (D027): gopod tells people when it is about to
// restart and when it is back.
//
// On a graceful shutdown (SIGTERM/SIGINT) the owner chat and every
// conversation with an unfinished turn get one short message, before
// the app context is cancelled. The sends run concurrently under one
// hard deadline, so Telegram can never hold shutdown up.
//
// On startup, after the queue has replayed unfinished turns, the owner
// gets one message: "back" when the previous run shut down gracefully,
// "unexpected restart" when it did not (crash, kill -9, power loss),
// nothing on the very first start. Chats whose turns were replayed are
// told by the resumed turn itself, so they get nothing here.
//
// State lives in router_state and is kept whether or not notices are
// enabled, so turning them on later does not report a phantom crash.

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/go-telegram/bot"

	"github.com/Coriol-is/gopod/internal/store"
)

// Notice texts. Russian is the owner's language.
const (
	noticeShutdown = "🔄 Перезапускаюсь. Вернусь через минуту — незавершённое продолжу сам."
	noticeBackFmt  = "✅ Снова на связи (версия %s)."
	noticeCrashFmt = "⚠️ Был неожиданный перезапуск (версия %s)."
	// noticeResumeFmt takes the count, the adjective and the noun,
	// see resumeLine.
	noticeResumeFmt = "Продолжаю %d %s %s."
)

// router_state keys.
const (
	stateGracefulShutdownAt = "lifecycle:graceful_shutdown_at"
	stateLastStartNoticeAt  = "lifecycle:last_start_notice_at"
	stateStartedAt          = "lifecycle:started_at"
)

const (
	// DefaultShutdownNoticeTimeout bounds all shutdown sends together.
	DefaultShutdownNoticeTimeout = 3 * time.Second
	// startNoticeTimeout bounds the startup send.
	startNoticeTimeout = 10 * time.Second
	// startNoticeMinGap is the crash-loop guard: at most one startup
	// notice per this window.
	startNoticeMinGap = 2 * time.Minute
)

// resumeLine is "Продолжаю N незавершённых запросов." with the Russian
// plural of the noun (1 запрос, 2-4 запроса, 5+ запросов; 11-14 take
// the many form) and of the adjective (singular only for the "one" form).
func resumeLine(n int) string {
	m10, m100 := n%10, n%100
	switch {
	case m10 == 1 && m100 != 11:
		return fmt.Sprintf(noticeResumeFmt, n, "незавершённый", "запрос")
	case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
		return fmt.Sprintf(noticeResumeFmt, n, "незавершённых", "запроса")
	default:
		return fmt.Sprintf(noticeResumeFmt, n, "незавершённых", "запросов")
	}
}

// startKind classifies how the previous run ended.
type startKind int

const (
	startFirst    startKind = iota // no previous run recorded
	startGraceful                  // previous run left the graceful marker
	startCrash                     // previous run started but left no marker
)

func (k startKind) String() string {
	switch k {
	case startGraceful:
		return "graceful"
	case startCrash:
		return "crash"
	default:
		return "first"
	}
}

// decideStart is the startup decision table. prevStarted: a previous
// run recorded its start. graceful: it also left the shutdown marker.
// lastNotice: when the last startup notice went out (zero = never).
// It returns the kind and whether to send a notice now.
func decideStart(prevStarted, graceful bool, lastNotice, now time.Time) (startKind, bool) {
	kind := startCrash
	switch {
	case graceful:
		kind = startGraceful
	case !prevStarted:
		return startFirst, false
	}
	if !lastNotice.IsZero() && now.Sub(lastNotice) < startNoticeMinGap {
		return kind, false
	}
	return kind, true
}

// startText renders the owner's startup notice.
func startText(kind startKind, version string, replayed int) string {
	format := noticeBackFmt
	if kind == startCrash {
		format = noticeCrashFmt
	}
	text := fmt.Sprintf(format, version)
	if replayed > 0 {
		text += " " + resumeLine(replayed)
	}
	return text
}

// LifecycleStore is the slice of the store the notices need.
type LifecycleStore interface {
	GetState(ctx context.Context, key string) (string, error)
	SetState(ctx context.Context, key, value string) error
	ListUnfinishedTurns(ctx context.Context) ([]store.Turn, error)
}

// LifecycleConfig configures NewLifecycle.
type LifecycleConfig struct {
	Store       LifecycleStore
	OwnerChatID int64
	Version     string
	// Enabled is GOPOD_NOTIFY_LIFECYCLE. With it off, or without an
	// owner, nothing is sent; state is still kept.
	Enabled bool
	// ShutdownTimeout bounds the shutdown sends; zero means
	// DefaultShutdownNoticeTimeout.
	ShutdownTimeout time.Duration
}

// Lifecycle sends the restart notices. A nil *Lifecycle is a no-op.
type Lifecycle struct {
	b       *Bot
	st      LifecycleStore
	owner   int64
	version string
	enabled bool
	timeout time.Duration
	log     *slog.Logger
	now     func() time.Time

	// mu orders Started's state section against Shutdown's marker: a
	// signal during startup must not have its marker read and cleared
	// as if it were the previous run's.
	mu           sync.Mutex
	shuttingDown bool
}

// NewLifecycle returns the notice sender for this bot.
func (b *Bot) NewLifecycle(cfg LifecycleConfig) *Lifecycle {
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = DefaultShutdownNoticeTimeout
	}
	return &Lifecycle{
		b:       b,
		st:      cfg.Store,
		owner:   cfg.OwnerChatID,
		version: cfg.Version,
		enabled: cfg.Enabled && cfg.OwnerChatID != 0,
		timeout: cfg.ShutdownTimeout,
		log:     b.log.With(slog.String("component", "lifecycle")),
		now:     time.Now,
	}
}

// Started records this run's start, decides from the previous run's
// markers what to tell the owner, and sends it. replayed is the number
// of turns queue.Recover scheduled. Call once, after Recover. Blocks
// for at most the startup send timeout.
func (l *Lifecycle) Started(ctx context.Context, replayed int) {
	if l == nil || l.st == nil {
		return
	}
	// State must be kept even if a signal is already cancelling ctx.
	stCtx := context.WithoutCancel(ctx)
	l.mu.Lock()
	if l.shuttingDown {
		l.mu.Unlock()
		return
	}
	now := l.now()
	prevStarted := l.getTime(stCtx, stateStartedAt)
	graceful := l.getTime(stCtx, stateGracefulShutdownAt)
	lastNotice := l.getTime(stCtx, stateLastStartNoticeAt)
	// The marker belongs to the previous run: clear it so a crash of
	// this run is not mistaken for a graceful exit.
	if !graceful.IsZero() {
		l.setState(stCtx, stateGracefulShutdownAt, "")
	}
	l.setTime(stCtx, stateStartedAt, now)
	kind, send := decideStart(!prevStarted.IsZero(), !graceful.IsZero(), lastNotice, now)
	if send && l.enabled {
		l.setTime(stCtx, stateLastStartNoticeAt, now)
	}
	l.mu.Unlock()

	attrs := []any{slog.String("previous_run", kind.String()), slog.Int("replayed", replayed)}
	switch {
	case kind == startFirst:
		l.log.Info("lifecycle: first start, no notice", attrs...)
		return
	case !send:
		l.log.Warn("lifecycle: startup notice suppressed (one per 2 minutes)",
			append(attrs, slog.Time("last_notice", lastNotice))...)
		return
	case !l.enabled:
		l.log.Info("lifecycle: startup notice disabled", attrs...)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, startNoticeTimeout)
	defer cancel()
	if err := l.b.sendNotice(sctx, l.owner, 0, startText(kind, l.version, replayed)); err != nil {
		l.log.Warn("lifecycle: startup notice failed", append(attrs, slog.Any("err", err))...)
		return
	}
	l.log.Info("lifecycle: startup notice sent", attrs...)
}

// Shutdown records a graceful shutdown and tells the owner and every
// conversation with an unfinished turn that gopod is restarting. It
// returns within the shutdown timeout whatever Telegram does; failures
// are logged and ignored. Call before the app context is cancelled.
func (l *Lifecycle) Shutdown(ctx context.Context) {
	if l == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.timeout)
	defer cancel()
	l.mu.Lock()
	l.shuttingDown = true
	if l.st != nil {
		l.setTime(ctx, stateGracefulShutdownAt, l.now())
	}
	l.mu.Unlock()
	if !l.enabled {
		return
	}
	targets := l.shutdownTargets(ctx)

	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t chatThread) {
			defer wg.Done()
			if err := l.b.sendNotice(ctx, t.chat, t.thread, noticeShutdown); err != nil {
				l.log.Warn("lifecycle: shutdown notice failed",
					slog.Int64("chat_id", t.chat), slog.Int("thread", t.thread), slog.Any("err", err))
			}
		}(t)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		l.log.Info("lifecycle: shutdown notices sent", slog.Int("targets", len(targets)))
	case <-ctx.Done():
		l.log.Warn("lifecycle: shutdown notices timed out", slog.Int("targets", len(targets)))
	}
}

// shutdownTargets is the owner's main chat plus each distinct
// (chat, thread) of an unfinished turn. Task turns (no chat id) run
// silently and are skipped.
func (l *Lifecycle) shutdownTargets(ctx context.Context) []chatThread {
	targets := []chatThread{{chat: l.owner}}
	seen := map[chatThread]bool{targets[0]: true}
	if l.st == nil {
		return targets
	}
	turns, err := l.st.ListUnfinishedTurns(ctx)
	if err != nil {
		l.log.Warn("lifecycle: list unfinished turns", slog.Any("err", err))
		return targets
	}
	for _, t := range turns {
		ct := chatThread{chat: t.ChatID, thread: t.ThreadID}
		if ct.chat == 0 || seen[ct] {
			continue
		}
		seen[ct] = true
		targets = append(targets, ct)
	}
	return targets
}

// sendNotice sends a plain-text system message into (chatID, thread),
// or the main chat when the topic is gone. It is not a turn reply and
// is not recorded anywhere.
func (b *Bot) sendNotice(ctx context.Context, chatID int64, thread int, text string) error {
	if b.threadGone(chatID, thread) {
		thread = 0
	}
	_, err := b.api.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID, MessageThreadID: thread, Text: text,
	})
	if err != nil && b.checkThreadGone(chatID, thread, err) {
		_, err = b.api.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text})
	}
	if err != nil {
		return fmt.Errorf("send notice: %w", err)
	}
	return nil
}

// getTime reads a unix-millisecond state value; zero when unset or
// unreadable.
func (l *Lifecycle) getTime(ctx context.Context, key string) time.Time {
	v, err := l.st.GetState(ctx, key)
	if err != nil {
		l.log.Warn("lifecycle: read state", slog.String("key", key), slog.Any("err", err))
		return time.Time{}
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func (l *Lifecycle) setTime(ctx context.Context, key string, t time.Time) {
	l.setState(ctx, key, strconv.FormatInt(t.UnixMilli(), 10))
}

func (l *Lifecycle) setState(ctx context.Context, key, value string) {
	if err := l.st.SetState(ctx, key, value); err != nil {
		l.log.Warn("lifecycle: write state", slog.String("key", key), slog.Any("err", err))
	}
}
