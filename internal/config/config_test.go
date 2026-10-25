package config

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("GOPOD_DATA_DIR", "")
	t.Setenv("GOPOD_LOG_LEVEL", "")
	t.Setenv("GOPOD_LOG_FORMAT", "")
	t.Setenv("GOPOD_OWNER_CHAT_ID", "")
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
	t.Setenv("GOPOD_OWNER_CHAT_ID", "123456789")
	cfg, err := Load(false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OwnerChatID != 123456789 {
		t.Errorf("OwnerChatID = %d, want 123456789", cfg.OwnerChatID)
	}
}

func TestLoadInvalidOwnerChatID(t *testing.T) {
	t.Setenv("GOPOD_OWNER_CHAT_ID", "not-a-number")
	if _, err := Load(false); err == nil {
		t.Fatal("Load: want error for non-numeric OwnerChatID, got nil")
	}
}

func TestLoadInvalidLogFormat(t *testing.T) {
	t.Setenv("GOPOD_LOG_FORMAT", "yaml")
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

func TestLoadNotifyLifecycle(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"", true}, {"1", true}, {"true", true}, {"ON", true},
		{"0", false}, {"false", false}, {"no", false}, {" Off ", false},
		{"maybe", true},
	} {
		t.Setenv("GOPOD_NOTIFY_LIFECYCLE", tc.val)
		cfg, err := Load(false)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.NotifyLifecycle != tc.want {
			t.Errorf("GOPOD_NOTIFY_LIFECYCLE=%q: NotifyLifecycle = %v, want %v", tc.val, cfg.NotifyLifecycle, tc.want)
		}
	}
}

func TestLoadProgressFlag(t *testing.T) {
	for val, want := range map[string]bool{"": true, "1": true, "true": true, "0": false, "false": false, "OFF": false, "no": false, "junk": true} {
		t.Setenv("GOPOD_PROGRESS", val)
		cfg, err := Load(false)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.ProgressEnabled != want {
			t.Errorf("GOPOD_PROGRESS=%q: ProgressEnabled = %v, want %v", val, cfg.ProgressEnabled, want)
		}
	}
}

func TestLoadAgentAdmissionDefaults(t *testing.T) {
	t.Setenv("GOPOD_MAX_AGENTS", "")
	t.Setenv("GOPOD_MIN_AVAILABLE_MB", "")
	t.Setenv("GOPOD_AGENT_NODE_HEAP_MB", "")
	cfg, err := Load(false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxAgents != 2 || cfg.MinAvailableMB != 900 || cfg.AgentNodeHeapMB != 768 {
		t.Fatalf("got max=%d min=%d heap=%d, want 2, 900, 768",
			cfg.MaxAgents, cfg.MinAvailableMB, cfg.AgentNodeHeapMB)
	}
}

func TestLoadAgentAdmissionOverrides(t *testing.T) {
	cases := []struct {
		max, min, heap          string
		wantMax, wantMin, wantH int
	}{
		{"3", "1200", "512", 3, 1200, 512},
		{"1", "0", "0", 1, 0, 0},   // 0 disables the memory check and the heap cap
		{"0", "-5", "-1", 1, 0, 0}, // max is at least 1; negatives clamp to off
		{" 4 ", "900", "768", 4, 900, 768},
	}
	for _, c := range cases {
		t.Setenv("GOPOD_MAX_AGENTS", c.max)
		t.Setenv("GOPOD_MIN_AVAILABLE_MB", c.min)
		t.Setenv("GOPOD_AGENT_NODE_HEAP_MB", c.heap)
		cfg, err := Load(false)
		if err != nil {
			t.Fatalf("%+v: Load: %v", c, err)
		}
		if cfg.MaxAgents != c.wantMax || cfg.MinAvailableMB != c.wantMin || cfg.AgentNodeHeapMB != c.wantH {
			t.Errorf("%+v: got max=%d min=%d heap=%d", c, cfg.MaxAgents, cfg.MinAvailableMB, cfg.AgentNodeHeapMB)
		}
	}
}

func TestLoadAgentAdmissionInvalid(t *testing.T) {
	for _, key := range []string{"GOPOD_MAX_AGENTS", "GOPOD_MIN_AVAILABLE_MB", "GOPOD_AGENT_NODE_HEAP_MB"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("GOPOD_MAX_AGENTS", "")
			t.Setenv("GOPOD_MIN_AVAILABLE_MB", "")
			t.Setenv("GOPOD_AGENT_NODE_HEAP_MB", "")
			t.Setenv(key, "lots")
			if _, err := Load(false); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("err = %v, want one naming %s", err, key)
			}
		})
	}
}
