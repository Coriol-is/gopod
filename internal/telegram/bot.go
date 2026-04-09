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

	"github.com/spaceinvaderz/picoclaw/internal/store"
)

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

	// /ping → pong. MatchTypeCommand catches both /ping and /ping@picoclawbot.
	api.RegisterHandler(bot.HandlerTypeMessageText, "/ping", bot.MatchTypeCommand, b.pingHandler)

	return b, nil
}

// Run starts the long-poll loop and blocks until ctx is cancelled.
// go-telegram/bot's Start() does not return an error; on cancellation it
// shuts down its internal goroutines and returns.
func (b *Bot) Run(ctx context.Context) error {
	b.log.Info("telegram bot starting (long poll)")
	b.api.Start(ctx)
	b.log.Info("telegram bot stopped")
	return nil
}

// ErrEmptyToken signals that no Telegram bot token was provided. Callers
// (typically cmd/picoclaw/main.go) check for this so they can decide
// whether to skip the subsystem or fail loudly.
var ErrEmptyToken = errors.New("telegram: empty bot token")
