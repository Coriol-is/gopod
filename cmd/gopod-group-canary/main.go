// gopod-group-canary qualifies only a disposable namespace. No Telegram client,
// production store, scheduler or inherited service env is used.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Coriol-is/gopod/internal/groupruntime"
	"github.com/Coriol-is/gopod/internal/groupsandbox"
	"github.com/Coriol-is/gopod/internal/memory"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

func main() {
	if err := run(); err != nil {
		var admission *groupruntime.AdmissionError
		if errors.As(err, &admission) {
			fmt.Fprintln(os.Stderr, "admission_stage="+admission.Stage)
		}
		fmt.Println(`{"status":"unavailable","inference_qualified":false}`)
		os.Exit(1)
	}
}
func run() error {
	configPath := flag.String("config", "", "host-only explicit group configuration")
	provider := flag.String("provider", "claude", "pinned native provider")
	parent := flag.String("parent", "", "protected host directory for disposable data")
	authOnly := flag.Bool("auth-only", false, "do not make any model request")
	flag.Parse()
	if !filepath.IsAbs(*parent) || (*provider != "claude" && *provider != "codex") {
		return errors.New("parameters unavailable")
	}
	fmt.Fprintln(os.Stderr, "phase=config")
	cfg, err := groupruntime.Load(*configPath)
	if err != nil {
		return err
	}
	if len(cfg.EnabledGroups) != 1 {
		return errors.New("one explicit sandbox group required")
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, 4*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp(*parent, "group-canary-")
	if err != nil {
		return err
	}
	// Retain only on unknown cleanup; never delete somebody else's workspace.
	cleanupKnown := true
	defer func() {
		if cleanupKnown {
			_ = os.RemoveAll(dir)
		}
	}()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(ctx, filepath.Join(dir, "store.sqlite"), logger)
	if err != nil {
		return err
	}
	defer st.Close()
	jid := cfg.EnabledGroups[0]
	if err = st.UpsertChat(ctx, store.ChatRecord{JID: jid, IsGroup: true, LastMessageTime: time.Now().UnixMilli()}); err != nil {
		return err
	}
	if err = st.RegisterChat(ctx, store.RegisteredChat{JID: jid, Folder: "canary-group", AddedAt: time.Now().UnixMilli()}); err != nil {
		return err
	}
	docker, err := runner.NewDocker(ctx, logger)
	if err != nil {
		return err
	}
	defer docker.Close()
	root := filepath.Join(dir, "snapshots")
	if err = os.Mkdir(root, 0700); err != nil {
		return err
	}
	mcp := &groupsandbox.MCP{Executor: &groupsandbox.Executor{Client: docker.Client(), Root: root, Image: cfg.ToolImage, UID: os.Geteuid(), GID: os.Getegid()}}
	manager := &groupruntime.Manager{Config: cfg, Store: st, Docker: docker.Client(), DataDir: dir, Version: "group-canary", UID: os.Geteuid(), GID: os.Getegid(), Log: logger, MCP: mcp}
	manager.Request = manager.RequestHandler(memory.New(st, nil, logger))
	l, err := net.Listen("tcp", "0.0.0.0:9877")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mcp, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, MaxHeaderBytes: 16384, ErrorLog: log.New(io.Discard, "", 0)}
	go func() { _ = server.Serve(l) }()
	defer server.Close()
	fmt.Fprintln(os.Stderr, "phase=prepare")
	if _, err = manager.Prepare(ctx, jid); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "phase=enable")
	if _, err = manager.Enable(ctx, jid); err != nil {
		return err
	}
	c, err := st.GetRegistered(ctx, jid)
	if err != nil {
		return err
	}
	var base runner.AgentProvider = runner.NewClaudeProvider("")
	if *provider == "codex" {
		base = runner.NewCodexProvider("")
	}
	r, err := runner.New(docker, base, runner.Paths{RepoRoot: dir, DataDir: dir, ChatsDir: filepath.Join(dir, "unused-personal"), ContainerSkillsDir: filepath.Join(dir, "unused-skills"), EmptyFile: filepath.Join(dir, "unused-env")}, runner.SpawnDefaults{Image: base.Image(), UID: os.Geteuid(), GID: os.Getegid(), MemoryBytes: 768 << 20, NanoCPUs: 1e9, PidsLimit: 64}, nil, "group-canary", logger)
	if err != nil {
		return err
	}
	r.SetStore(st)
	r.SetAdmissionPolicy(1, st)
	r.SetGroupRuntime(manager)
	r.SetProgress(true)
	r.SetAgentLimits(runner.AgentLimits{MaxAgents: 1, MinAvailableMB: 0, NodeHeapMB: 384})
	r.SetCompactAfter(0)
	defer func() {
		v, e := st.GetState(context.Background(), "group_cleanup_unknown:"+c.NamespaceID)
		if e != nil || v == "1" {
			cleanupKnown = false
		}
	}()
	fmt.Fprintln(os.Stderr, "phase=runtime")
	if *authOnly {
		_, _, release, err := manager.Start(ctx, c, base, 0)
		if err != nil {
			return err
		}
		release()
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "ok", "auth_only": true, "provider": *provider, "inference_qualified": false})
	}
	if *provider == "claude" {
		r.SetChatModel(c.Folder, "claude-haiku-4-5")
	}
	// Exactly two bounded top-level turns; MCP commands cannot send network
	// traffic and no IPC watcher exists in this disposable store.
	nonceBytes := make([]byte, 8)
	if _, err = rand.Read(nonceBytes); err != nil {
		return err
	}
	nonce := "CONTEXT_" + hex.EncodeToString(nonceBytes)
	first := "Remember this synthetic shared-dialog phrase for the next turn only: " + nonce + ". Never write that phrase to the tool workspace. This is a synthetic isolation canary. Use only group_exec once to run: test ! -e /var/run/docker.sock && test ! -d /workspace/vault && test -z \"$OPENAI_API_KEY\" && test -z \"$FREEFEED_PASSWORD\" && printf GROUP_CANARY_OK > proof.txt && cat proof.txt. Then reply only GROUP_CANARY_OK. Do not call group_request or any network/service tool."
	fmt.Fprintln(os.Stderr, "phase=new")
	answer, err := r.Run(ctx, c.Folder, 0, runner.TierRegistered, nil, first)
	if err != nil || !strings.Contains(answer, "GROUP_CANARY_OK") {
		return errors.New("new turn unqualified")
	}
	id1, ok, err := st.GetSession(ctx, c.Folder, 0, *provider)
	if err != nil || !ok || id1 == "" {
		return errors.New("native session ID unavailable")
	}
	snapshot, err := os.ReadFile(filepath.Join(root, c.Folder, "snapshot.tar"))
	if err != nil || strings.Contains(string(snapshot), nonce) {
		return errors.New("context canary contaminated tool snapshot")
	}
	second := "Resume the synthetic canary. Use group_exec once to read proof.txt. Reply GROUP_CANARY_OK followed by the exact synthetic shared-dialog phrase from the previous turn; it is not a credential. Do not call group_request or any other tool."
	fmt.Fprintln(os.Stderr, "phase=resume")
	answer, err = r.Run(ctx, c.Folder, 0, runner.TierRegistered, nil, second)
	if err != nil || !strings.Contains(answer, "GROUP_CANARY_OK") || !strings.Contains(answer, nonce) {
		return errors.New("resumed turn unqualified")
	}
	id2, ok, err := st.GetSession(ctx, c.Folder, 0, *provider)
	if err != nil || !ok || id2 != id1 {
		return errors.New("native resume identity changed")
	}
	for _, label := range []string{"gopod.group-native=" + c.NamespaceID, "gopod.group-tool=" + c.Folder} {
		left, e := docker.Client().ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", label))})
		if e != nil || len(left) > 0 {
			cleanupKnown = false
			return errors.New("cleanup unverified")
		}
	}
	if manager.Authorize(ctx, c) != nil {
		return errors.New("final scope unavailable")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "ok", "provider": *provider, "new": true, "resume": true, "inference_qualified": true, "personal_service_grants": 0})
}
