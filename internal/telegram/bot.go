// Package telegram is picoclaw's Telegram frontend.
//
// M1 scope: long-poll a single bot token, store every inbound message in
// internal/store, and answer /ping with "pong". No agent loop, no
// triggers, no chunking, no owner gating yet — those land in M2/M3/M3.5
// alongside the runner and control plane.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

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
}

// Bot is picoclaw's wrapper around go-telegram/bot.Bot. It owns the
// long-poll loop and routes incoming updates to handlers in this package.
//
// Construct via New, then call Run(ctx) — Run blocks until ctx is
// cancelled (typically by SIGINT/SIGTERM in main). One Bot instance
// corresponds to one Telegram bot token.
type Bot struct {
	api   *bot.Bot
	store *store.Store
	log   *slog.Logger
}

// New constructs a Bot. The token must be a valid @BotFather token; an
// empty token returns ErrEmptyToken so callers can branch on "no
// telegram subsystem at all" without scattering empty-string checks.
//
// The store handle is required — every inbound update is persisted there.
func New(token string, st *store.Store, log *slog.Logger) (*Bot, error) {
	if token == "" {
		return nil, ErrEmptyToken
	}
	if st == nil {
		return nil, errors.New("telegram: nil store")
	}
	if log == nil {
		log = slog.Default()
	}

	b := &Bot{store: st, log: log}

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
