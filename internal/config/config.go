// Package config loads gopod runtime configuration from environment
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

// Config holds the resolved runtime settings for gopod.
//
// Only fields needed by M0 (skeleton) are populated here. Later milestones
// extend this struct with their own fields (Telegram token, container image,
// observability endpoints, etc.) — they live next to the subsystems that own
// them, gathered into Config at Load time.
type Config struct {
	// DataDir is the gopod runtime state directory. The SQLite store
	// lives at ${DataDir}/store.sqlite, IPC dirs at ${DataDir}/ipc/<chat>/,
	// session dirs at ${DataDir}/sessions/<chat>/.
	DataDir string

	// StorePath is the absolute path to the SQLite database file.
	// Computed from DataDir at Load time.
	StorePath string

	// LogLevel controls the slog level. One of debug|info|warn|error.
	LogLevel slog.Level

	// LogFormat is "text" or "json". Defaults to text for development;
	// production deployments set GOPOD_LOG_FORMAT=json.
	LogFormat string

	// OwnerChatID is the Telegram chat ID of the owner. Optional in M0
	// (the runner has nothing to gate yet); enforced from M2 onward.
	// Zero means "unset".
	OwnerChatID int64

	// --- Session compact settings ---

	// CompactAfter triggers auto-compact after N turns. 0 = disabled.
	CompactAfter int

	// CompactInterval triggers compact every N duration. "" = disabled.
	CompactInterval string

	// CompactTime triggers compact at a specific time daily (HH:MM). "" = disabled.
	CompactTime string

	// ObsidianVault is the path to the Obsidian vault to ingest.
	// Empty = disabled. Markdown files are chunked by heading,
	// embedded, and stored in memory with kind=document.
	ObsidianVault string

	// TelegramBotToken is the Bot API token from @BotFather.
	//
	// Optional at config level: if empty, the telegram subsystem refuses
	// to start (with a warning log) and the rest of gopod still runs.
	// This keeps the M0-style "binary that just opens the store" mode
	// available for diagnostics and tests.
	//
	// Required from M1 onward for any actual messaging work.
	TelegramBotToken string

	// --- Runner / container runtime (M5+) ---------------------------

	// ContainerEnabled is true unless GOPOD_NO_CONTAINER=1.
	//
	// When false, gopod skips the Docker subsystem entirely (no
	// client open, no leftover cleanup). This is a dev-only escape
	// hatch; there is no working host-subprocess agent path yet
	// (see docs/ROADMAP.md notes on the NO_CONTAINER stub).
	ContainerEnabled bool

	// RepoRoot is the absolute path to the gopod checkout. The
	// runner mounts it RO inside the owner chat's agent container per
	// ISOLATION.md §3.1. Defaults to os.Getwd() if unset.
	RepoRoot string

	// ContainerSkillsDir is the host directory bind-mounted at
	// /home/node/.claude/skills inside every agent container. Defaults
	// to ${RepoRoot}/container/skills.
	ContainerSkillsDir string

	// ChatsDir is the host directory holding per-chat workspaces.
	// Defaults to ${RepoRoot}/chats.
	ChatsDir string

	// MountAllowlistPath is the operator-managed extras allowlist.
	// Defaults to ${DataDir}/mount-allowlist.json. Missing file is
	// not an error — gopod runs with only the standard mounts.
	MountAllowlistPath string

	// EmptyFile masks ${REPO_ROOT}/.env inside the owner agent
	// container. EnsureChatDirs creates it if missing. Defaults to
	// ${DataDir}/empty-env.
	EmptyFile string

	// ContainerImage is the image tag the runner spawns per chat.
	ContainerImage string

	// LeftoverCleanupEnabled controls the boot-time cleanup pass in
	// lifecycle.go. Default true; set GOPOD_LEFTOVER_CLEANUP=0 to
	// disable (debug only).
	LeftoverCleanupEnabled bool

	// StreamEnabled enables real-time streaming of agent output to
	// Telegram via message editing. When false, falls back to the
	// buffered path (wait for full response, then send).
	// Default: true. Set GOPOD_STREAM_ENABLED=false to disable.
	StreamEnabled bool
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

	compactAfter := 30
	if v := os.Getenv("GOPOD_COMPACT_AFTER"); v != "" {
		fmt.Sscanf(v, "%d", &compactAfter)
	}

	cfg := Config{
		DataDir:                getenvDefault("GOPOD_DATA_DIR", "./data"),
		LogLevel:               parseLevel(getenvDefault("GOPOD_LOG_LEVEL", "info")),
		LogFormat:              strings.ToLower(getenvDefault("GOPOD_LOG_FORMAT", "text")),
		TelegramBotToken:       os.Getenv("TELEGRAM_BOT_TOKEN"),
		ContainerEnabled:       !envFlag("GOPOD_NO_CONTAINER"),
		RepoRoot:               os.Getenv("GOPOD_REPO_ROOT"),
		ContainerImage:         getenvDefault("GOPOD_CONTAINER_IMAGE", "gopod-agent:latest"),
		LeftoverCleanupEnabled: !envFlag("GOPOD_LEFTOVER_CLEANUP_DISABLED"),
		StreamEnabled:          !envFlag("GOPOD_STREAM_DISABLED"),
		CompactAfter:           compactAfter,
		CompactInterval:        os.Getenv("GOPOD_COMPACT_INTERVAL"),
		CompactTime:            os.Getenv("GOPOD_COMPACT_TIME"),
		ObsidianVault:          os.Getenv("GOPOD_OBSIDIAN_VAULT"),
	}

	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolving GOPOD_DATA_DIR=%q: %w", cfg.DataDir, err)
	}
	cfg.DataDir = abs
	cfg.StorePath = filepath.Join(cfg.DataDir, "store.sqlite")

	// Runner/container path defaults. RepoRoot falls back to cwd so
	// `go run ./cmd/gopod` from the project root does the right
	// thing without extra configuration.
	if cfg.RepoRoot == "" {
		wd, err := os.Getwd()
		if err != nil {
			return Config{}, fmt.Errorf("resolving RepoRoot from cwd: %w", err)
		}
		cfg.RepoRoot = wd
	}
	cfg.RepoRoot, err = filepath.Abs(cfg.RepoRoot)
	if err != nil {
		return Config{}, fmt.Errorf("resolving GOPOD_REPO_ROOT=%q: %w", cfg.RepoRoot, err)
	}
	if cfg.ChatsDir == "" {
		cfg.ChatsDir = filepath.Join(cfg.RepoRoot, "chats")
	}
	if cfg.ContainerSkillsDir == "" {
		cfg.ContainerSkillsDir = filepath.Join(cfg.RepoRoot, "container", "skills")
	}
	if cfg.MountAllowlistPath == "" {
		cfg.MountAllowlistPath = filepath.Join(cfg.DataDir, "mount-allowlist.json")
	}
	if cfg.EmptyFile == "" {
		cfg.EmptyFile = filepath.Join(cfg.DataDir, "empty-env")
	}

	if v := os.Getenv("GOPOD_OWNER_CHAT_ID"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parsing GOPOD_OWNER_CHAT_ID=%q: %w", v, err)
		}
		cfg.OwnerChatID = id
	}

	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		return Config{}, fmt.Errorf("GOPOD_LOG_FORMAT=%q: must be text or json", cfg.LogFormat)
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validate enforces invariants that must hold for gopod to start.
// M0 is permissive — only DataDir is required to be non-empty after
// resolution. Future milestones tighten this (e.g. M1 will require
// TELEGRAM_BOT_TOKEN, M2 will require ANTHROPIC_API_KEY).
func (c Config) validate() error {
	if c.DataDir == "" {
		return errors.New("GOPOD_DATA_DIR resolved to empty path")
	}
	return nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envFlag parses an env var as a boolean with the "anything truthy
// means true" convention used across gopod. Accepts 1, true, yes,
// on (case-insensitive). Unset or unrecognised value is false.
func envFlag(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
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
