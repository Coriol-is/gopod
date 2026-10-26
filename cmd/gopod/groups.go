package main

import (
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Coriol-is/gopod/internal/config"
	"github.com/Coriol-is/gopod/internal/groupruntime"
	"github.com/Coriol-is/gopod/internal/groupsandbox"
	"github.com/Coriol-is/gopod/internal/memory"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

func wireGroups(ctx context.Context, cfg config.Config, st *store.Store, r *runner.Runner, mem *memory.Memory, logger *slog.Logger) (*groupruntime.Manager, func(), error) {
	path := os.Getenv("GOPOD_GROUP_CONFIG")
	if path == "" {
		return nil, func() {}, nil
	}
	if os.Geteuid() <= 0 || os.Getegid() <= 0 || r == nil || path != filepath.Join(cfg.DataDir, "group-runtime.json") {
		return nil, nil, errors.New("group runtime configuration unavailable")
	}
	policy, err := groupruntime.Load(path)
	if err != nil {
		return nil, nil, errors.New("group runtime policy unavailable")
	}
	snapshotRoot := filepath.Join(cfg.DataDir, "shared-tool-snapshots")
	if err = os.Mkdir(snapshotRoot, 0700); err != nil && !os.IsExist(err) {
		return nil, nil, errors.New("group tool storage unavailable")
	}
	info, err := os.Lstat(snapshotRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, nil, errors.New("group tool storage unsafe")
	}
	mcp := &groupsandbox.MCP{Executor: &groupsandbox.Executor{Client: r.Docker().Client(), Root: snapshotRoot, Image: policy.ToolImage, UID: os.Geteuid(), GID: os.Getegid()}}
	manager := &groupruntime.Manager{Config: policy, Store: st, Docker: r.Docker().Client(), DataDir: cfg.DataDir, Version: buildVersion(), UID: os.Geteuid(), GID: os.Getegid(), Log: logger, MCP: mcp}
	localMemory := memory.New(st, nil, logger)
	manager.Request = manager.RequestHandler(localMemory)
	if mem == nil {
		r.SetMemory(&memoryCompilerAdapter{mem: localMemory, st: st, ownerID: cfg.OwnerChatID, localOnly: true})
	}
	listener, err := net.Listen("tcp", "0.0.0.0:9877")
	if err != nil {
		return nil, nil, errors.New("group tool listener unavailable")
	}
	server := &http.Server{Handler: mcp, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384, ErrorLog: log.New(io.Discard, "", 0)}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("group tool server stopped")
		}
	}()
	stop := func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(cleanup)
	}
	r.SetGroupRuntime(manager)
	return manager, stop, nil
}

func groupAction(st *store.Store, r *runner.Runner, operation func(context.Context, string) (string, error)) func(context.Context, string) (string, error) {
	return func(ctx context.Context, jid string) (string, error) {
		c, err := st.GetRegistered(ctx, jid)
		if err != nil {
			return "", groupruntime.ErrUnavailable
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return r.WithChatSlot(ctx, c.Folder, func(ctx context.Context) (string, error) { return operation(ctx, jid) })
	}
}
