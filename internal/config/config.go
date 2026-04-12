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
	// to start (with a warning log) and the rest of picoclaw still runs.
	// This keeps the M0-style "binary that just opens the store" mode
	// available for diagnostics and tests.
	//
	// Required from M1 onward for any actual messaging work.
	TelegramBotToken string

	// --- Runner / container runtime (M5+) ---------------------------

	// ContainerEnabled is true unless PICOCLAW_NO_CONTAINER=1.
	//
	// When false, picoclaw skips the Docker subsystem entirely (no
	// client open, no leftover cleanup). This is a dev-only escape
	// hatch; there is no working host-subprocess agent path yet
	// (see docs/ROADMAP.md notes on the NO_CONTAINER stub).
	ContainerEnabled bool

	// RepoRoot is the absolute path to the picoclaw checkout. The
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
	// not an error — picoclaw runs with only the standard mounts.
	MountAllowlistPath string

	// EmptyFile masks ${REPO_ROOT}/.env inside the owner agent
	// container. EnsureChatDirs creates it if missing. Defaults to
	// ${DataDir}/empty-env.
	EmptyFile string

	// ContainerImage is the image tag the runner spawns per chat.
	ContainerImage string

	// LeftoverCleanupEnabled controls the boot-time cleanup pass in
	// lifecycle.go. Default true; set PICOCLAW_LEFTOVER_CLEANUP=0 to
	// disable (debug only).
	LeftoverCleanupEnabled bool
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
	if v := os.Getenv("PICOCLAW_COMPACT_AFTER"); v != "" {
		fmt.Sscanf(v, "%d", &compactAfter)
	}

	cfg := Config{
		DataDir:                getenvDefault("PICOCLAW_DATA_DIR", "./data"),
		LogLevel:               parseLevel(getenvDefault("PICOCLAW_LOG_LEVEL", "info")),
		LogFormat:              strings.ToLower(getenvDefault("PICOCLAW_LOG_FORMAT", "text")),
		TelegramBotToken:       os.Getenv("TELEGRAM_BOT_TOKEN"),
		ContainerEnabled:       !envFlag("PICOCLAW_NO_CONTAINER"),
		RepoRoot:               os.Getenv("PICOCLAW_REPO_ROOT"),
		ContainerImage:         getenvDefault("PICOCLAW_CONTAINER_IMAGE", "picoclaw-agent:latest"),
		LeftoverCleanupEnabled: !envFlag("PICOCLAW_LEFTOVER_CLEANUP_DISABLED"),
		CompactAfter:           compactAfter,
		CompactInterval:        os.Getenv("PICOCLAW_COMPACT_INTERVAL"),
		CompactTime:            os.Getenv("PICOCLAW_COMPACT_TIME"),
		ObsidianVault:          os.Getenv("PICOCLAW_OBSIDIAN_VAULT"),
	}

	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolving PICOCLAW_DATA_DIR=%q: %w", cfg.DataDir, err)
	}
	cfg.DataDir = abs
	cfg.StorePath = filepath.Join(cfg.DataDir, "store.sqlite")

	// Runner/container path defaults. RepoRoot falls back to cwd so
	// `go run ./cmd/picoclaw` from the project root does the right
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
		return Config{}, fmt.Errorf("resolving PICOCLAW_REPO_ROOT=%q: %w", cfg.RepoRoot, err)
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

// envFlag parses an env var as a boolean with the "anything truthy
// means true" convention used across picoclaw. Accepts 1, true, yes,
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
