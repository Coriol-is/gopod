package log_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	picoclog "github.com/Coriol-is/gopod/internal/log"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestSecretsRedactedBeforeBothSinks(t *testing.T) {
	const configured = "configured-secret-canary"
	const telegram = "123456:telegram-canary-abc"
	t.Setenv("TEST_PASSWORD", configured)
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "store.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var stderr bytes.Buffer
	h := picoclog.NewSQLiteHandler(st.DB(), slog.NewJSONHandler(&stderr, nil))
	logger := slog.New(h).With("subsys", "download", "nested", slog.GroupValue(slog.String("password", "nested-secret-canary")))
	logger.Error("failed "+configured, "err", &url.Error{Op: "Get", URL: "https://api.telegram.org/file/bot" + telegram + "/x", Err: errors.New("connection failed")}, "details", "Bearer bearer-canary", "private", configured)
	h.Close()
	rows, err := picoclog.QueryLogs(st.DB(), "", "", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	output := stderr.String() + fmt.Sprint(rows)
	for _, secret := range []string{configured, telegram, "nested-secret-canary", "bearer-canary"} {
		if strings.Contains(output, secret) {
			t.Errorf("secret canary leaked")
		}
	}
	if !strings.Contains(output, "connection failed") || !strings.Contains(output, "redacted") {
		t.Fatal("lost useful failure context")
	}
}

func TestBoundedWriterClonesAttrsAndClosesAcrossDerivedHandlers(t *testing.T) {
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "store.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := picoclog.NewSQLiteHandler(st.DB(), slog.NewTextHandler(io.Discard, nil))
	left := slog.New(h).With("branch", "left")
	right := slog.New(h).With("branch", "right")
	var wg sync.WaitGroup
	for _, logger := range []*slog.Logger{left, right} {
		wg.Add(1)
		go func(l *slog.Logger) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				l.Info("entry", "i", i)
			}
		}(logger)
	}
	wg.Wait()
	h.Close()
	rows, err := picoclog.QueryLogs(st.DB(), "", "", 5000)
	if err != nil || len(rows) == 0 {
		t.Fatalf("%d %v", len(rows), err)
	}
	for _, row := range rows {
		if !strings.Contains(row.AttrsJSON, "left") && !strings.Contains(row.AttrsJSON, "right") {
			t.Fatal("derived attrs lost")
		}
	}
	before := len(rows)
	left.Info("after close")
	h.Close()
	rows, err = picoclog.QueryLogs(st.DB(), "", "", 5000)
	if err != nil || len(rows) != before {
		t.Fatal("write after close")
	}
}

func TestBaseReplaceAttrRedactsMessagesAndNestedValues(t *testing.T) {
	t.Setenv("TEST_SECRET", "base-secret-canary")
	var out bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{ReplaceAttr: picoclog.NewRedactor().ReplaceAttr}))
	logger.WithGroup("authorization").Info("group", "value", "group-secret-canary")
	logger.LogAttrs(context.Background(), slog.LevelError, "base-secret-canary", slog.Group("detail", slog.Any("err", errors.New("base-secret-canary"))), slog.String("auth", "hidden"))
	if strings.Contains(out.String(), "base-secret-canary") || strings.Contains(out.String(), "hidden") || strings.Contains(out.String(), "group-secret-canary") {
		t.Fatal("base stderr leaked")
	}
}
