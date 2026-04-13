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

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/spaceinvaderz/gopod/internal/control"
	"github.com/spaceinvaderz/gopod/internal/queue"
	"github.com/spaceinvaderz/gopod/internal/runner"
	"github.com/spaceinvaderz/gopod/internal/runner/mountsec"
	"github.com/spaceinvaderz/gopod/internal/store"
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
}

// Bot is gopod's wrapper around go-telegram/bot.Bot. It owns the
// long-poll loop and routes incoming updates to handlers in this package.
//
// Construct via New, then call Run(ctx) — Run blocks until ctx is
// cancelled (typically by SIGINT/SIGTERM in main). One Bot instance
// corresponds to one Telegram bot token.
type Bot struct {
	api         *bot.Bot
	store       *store.Store
	runner      *runner.Runner
	queue       *queue.Queue
	router      *control.Router
	allowlist   *mountsec.Allowlist
	ownerChatID int64
	log         *slog.Logger
	logins      *loginSessions
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
		store:       deps.Store,
		runner:      deps.Runner,
		queue:       deps.Queue,
		router:      deps.Router,
		allowlist:   deps.Allowlist,
		ownerChatID: deps.OwnerChatID,
		log:         deps.Log,
		logins:      newLoginSessions(),
	}

	// Load last processed update offset for cursor backfill (M8a).
	// On restart, this tells Telegram to deliver any updates that
	// arrived while gopod was down.
	opts := []bot.Option{
		bot.WithDefaultHandler(b.defaultHandler),
		bot.WithMiddlewares(b.offsetMiddleware),
	}
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

	// /login is special-cased because it's a stateful interactive
	// session (stdin pipe, timeout), not a stateless Command→Response.
	// All other slash commands go through the Router via slashHandler.
	api.RegisterHandler(bot.HandlerTypeMessageText, "login", bot.MatchTypeCommand, b.loginHandler)

	return b, nil
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
