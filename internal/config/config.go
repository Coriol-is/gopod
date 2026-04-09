// Package config loads picoclaw runtime configuration from environment
// variables, with optional .env file support for local development.
//
// Per D012, environment is the only source of truth for secret values; .env
// is loaded once at process start to populate os.Environ() and is then
// invisible to the rest of the program.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds the resolved runtime settings for picoclaw.
//
// Only fields needed by M0 (skeleton) are populated here. Later milestones
// extend this struct with their own fields (Telegram token, container image,
// observability endpoints, etc.) — they live next to the subsystems that own
// them, gathered into Config at Load time.
type Config struct {
	// DataDir is the picoclaw runtime state directory. The SQLite store
	// lives at ${DataDir}/store.sqlite, IPC dirs at ${DataDir}/ipc/<chat>/,
	// session dirs at ${DataDir}/sessions/<chat>/.
	DataDir string

	// StorePath is the absolute path to the SQLite database file.
	// Computed from DataDir at Load time.
	StorePath string

	// LogLevel controls the slog level. One of debug|info|warn|error.
	LogLevel slog.Level

	// LogFormat is "text" or "json". Defaults to text for development;
	// production deployments set PICOCLAW_LOG_FORMAT=json.
	LogFormat string

	// OwnerChatID is the Telegram chat ID of the owner. Optional in M0
	// (the runner has nothing to gate yet); enforced from M2 onward.
	// Zero means "unset".
	OwnerChatID int64

	// TelegramBotToken is the Bot API token from @BotFather.
	//
	// Optional at config level: if empty, the telegram subsystem refuses
	// to start (with a warning log) and the rest of picoclaw still runs.
	// This keeps the M0-style "binary that just opens the store" mode
	// available for diagnostics and tests.
	//
	// Required from M1 onward for any actual messaging work.
	TelegramBotToken string
}

// Load reads environment, applies defaults, validates, and returns a
// Config. Pass loadDotenv = true to also read a .env file from the current
// working directory if one exists. .env never overrides values already
// present in the environment.
func Load(loadDotenv bool) (Config, error) {
	if loadDotenv {
		// Best-effort: a missing .env is not an error.
		if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
			return Config{}, fmt.Errorf("loading .env: %w", err)
		}
	}

	cfg := Config{
		DataDir:          getenvDefault("PICOCLAW_DATA_DIR", "./data"),
		LogLevel:         parseLevel(getenvDefault("PICOCLAW_LOG_LEVEL", "info")),
		LogFormat:        strings.ToLower(getenvDefault("PICOCLAW_LOG_FORMAT", "text")),
		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
	}

	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolving PICOCLAW_DATA_DIR=%q: %w", cfg.DataDir, err)
	}
	cfg.DataDir = abs
	cfg.StorePath = filepath.Join(cfg.DataDir, "store.sqlite")

	if v := os.Getenv("PICOCLAW_OWNER_CHAT_ID"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parsing PICOCLAW_OWNER_CHAT_ID=%q: %w", v, err)
		}
		cfg.OwnerChatID = id
	}

	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		return Config{}, fmt.Errorf("PICOCLAW_LOG_FORMAT=%q: must be text or json", cfg.LogFormat)
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validate enforces invariants that must hold for picoclaw to start.
// M0 is permissive — only DataDir is required to be non-empty after
// resolution. Future milestones tighten this (e.g. M1 will require
// TELEGRAM_BOT_TOKEN, M2 will require ANTHROPIC_API_KEY).
func (c Config) validate() error {
	if c.DataDir == "" {
		return errors.New("PICOCLAW_DATA_DIR resolved to empty path")
	}
	return nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
