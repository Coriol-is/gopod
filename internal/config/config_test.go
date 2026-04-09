package config

import (
	"log/slog"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("PICOCLAW_DATA_DIR", "")
	t.Setenv("PICOCLAW_LOG_LEVEL", "")
	t.Setenv("PICOCLAW_LOG_FORMAT", "")
	t.Setenv("PICOCLAW_OWNER_CHAT_ID", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")

	cfg, err := Load(false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if cfg.LogFormat != "text" {
		t.Errorf("LogFormat = %q, want text", cfg.LogFormat)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Errorf("DataDir = %q, want absolute path", cfg.DataDir)
	}
	if cfg.StorePath != filepath.Join(cfg.DataDir, "store.sqlite") {
		t.Errorf("StorePath = %q, want %q", cfg.StorePath, filepath.Join(cfg.DataDir, "store.sqlite"))
	}
	if cfg.OwnerChatID != 0 {
		t.Errorf("OwnerChatID = %d, want 0 (unset)", cfg.OwnerChatID)
	}
	if cfg.TelegramBotToken != "" {
		t.Errorf("TelegramBotToken = %q, want empty", cfg.TelegramBotToken)
	}
}

func TestLoadTelegramBotToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "1234:abc")
	cfg, err := Load(false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TelegramBotToken != "1234:abc" {
		t.Errorf("TelegramBotToken = %q, want 1234:abc", cfg.TelegramBotToken)
	}
}

func TestLoadOwnerChatID(t *testing.T) {
	t.Setenv("PICOCLAW_OWNER_CHAT_ID", "123456789")
	cfg, err := Load(false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OwnerChatID != 123456789 {
		t.Errorf("OwnerChatID = %d, want 123456789", cfg.OwnerChatID)
	}
}

func TestLoadInvalidOwnerChatID(t *testing.T) {
	t.Setenv("PICOCLAW_OWNER_CHAT_ID", "not-a-number")
	if _, err := Load(false); err == nil {
		t.Fatal("Load: want error for non-numeric OwnerChatID, got nil")
	}
}

func TestLoadInvalidLogFormat(t *testing.T) {
	t.Setenv("PICOCLAW_LOG_FORMAT", "yaml")
	if _, err := Load(false); err == nil {
		t.Fatal("Load: want error for invalid log format, got nil")
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"":        slog.LevelInfo, // default
		"garbage": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}
