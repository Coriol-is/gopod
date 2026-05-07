package ipc

import (
	"testing"
	"time"
)

func TestNewFillsDefaults(t *testing.T) {
	w := New(Config{}, nil, nil, nil, nil)
	if w.cfg.Tick != time.Second {
		t.Errorf("Tick = %v, want 1s", w.cfg.Tick)
	}
	if w.cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", w.cfg.MaxRetries)
	}
	if w.cfg.Retention != 200 {
		t.Errorf("Retention = %d, want 200", w.cfg.Retention)
	}
	if len(w.cfg.RetryBackoff) != 3 {
		t.Errorf("RetryBackoff len = %d, want 3", len(w.cfg.RetryBackoff))
	}
	if w.cfg.NowFn == nil {
		t.Error("NowFn = nil, want non-nil")
	}
}
