package telegram

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
)

func TestRestartPollsStoredNextOffsetAndDispatchesFirstCommand(t *testing.T) {
	for _, nextOffset := range []int64{1, 41} {
		t.Run(strconv.FormatInt(nextOffset, 10), func(t *testing.T) {
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "store.sqlite"), log)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.SetState(t.Context(), "telegram_update_offset", strconv.FormatInt(nextOffset, 10)); err != nil {
				t.Fatal(err)
			}
			router := control.New(log)
			control.RegisterBuiltins(router)
			b, err := New("123:test", Deps{Store: st, Log: log, Router: router, Runner: &runner.Runner{}, OwnerChatID: streamChat})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			firstOffset := make(chan string, 1)
			replied := make(chan string, 1)
			var once sync.Once
			fake := &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
				switch method {
				case "getUpdates":
					first := false
					once.Do(func() { first = true; firstOffset <- form["offset"] })
					if first && form["offset"] == strconv.FormatInt(nextOffset, 10) {
						return http.StatusOK, fmt.Sprintf(`{"ok":true,"result":[{"update_id":%d,"message":{"message_id":10,"date":0,"chat":{"id":1001,"type":"private"},"from":{"id":1001,"is_bot":false,"first_name":"test"},"text":"/ping"}}]}`, nextOffset)
					}
					<-ctx.Done()
					return http.StatusOK, `{"ok":true,"result":[]}`
				case "sendMessage":
					replied <- form["text"]
					return sentMessage("11")
				}
				return http.StatusOK, `{"ok":true,"result":true}`
			}}
			srv := httptest.NewServer(fake)
			defer srv.Close()
			// Apply only the test endpoint; retain New's real resume options.
			bot.WithServerURL(srv.URL)(b.api)
			done := make(chan struct{})
			go func() { b.api.Start(ctx); b.inboundHandlers.Wait(); close(done) }()
			defer func() { cancel(); <-done }()
			select {
			case got := <-firstOffset:
				if got != strconv.FormatInt(nextOffset, 10) {
					t.Fatalf("first poll offset=%s, want %d", got, nextOffset)
				}
			case <-ctx.Done():
				t.Fatal("no first poll")
			}
			select {
			case got := <-replied:
				if got != "pong" {
					t.Fatalf("first command response=%q", got)
				}
			case <-ctx.Done():
				t.Fatal("first unseen command was not dispatched")
			}
			cancel()
			<-done
			if got, err := st.GetState(t.Context(), "telegram_update_offset"); err != nil || got != strconv.FormatInt(nextOffset+1, 10) {
				t.Fatalf("persisted next offset=%q, err=%v", got, err)
			}
		})
	}
}
