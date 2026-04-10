// Package telegram is picoclaw's Telegram frontend.
//
// As of M6 the default handler routes inbound text from registered
// chats through the runner and replies with the agent's output. The
// owner chat (PICOCLAW_OWNER_CHAT_ID) is auto-registered on first
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

	"github.com/spaceinvaderz/picoclaw/internal/runner"
	"github.com/spaceinvaderz/picoclaw/internal/runner/mountsec"
	"github.com/spaceinvaderz/picoclaw/internal/store"
)

// publicCommands is the canonical list of slash commands picoclaw exposes
// to Telegram clients via setMyCommands. The Telegram app uses this to
// populate the "/" autocomplete picker so users see commands without any
// BotFather setup step.
//
// This list is the temporary M1 source of truth. When the M3.5 control
// plane (internal/control Router) lands, the Router becomes the authority
// and this slice goes away — the Telegram frontend will iterate registered
// commands at the right Perm level and call setMyCommands itself. Adding
// a command here without also calling RegisterHandler in New() below will
// publish a command Telegram suggests but picoclaw doesn't actually
// implement, so keep them in sync.
var publicCommands = []models.BotCommand{
	{Command: "ping", Description: "check the bot is alive"},
	{Command: "register", Description: "(owner) register a chat for agent access"},
	{Command: "whoami", Description: "show this chat's id and registration state"},
}

// Deps groups picoclaw's runtime dependencies the telegram bot needs
// at construction time. Bundling them in a struct keeps New()'s
// signature stable as new fields land.
type Deps struct {
	// Store is the SQLite handle. Required.
	Store *store.Store

	// Runner is the agent runner. Optional: if nil, the default
	// handler stays at M1 behaviour (persist + ignore for non-/ping
	// text), useful for store-only and pre-M6 dev modes.
	Runner *runner.Runner

	// Allowlist is the parsed mount allowlist. Optional: nil means
	// "no extras", which is the common case.
	Allowlist *mountsec.Allowlist

	// OwnerChatID is the Telegram chat id picoclaw treats as the
	// owner. Zero means "no owner", in which case auto-registration
	// is disabled and /register is rejected from every chat.
	OwnerChatID int64

	// Log is the slog logger; defaults to slog.Default if nil.
	Log *slog.Logger
}

// Bot is picoclaw's wrapper around go-telegram/bot.Bot. It owns the
// long-poll loop and routes incoming updates to handlers in this package.
//
// Construct via New, then call Run(ctx) — Run blocks until ctx is
// cancelled (typically by SIGINT/SIGTERM in main). One Bot instance
// corresponds to one Telegram bot token.
type Bot struct {
	api         *bot.Bot
	store       *store.Store
	runner      *runner.Runner
	allowlist   *mountsec.Allowlist
	ownerChatID int64
	log         *slog.Logger
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
		allowlist:   deps.Allowlist,
		ownerChatID: deps.OwnerChatID,
		log:         deps.Log,
	}

	api, err := bot.New(token,
		bot.WithDefaultHandler(b.defaultHandler),
	)
	if err != nil {
		return nil, fmt.Errorf("telegram: bot.New: %w", err)
	}
	b.api = api

	// /ping → pong. MatchTypeCommand strips the leading slash from the
	// message before comparing against the pattern, so the pattern must
	// be the bare word ("ping", not "/ping"). The matcher also handles
	// the @botname suffix automatically (it walks Telegram's bot_command
	// entities, which already account for it).
	api.RegisterHandler(bot.HandlerTypeMessageText, "ping", bot.MatchTypeCommand, b.pingHandler)
	api.RegisterHandler(bot.HandlerTypeMessageText, "whoami", bot.MatchTypeCommand, b.whoamiHandler)
	api.RegisterHandler(bot.HandlerTypeMessageText, "register", bot.MatchTypeCommand, b.registerHandler)

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

// publishCommands pushes the publicCommands list to Telegram. It uses
// the default scope (all chats, all users), which is correct for a
// personal single-owner bot. When non-owner chats join in M5+, this
// will be revisited so non-owner chats see a smaller picker.
func (b *Bot) publishCommands(ctx context.Context) {
	if len(publicCommands) == 0 {
		return
	}
	ok, err := b.api.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: publicCommands,
	})
	if err != nil {
		b.log.Warn("telegram: setMyCommands failed (autocomplete will be stale)",
			slog.Any("err", err))
		return
	}
	if !ok {
		b.log.Warn("telegram: setMyCommands returned false")
		return
	}
	b.log.Info("telegram: published commands",
		slog.Int("count", len(publicCommands)))
}

// ErrEmptyToken signals that no Telegram bot token was provided. Callers
// (typically cmd/picoclaw/main.go) check for this so they can decide
// whether to skip the subsystem or fail loudly.
var ErrEmptyToken = errors.New("telegram: empty bot token")
