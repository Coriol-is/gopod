// gopod-group-admin performs explicit offline host maintenance. It never loads
// environment configuration, starts Telegram/scheduler listeners or runs models.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Coriol-is/gopod/internal/groupruntime"
	"github.com/Coriol-is/gopod/internal/groupsandbox"
	"github.com/Coriol-is/gopod/internal/memory"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/docker/docker/client"
)

var errUnavailable = errors.New("group administration unavailable")

type options struct {
	config, data, database, jid, controller, action string
	expected                                        int64
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_, _ = io.WriteString(os.Stderr, "group administration unavailable\n")
		os.Exit(1)
	}
}
func parse(args []string) (options, error) {
	var o options
	f := flag.NewFlagSet("gopod-group-admin", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.config, "config", "", "explicit protected group config")
	f.StringVar(&o.data, "data", "", "absolute host DataDir")
	f.StringVar(&o.database, "store", "", "explicit existing store.sqlite")
	f.StringVar(&o.jid, "jid", "", "one canonical registered group JID")
	f.StringVar(&o.controller, "controller-name", "", "existing stopped compose controller name")
	f.StringVar(&o.action, "action", "", "status|prepare|enable|disable")
	f.Int64Var(&o.expected, "expected-revision", 0, "required registration revision for a mutation")
	if f.Parse(args) != nil || len(f.Args()) != 0 || o.controller == "" || strings.ContainsAny(o.controller, "/\\\x00\r\n") {
		return o, errUnavailable
	}
	if !filepath.IsAbs(o.data) || filepath.Clean(o.data) != o.data || o.database != filepath.Join(o.data, "store.sqlite") || o.config != filepath.Join(o.data, "group-runtime.json") {
		return o, errUnavailable
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(o.jid, "tg:"), 10, 64)
	if err != nil || id >= 0 || o.jid != "tg:"+strconv.FormatInt(id, 10) {
		return o, errUnavailable
	}
	switch o.action {
	case "status":
		if o.expected < 0 {
			return o, errUnavailable
		}
	case "prepare", "enable", "disable":
		if o.expected < 1 {
			return o, errUnavailable
		}
	default:
		return o, errUnavailable
	}
	return o, nil
}
func run(args []string, out io.Writer) error {
	o, err := parse(args)
	if err != nil {
		return err
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if uid <= 0 || gid <= 0 {
		return errUnavailable
	}
	unlock, err := adminLock(o.data, uid)
	if err != nil {
		return errUnavailable
	}
	defer unlock()
	if regularFile(o.database) != nil || regularFile(o.config) != nil {
		return errUnavailable
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, 45*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	docker, err := client.NewClientWithOpts(client.WithHost(client.DefaultDockerHost), client.WithAPIVersionNegotiation())
	if err != nil {
		return errUnavailable
	}
	defer docker.Close()
	stopped := func(ctx context.Context) error { return controllerStopped(ctx, docker, o.controller, o.data) }
	// Require an existing matching controller, not a misspelled absent name.
	if stopped(ctx) != nil {
		return errUnavailable
	}
	cfg, err := groupruntime.Load(o.config)
	if err != nil {
		return errUnavailable
	}
	st, err := store.Open(ctx, o.database, logger)
	if err != nil {
		return errUnavailable
	}
	defer st.Close()
	mcp := &groupsandbox.MCP{Executor: &groupsandbox.Executor{Client: docker, Root: filepath.Join(o.data, "shared-tool-snapshots"), Image: cfg.ToolImage, UID: uid, GID: gid}}
	manager := &groupruntime.Manager{Config: cfg, Store: st, Docker: docker, DataDir: o.data, Version: "group-admin", UID: uid, GID: gid, Log: logger, MCP: mcp}
	manager.Request = manager.RequestHandler(memory.New(st, nil, logger))
	result, err := execute(ctx, o, st, manager, stopped)
	if err != nil {
		return errUnavailable
	}
	current, err := st.GetRegistered(ctx, o.jid)
	if err != nil {
		return errUnavailable
	}
	return json.NewEncoder(out).Encode(map[string]any{"registration_revision": current.RegistrationRevision, "namespace_epoch": current.NamespaceEpoch, "status": "ok", "action": o.action, "chat_jid": o.jid, "message": result, "inference_checked": false, "telegram_messages": 0})
}

type registrationStore interface {
	GetRegistered(context.Context, string) (store.RegisteredChat, error)
	ValidateScope(context.Context, store.ScopeRef) error
}
type actions interface {
	Status(context.Context, string) (string, error)
	Prepare(context.Context, string) (string, error)
	Enable(context.Context, string) (string, error)
	Disable(context.Context, string) (string, error)
}

func execute(ctx context.Context, o options, st registrationStore, manager actions, stopped func(context.Context) error) (string, error) {
	c, err := st.GetRegistered(ctx, o.jid)
	if err != nil || c.IsOwner || c.JID != o.jid || st.ValidateScope(ctx, c.Scope()) != nil {
		return "", errUnavailable
	}
	if o.expected != 0 && o.expected != c.RegistrationRevision {
		return "", errUnavailable
	}
	if o.action != "status" && o.expected < 1 {
		return "", errUnavailable
	}
	// Recheck after policy/store initialization, immediately before maintenance.
	if ctx.Err() != nil || stopped(ctx) != nil {
		return "", errUnavailable
	}
	switch o.action {
	case "status":
		return manager.Status(ctx, o.jid)
	case "prepare":
		return manager.Prepare(ctx, o.jid)
	case "enable":
		return manager.Enable(ctx, o.jid)
	case "disable":
		return manager.Disable(ctx, o.jid)
	}
	return "", errUnavailable
}
