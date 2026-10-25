// Package telegram is gopod's Telegram frontend.
//
// As of M6 the default handler routes inbound text from registered
// chats through the runner and replies with the agent's output. The
// owner chat (GOPOD_OWNER_CHAT_ID) is auto-registered on first
// sight; other chats are registered explicitly via /register from
// the owner chat.
//
// Auth state is checked before every prompt: if the agent container
// is not logged in, the bot replies asking the user to run /login
// instead of trying to talk to claude.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"github.com/Coriol-is/gopod/internal/store"
)

// publicCommands is now built dynamically from the Router's command
// list in publishCommands(). This global is kept as a nil-init
// sentinel so the old compile references don't break.

// Deps groups gopod's runtime dependencies the telegram bot needs
// at construction time. Bundling them in a struct keeps New()'s
// signature stable as new fields land.
type Deps struct {
	// Store is the SQLite handle. Required.
	Store *store.Store

	// Runner is the agent runner. Optional: if nil, the default
	// handler stays at M1 behaviour (persist + ignore for non-/ping
	// text), useful for store-only and pre-M6 dev modes.
	Runner *runner.Runner

	// Queue is the M3 GroupQueue. Optional: if nil AND Runner is
	// set, the default handler falls back to synchronous runner.Run
	// (pre-M3 behaviour). If set, messages are enqueued and the
	// queue worker calls the agent asynchronously.
	Queue *queue.Queue

	// Router is the M3.5 control plane Router. Required from M3.5
	// onward — all slash commands dispatch through it. If nil,
	// the bot falls back to the pre-M3.5 direct handlers.
	Router *control.Router

	// Allowlist is the parsed mount allowlist. Optional: nil means
	// "no extras", which is the common case.
	Allowlist *mountsec.Allowlist

	// OwnerChatID is the Telegram chat id gopod treats as the
	// owner. Zero means "no owner", in which case auto-registration
	// is disabled and /register is rejected from every chat.
	OwnerChatID int64

	// Log is the slog logger; defaults to slog.Default if nil.
	Log *slog.Logger

	// StreamEnabled enables real-time streaming of agent output to
	// Telegram. Default: true when field is set; false uses buffered path.
	StreamEnabled bool
}

// Bot is gopod's wrapper around go-telegram/bot.Bot. It owns the
// long-poll loop and routes incoming updates to handlers in this package.
//
// Construct via New, then call Run(ctx) — Run blocks until ctx is
// cancelled (typically by SIGINT/SIGTERM in main). One Bot instance
// corresponds to one Telegram bot token.
type Bot struct {
	api           *bot.Bot
	store         *store.Store
	runner        *runner.Runner
	queue         *queue.Queue
	router        *control.Router
	allowlist     *mountsec.Allowlist
	ownerChatID   int64
	log           *slog.Logger
	logins        *loginSessions
	streamEnabled bool

	// draftsOff marks chats where sendMessageDraft was refused; sticky
	// until restart. draftSeq hands out draft ids.
	draftsMu  sync.Mutex
	draftsOff map[int64]bool
	// draftBadReqs counts consecutive unrecognized 400s per chat.
	draftBadReqs map[int64]int
	draftSeq     atomic.Int64

	// goneThreads marks private-chat topics that no longer exist
	// (threadgone.go); sends into them go to the main chat.
	threadsMu   sync.Mutex
	goneThreads map[chatThread]bool

	// inflight maps turn id to its stoppable streaming state (stop.go).
	inflightMu sync.Mutex
	inflight   map[int64]*inflightTurn

	// waitPlaceholders holds placeholders sent early by a turn's agent
	// gate wait notice, for streamReply to take over (agentwait.go).
	waitMu           sync.Mutex
	waitPlaceholders map[waitKey]int
	// waitRefresh holds each waiting turn's wait draft refresher.
	waitRefresh map[waitKey]waitRefresher
	// waitDraftEvery is the wait draft refresh interval; zero means
	// draftRefresh. Tests shorten it.
	waitDraftEvery time.Duration

	// streamTick is the streaming flush cadence; zero means 1s.
	streamTick time.Duration

	// sleep waits d or until ctx ends; nil means sleepCtx. Tests stub
	// it to skip retry_after waits.
	sleep func(ctx context.Context, d time.Duration) error

	// callbacks maps a callback act to its handler (callback.go).
	cbMu      sync.RWMutex
	callbacks map[string]cbHandler

	// errMu guards errShown, the last time each (chat, error kind)
	// reply went out (errors.go cooldown), and retried, the turns a
	// Retry button already re-enqueued (actions.go).
	errMu    sync.Mutex
	errShown map[errCooldownKey]time.Time
	retried  map[int64]time.Time

	// now is the clock of the error cooldown; nil means time.Now.
	now func() time.Time

	// loginRun starts a reserved login and reports whether a session
	// was stored; nil means runLogin. Tests stub it (login.go).
	loginRun func(ctx context.Context, m *models.Message, thread int, rc store.RegisteredChat) bool

	// statuses holds each user message's reaction state machine
	// (status.go); reactClock is its clock, nil means the real one.
	statusMu   sync.Mutex
	statuses   map[statusKey]*statusReactor
	reactClock reactClock
	// busyThreads counts turns streaming per chat thread; queueWaits
	// holds the queue-position draft loops of waiting turns
	// (statusline.go). Both under statusMu.
	busyThreads map[chatThread]int
	queueWaits  map[chatThread][]*queueWaiter
	// rows is the reply action row state (replyrow.go), under rowMu.
	rowMu sync.Mutex
	rows  replyRow

	// albums holds media groups being collected (inbound_context.go);
	// albumClock is their window's clock, nil means the real one.
	albumMu    sync.Mutex
	albums     map[albumKey]*albumBuf
	albumClock reactClock

	// whisperURL is the transcription endpoint; "" means OpenAI's.
	// Tests point it at a fake.
	whisperURL string
}

// allowedUpdates is the explicit getUpdates allowed_updates list. Before
// inline buttons gopod sent none, so Telegram's default set applied
// (every type except chat_member, message_reaction and
// message_reaction_count); of that set gopod handles only message and
// stopped_message_generation (draft Stop). callback_query is in the
// default set too, but listing the three keeps the set gopod depends on
// visible. Add a type here before handling it.
var allowedUpdates = bot.AllowedUpdates{
	"message",
	"callback_query",
	"stopped_message_generation",
}

// New constructs a Bot. The token must be a valid @BotFather token; an
// empty token returns ErrEmptyToken so callers can branch on "no
// telegram subsystem at all" without scattering empty-string checks.
//
// The Store handle in deps is required — every inbound update is
// persisted there. The Runner handle is optional; without it the bot
// runs in M1-style "echo" mode (persist + /ping reply + nothing else).
func New(token string, deps Deps) (*Bot, error) {
	if token == "" {
		return nil, ErrEmptyToken
	}
	if deps.Store == nil {
		return nil, errors.New("telegram: nil Store")
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}

	b := &Bot{
		store:         deps.Store,
		runner:        deps.Runner,
		queue:         deps.Queue,
		router:        deps.Router,
		allowlist:     deps.Allowlist,
		ownerChatID:   deps.OwnerChatID,
		log:           deps.Log,
		logins:        newLoginSessions(),
		streamEnabled: deps.StreamEnabled,
	}

	opts := b.botOptions()
	// Load last processed update offset for cursor backfill (M8a).
	// On restart, this tells Telegram to deliver any updates that
	// arrived while gopod was down.
	if deps.Store != nil {
		offsetStr, _ := deps.Store.GetState(context.Background(), "telegram_update_offset")
		if offsetStr != "" {
			var offset int64
			fmt.Sscanf(offsetStr, "%d", &offset)
			if offset > 0 {
				opts = append(opts, bot.WithInitialOffset(offset))
				deps.Log.Info("telegram: resuming from stored offset",
					slog.Int64("offset", offset))
			}
		}
	}

	api, err := bot.New(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("telegram: bot.New: %w", err)
	}
	b.api = api
	b.registerHandlers(api)
	b.registerCallbacks()

	return b, nil
}

// botOptions are the go-telegram/bot options of every gopod bot: the
// default handler, the offset middleware and the allowed updates.
func (b *Bot) botOptions() []bot.Option {
	return []bot.Option{
		bot.WithDefaultHandler(b.defaultHandler),
		bot.WithMiddlewares(b.offsetMiddleware),
		bot.WithAllowedUpdates(allowedUpdates),
	}
}

// registerHandlers registers the handlers that take updates away from
// the default handler: /login and every callback_query.
func (b *Bot) registerHandlers(api *bot.Bot) {
	// /login is special-cased because it's a stateful interactive
	// session (stdin pipe, timeout), not a stateless Command→Response.
	// All other slash commands go through the Router via slashHandler.
	api.RegisterHandler(bot.HandlerTypeMessageText, "login", bot.MatchTypeCommand, b.loginHandler)
	// Every inline button press goes to one dispatcher (callback.go).
	// An empty prefix matches all callback data and nothing else.
	api.RegisterHandler(bot.HandlerTypeCallbackQueryData, "", bot.MatchTypePrefix, b.callbackHandler)
}

// Run starts the long-poll loop and blocks until ctx is cancelled.
// go-telegram/bot's Start() does not return an error; on cancellation it
// shuts down its internal goroutines and returns.
//
// Before entering the loop, Run publishes the public command list to
// Telegram via setMyCommands so clients can autocomplete them. A
// publish failure is logged at warn level and the bot keeps running —
// command discovery is nice-to-have, not a hard requirement.
func (b *Bot) Run(ctx context.Context) error {
	b.publishCommands(ctx)

	b.log.Info("telegram bot starting (long poll)")
	b.api.Start(ctx)
	b.log.Info("telegram bot stopped")
	return nil
}

// publishCommands pushes the command list to Telegram's autocomplete
// picker. Built dynamically from Router.List() so adding a command
// to the Router is all that's needed — no manual sync with a hardcoded
// slice.
func (b *Bot) publishCommands(ctx context.Context) {
	var cmds []models.BotCommand
	if b.router != nil {
		for _, c := range b.router.List() {
			cmds = append(cmds, models.BotCommand{
				Command:     c.SlashName,
				Description: c.Description,
			})
		}
	}
	if len(cmds) == 0 {
		return
	}
	ok, err := b.api.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: cmds,
	})
	if err != nil {
		b.log.Warn("telegram: setMyCommands failed",
			slog.Any("err", err))
		return
	}
	if !ok {
		b.log.Warn("telegram: setMyCommands returned false")
		return
	}
	b.log.Info("telegram: published commands",
		slog.Int("count", len(cmds)))
}

// offsetMiddleware persists the Telegram update_id on every update
// (registered handlers + default handler) for cursor backfill on restart.
func (b *Bot) offsetMiddleware(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, api *bot.Bot, update *models.Update) {
		if update != nil {
			b.log.Debug("middleware: update received",
				slog.Int64("update_id", update.ID),
				slog.Bool("has_message", update.Message != nil))
			if update.ID > 0 && b.store != nil {
				b.store.SetState(ctx, "telegram_update_offset",
					fmt.Sprintf("%d", update.ID+1))
			}
		}
		next(ctx, api, update)
	}
}

// SetQueue wires the GroupQueue after construction. This breaks the
// circular dependency between Bot and Queue: the Bot is constructed
// first, then the Queue is created with NewAgentHandler(), then
// SetQueue plugs it back in. Must be called before Run().
func (b *Bot) SetQueue(q *queue.Queue) { b.queue = q }

// ErrEmptyToken signals that no Telegram bot token was provided. Callers
// (typically cmd/gopod/main.go) check for this so they can decide
// whether to skip the subsystem or fail loudly.
var ErrEmptyToken = errors.New("telegram: empty bot token")
