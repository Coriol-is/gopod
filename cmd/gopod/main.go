// Command gopod is the personal Telegram Claude assistant.
//
// As of M5: loads config, opens the SQLite store, wires slog, starts
// the Telegram long-poll loop (M1), opens the Docker client and runs
// the boot-time leftover cleanup pass (M5), and blocks on SIGINT/SIGTERM.
// No agent loop yet — that lands in M6.
//
// Subsystems are layered so each one can be skipped independently:
//
//   - Store always opens (it holds every other subsystem's state).
//   - Telegram starts only if TELEGRAM_BOT_TOKEN is set.
//   - The runner/Docker subsystem starts only if GOPOD_NO_CONTAINER
//     is not truthy AND the Docker daemon is reachable.
//
// This keeps the "store-only" diagnostic mode available even if
// Telegram or Docker are unreachable.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"
	"runtime/debug"
	"sync"
	"syscall"

	picoclog "github.com/spaceinvaderz/gopod/internal/log"
	"github.com/spaceinvaderz/gopod/internal/config"
	"github.com/spaceinvaderz/gopod/internal/control"
	"github.com/spaceinvaderz/gopod/internal/memory"
	"github.com/spaceinvaderz/gopod/internal/queue"
	"github.com/spaceinvaderz/gopod/internal/runner"
	"github.com/spaceinvaderz/gopod/internal/runner/mountsec"
	"github.com/spaceinvaderz/gopod/internal/scheduler"
	"github.com/spaceinvaderz/gopod/internal/store"
	"github.com/spaceinvaderz/gopod/internal/telegram"
)

// version is overridden at link time via -ldflags "-X main.version=...".
// In dev builds it falls back to the VCS info embedded by `go build`.
var version = "dev"

func main() {
	if err := run(); err != nil {
		// run() may exit before slog is wired, so go to stderr.
		fmt.Fprintf(os.Stderr, "gopod: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dotenv := flag.Bool("dotenv", true, "load .env from the working directory if present")
	flag.Parse()

	cfg, err := config.Load(*dotenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := newLogger(cfg)
	slog.SetDefault(logger)
	// After store opens, we'll upgrade the logger to also write to SQLite.

	logger.Info("gopod starting",
		slog.String("version", buildVersion()),
		slog.String("go", runtime.Version()),
		slog.String("data_dir", cfg.DataDir),
		slog.String("store_path", cfg.StorePath),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.StorePath, logger.With(slog.String("subsys", "store")))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Error("store close", slog.Any("err", err))
		}
	}()

	// Upgrade logger to also write to SQLite logs table.
	sqliteHandler := picoclog.NewSQLiteHandler(st.DB(), logger.Handler())
	logger = slog.New(sqliteHandler)
	slog.SetDefault(logger)

	var subsystems sync.WaitGroup

	// Runner / Docker subsystem. Optional at boot: if GOPOD_NO_CONTAINER
	// is set, or the daemon is unreachable, gopod logs and continues
	// in "no runner" mode (telegram still echoes /ping).
	var (
		agentRunner *runner.Runner
		allowlist   *mountsec.Allowlist
		agentMemory *memory.Memory
	)
	if !cfg.ContainerEnabled {
		logger.Warn("runner subsystem skipped: GOPOD_NO_CONTAINER is truthy")
	} else {
		runLog := logger.With(slog.String("subsys", "runner"))
		d, err := runner.NewDocker(ctx, runLog)
		if err != nil {
			// Failure to reach Docker is not fatal for store-only /
			// telegram-echo modes. M6 will tighten this once the agent
			// loop depends on it.
			runLog.Warn("docker unavailable, runner subsystem disabled",
				slog.Any("err", err))
		} else {
			defer func() {
				if err := d.Close(); err != nil {
					runLog.Error("docker close", slog.Any("err", err))
				}
			}()

			// Load the mount allowlist. A missing file is fine — callers
			// get an empty allowlist back and construct only the standard
			// mounts. A malformed file is fatal per D013.
			al, err := mountsec.Load(cfg.MountAllowlistPath)
			if err != nil {
				return fmt.Errorf("mount allowlist: %w", err)
			}
			runLog.Info("mount allowlist loaded",
				slog.String("path", cfg.MountAllowlistPath),
				slog.Int("extras", len(al.ExtraMounts)))
			allowlist = al

			// Boot-time leftover cleanup per ISOLATION.md §7.1.
			if cfg.LeftoverCleanupEnabled {
				n, err := d.CleanupLeftovers(ctx, buildVersion())
				if err != nil {
					runLog.Warn("leftover cleanup failed",
						slog.Any("err", err))
				} else {
					runLog.Info("leftover cleanup complete",
						slog.Int("cleaned", n))
				}
			}

			// Build the high-level Runner facade. UID/GID come from the
			// host process so bind-mount writes stay owned by the
			// operator on disk per ISOLATION.md §6.1.
			defaults := runner.SpawnDefaults{
				Image:       cfg.ContainerImage,
				UID:         os.Getuid(),
				GID:         os.Getgid(),
				MemoryBytes: 4 << 30,    // 4 GiB; GOPOD_CONTAINER_MEM later
				NanoCPUs:    2_000_000_000, // 2 CPUs
				PidsLimit:   1024,
			}
			// Create the agent provider. Currently always Claude;
			// P3/P4 will add per-chat provider selection.
			agentProvider := runner.NewClaudeProvider(cfg.ContainerImage)

			// Additional env vars beyond what the provider requires.
			envAllow := []string{"OPENAI_API_KEY"}
			r, err := runner.New(d, agentProvider, runner.Paths{
				RepoRoot:           cfg.RepoRoot,
				DataDir:            cfg.DataDir,
				ChatsDir:           cfg.ChatsDir,
				ContainerSkillsDir: cfg.ContainerSkillsDir,
				EmptyFile:          cfg.EmptyFile,
				ObsidianVault:      cfg.ObsidianVault,
			}, defaults, envAllow, buildVersion(), runLog)
			if err != nil {
				return fmt.Errorf("init runner: %w", err)
			}
			agentRunner = r
			r.SetStore(st)
			if cfg.CompactAfter > 0 {
				r.SetCompactAfter(cfg.CompactAfter)
			}

			// Start compact watcher for interval/daily triggers.
			var compactInterval time.Duration
			if cfg.CompactInterval != "" {
				compactInterval, _ = time.ParseDuration(cfg.CompactInterval)
			}
			r.StartCompactWatcher(ctx, runner.CompactConfig{
				After:     cfg.CompactAfter,
				Interval:  compactInterval,
				TimeOfDay: cfg.CompactTime,
			}, allowlist)

			runLog.Info("runner ready",
				slog.String("image", defaults.Image),
				slog.Int("uid", defaults.UID),
				slog.Int("gid", defaults.GID))

			// Idle watcher: a single goroutine that scans every active
			// chat once a minute and stops + removes containers that
			// have been idle for longer than DefaultIdleTimeout (30m).
			// Exits when ctx is cancelled by SIGINT/SIGTERM.
			r.StartIdleWatcher(ctx, runner.DefaultIdleTimeout)

			// Memory layer (M9). OpenAI embedder + hybrid search.
			// Optional: if OPENAI_API_KEY is unset, memory is disabled.
			embedder, err := memory.NewOpenAIEmbedder("", "", 0)
			if err != nil {
				runLog.Warn("memory disabled: embedder unavailable",
					slog.Any("err", err))
			} else {
				agentMemory = memory.New(st, embedder, runLog)
				r.SetMemory(&memoryCompilerAdapter{mem: agentMemory})

				// Session compact callback: summarize + store + supersede.
				r.SetCompactFn(func(ctx context.Context, chatFolder string) error {
					promptFn := func(c context.Context, f, p string) (string, error) {
						return r.RunFresh(c, f, runner.TierRegistered, allowlist, p)
					}
					summary, err := promptFn(ctx, chatFolder,
						"Summarize the key decisions, facts, and in-progress tasks from our conversation. "+
							"Be concise. Preserve any multi-step work that's not yet finished. "+
							"Return ONLY the summary, no preamble.")
					if err != nil {
						return err
					}
					if summary != "" {
						agentMemory.Add(ctx, chatFolder, "conversation_summary", "session compact", summary, "compact")
					}
					return nil
				})

				// Phase A: extraction callback. After each agent turn,
				// sends the conversation through an extraction prompt
				// to Claude, stores structured facts. Uses the same
				// runner.Run as taskPromptFn for the LLM call.
				r.SetExtractFn(func(ctx context.Context, chatFolder, userMsg, agentReply string) {
					promptFn := func(c context.Context, f, p string) (string, error) {
						return r.RunFresh(c, f, runner.TierRegistered, allowlist, p)
					}
					facts, err := agentMemory.ExtractFromTurn(ctx, chatFolder, userMsg, agentReply, promptFn)
					if err != nil {
						runLog.Warn("extraction failed", slog.Any("err", err))
						return
					}
					if len(facts) > 0 {
						agentMemory.IngestExtracted(ctx, chatFolder, facts)
						runLog.Debug("extracted facts from turn",
							slog.String("chat", chatFolder),
							slog.Int("facts", len(facts)))
					}
				})

				// Memory API server: tiny localhost HTTP endpoint for
				// the agent to call via curl from inside the container.
				memAPI := memory.NewAPIServer(agentMemory, memory.MemoryAPIAddr(), runLog)
				subsystems.Add(1)
				go func() {
					defer subsystems.Done()
					if err := memAPI.Start(); err != nil && err != http.ErrServerClosed {
						runLog.Error("memory API server", slog.Any("err", err))
					}
				}()
				// Shut down API server on context cancellation.
				go func() {
					<-ctx.Done()
					memAPI.Shutdown(context.Background())
				}()

				// Obsidian vault ingestion — runs entirely in background
				// so it never blocks startup or agent responses.
				if cfg.ObsidianVault != "" {
					ingester := memory.NewObsidianIngester(agentMemory, cfg.ObsidianVault, "owner", runLog)
					go func() {
						runLog.Info("obsidian: starting background scan",
							slog.String("vault", cfg.ObsidianVault))
						n, err := ingester.Scan(ctx)
						if err != nil {
							runLog.Warn("obsidian initial scan failed", slog.Any("err", err))
						} else {
							runLog.Info("obsidian: initial scan complete",
								slog.Int("chunks", n))
						}
					}()
					// Watcher re-scans every 5 minutes (also background).
					ingester.StartWatcher(ctx, 5*time.Minute)
				}

				runLog.Info("memory ready (with extraction + API)",
					slog.String("model", embedder.Model()),
					slog.Int("dim", embedder.Dim()),
					slog.String("api_addr", memory.MemoryAPIAddr()))
			}
		}
	}

	// Control plane Router (M3.5). Built-in commands are registered
	// here; subsystem-specific commands are registered by the subsystems
	// that own them (e.g. telegram registers /login directly because
	// it's a stateful interactive session, not a Command→Response).
	ctlLog := logger.With(slog.String("subsys", "control"))
	router := control.New(ctlLog)
	control.RegisterBuiltins(router)
	control.RegisterWhoami(router, cfg.OwnerChatID)
	control.RegisterVoiceCommand(router, telegram.SetReplyMode)
	control.RegisterLogsCommand(router, func(level, subsystem string, limit int) (string, error) {
		entries, err := picoclog.QueryLogs(st.DB(), level, subsystem, limit)
		if err != nil {
			return "", err
		}
		var sb strings.Builder
		for _, e := range entries {
			ts := time.UnixMilli(e.Timestamp).Format("15:04:05")
			fmt.Fprintf(&sb, "%s [%s]", ts, e.Level)
			if e.Subsystem != "" {
				fmt.Fprintf(&sb, " %s", e.Subsystem)
			}
			fmt.Fprintf(&sb, " %s\n", e.Message)
		}
		return sb.String(), nil
	})

	// /tasks commands. The chatFolderLookup closure resolves a Telegram
	// chat ID to its registered folder name via the store. promptFn
	// sends a prompt to Claude inside the chat's agent container for
	// natural-language schedule parsing.
	chatFolderLookup := func(chatID int64) string {
		jid := fmt.Sprintf("tg:%d", chatID)
		rc, err := st.GetRegistered(context.Background(), jid)
		if err != nil {
			return ""
		}
		return rc.Folder
	}
	var taskPromptFn control.PromptFunc
	if agentRunner != nil {
		taskPromptFn = func(ctx context.Context, folder, prompt string) (string, error) {
			return agentRunner.RunFresh(ctx, folder, runner.TierRegistered, allowlist, prompt)
		}
	}
	control.RegisterTaskCommands(router, st, chatFolderLookup, taskPromptFn)

	// /provider command — per-chat agent provider switching.
	if agentRunner != nil {
		providers := map[string]runner.AgentProvider{
			"claude": runner.NewClaudeProvider(cfg.ContainerImage),
			"codex":  runner.NewCodexProvider(""),
		}
		// Restore saved provider choices from store.
		for _, rc := range func() []store.RegisteredChat {
			list, _ := st.ListRegistered(context.Background())
			return list
		}() {
			saved, _ := st.GetState(context.Background(), "provider:"+rc.Folder)
			if saved != "" {
				if p, ok := providers[saved]; ok {
					agentRunner.SetChatProvider(rc.Folder, p)
				}
			}
		}

		control.RegisterProviderCommand(router, func(chatFolder, name string) (string, error) {
			p, ok := providers[name]
			if !ok {
				return "", fmt.Errorf("unknown provider %q (available: claude, codex)", name)
			}
			agentRunner.SetChatProvider(chatFolder, p)
			// Persist choice.
			st.SetState(context.Background(), "provider:"+chatFolder, name)
			agentRunner.ClearSession(context.Background(), chatFolder, runner.TierRegistered, allowlist)
			return fmt.Sprintf("Switched to %s. Session cleared for fresh start.", name), nil
		}, chatFolderLookup)
	}

	// /clear + /compact commands.
	if agentRunner != nil {
		control.RegisterSessionCommands(router, func(ctx context.Context, folder string, clearOnly bool) (string, error) {
			if clearOnly {
				return "", agentRunner.ClearSession(ctx, folder, runner.TierRegistered, allowlist)
			}
			return agentRunner.CompactSession(ctx, folder, runner.TierRegistered, allowlist)
		}, chatFolderLookup)
	}

	// Memory commands. Only wired if memory is available.
	if agentMemory != nil {
		control.RegisterMemoryCommands(router, &memoryOpsAdapter{mem: agentMemory}, chatFolderLookup)
		control.RegisterMemoryGC(router,
			func(ctx context.Context, folder string) (int64, error) {
				return agentMemory.ArchiveStale(ctx, folder, 90, 50)
			},
			func(ctx context.Context, folder string) (string, error) {
				items, _ := agentMemory.List(ctx, folder, 100)
				active := 0
				for range items {
					active++
				}
				return fmt.Sprintf("Memories: %d active", active), nil
			},
			chatFolderLookup)
	}

	// /login is registered as a Telegram-side special case (stateful
	// interactive session with stdin pipe, not a Router command).
	// Register it in the Router anyway for setMyCommands / /help
	// visibility, but the handler just returns a "use /login directly"
	// message — the actual implementation lives in telegram/login.go
	// and is wired via go-telegram/bot's MatchTypeCommand.
	router.Register("login", "login", "authenticate Claude Code (Pro/Max subscription)",
		control.PermPublic, func(ctx context.Context, cmd control.Command) (control.Response, error) {
			return control.Response{Text: "Use /login directly (interactive OAuth flow)."}, nil
		})
	// /register: owner-only command to register other chats.
	router.Register("chats.register", "register", "(owner) register a chat for agent access",
		control.PermOwnerOnly, func(ctx context.Context, cmd control.Command) (control.Response, error) {
			// Delegate to telegram's registerHandler logic via a thin
			// wrapper. For now this returns usage hint; the real register
			// logic stays in commands.go until the store dep is plumbed
			// into control handlers via Deps.
			if len(cmd.Args) != 2 {
				return control.Response{
					Text: "Usage: /register <chat_id> <folder>",
					Code: 1,
				}, nil
			}
			return control.Response{
				Text: fmt.Sprintf("register via Router not yet wired (args: %v). Use /register from Telegram directly for now.", cmd.Args),
				Code: 1,
			}, nil
		})

	if cfg.TelegramBotToken == "" {
		logger.Warn("telegram subsystem skipped: TELEGRAM_BOT_TOKEN is unset (store-only mode)")
	} else {
		tgLog := logger.With(slog.String("subsys", "telegram"))

		tgBot, err := telegram.New(cfg.TelegramBotToken, telegram.Deps{
			Store:       st,
			Runner:      agentRunner,
			Router:      router,
			Allowlist:   allowlist,
			OwnerChatID: cfg.OwnerChatID,
			Log:         tgLog,
		})
		if err != nil {
			return fmt.Errorf("init telegram: %w", err)
		}

		// Wire the GroupQueue if the runner is available.
		var agentQueue *queue.Queue
		if agentRunner != nil {
			queueLog := logger.With(slog.String("subsys", "queue"))
			agentQueue = queue.New(tgBot.NewAgentHandler(), queue.DefaultMaxConcurrent, queueLog)
			tgBot.SetQueue(agentQueue)
			queueLog.Info("queue ready",
				slog.Int("max_concurrent", queue.DefaultMaxConcurrent))
		}

		// Scheduler (M4): polls store for due tasks and pushes them
		// through the same queue as Telegram messages. Only starts
		// if both the queue and the store are available.
		if agentQueue != nil {
			schedLog := logger.With(slog.String("subsys", "scheduler"))
			sched := scheduler.New(st, agentQueue, schedLog)
			subsystems.Add(1)
			go func() {
				defer subsystems.Done()
				sched.Start(ctx)
			}()
		}

		subsystems.Add(1)
		go func() {
			defer subsystems.Done()
			if err := tgBot.Run(ctx); err != nil {
				tgLog.Error("telegram bot exited with error", slog.Any("err", err))
			}
		}()
	}

	logger.Info("gopod ready (waiting for SIGINT/SIGTERM)")

	<-ctx.Done()
	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		logger.Warn("shutdown", slog.Any("reason", err))
	} else {
		logger.Info("shutdown signal received")
	}

	// Wait for subsystems (telegram long-poll, future runner/scheduler)
	// to drain before returning so deferred cleanup is sequenced after
	// every goroutine has stopped touching the store.
	subsystems.Wait()
	return nil
}

// memoryOpsAdapter adapts memory.Memory → control.MemoryOps for
// /remember and /recall commands.
type memoryOpsAdapter struct {
	mem *memory.Memory
}

func (a *memoryOpsAdapter) Add(ctx context.Context, chatFolder, kind, title, content, source string) (int64, error) {
	return a.mem.Add(ctx, chatFolder, kind, title, content, source)
}

func (a *memoryOpsAdapter) SearchText(ctx context.Context, chatFolder, query string, k int) ([]control.MemoryResult, error) {
	results, err := a.mem.SearchText(ctx, chatFolder, query, k)
	if err != nil {
		return nil, err
	}
	out := make([]control.MemoryResult, len(results))
	for i, r := range results {
		out[i] = control.MemoryResult{ID: r.ID, Kind: r.Kind, Title: r.Title, Content: r.Content, Score: r.Score}
	}
	return out, nil
}

// memoryCompilerAdapter adapts memory.Memory → runner.MemoryCompiler.
type memoryCompilerAdapter struct {
	mem *memory.Memory
}

func (a *memoryCompilerAdapter) CompileContext(ctx context.Context, chatFolder, query string) string {
	return a.mem.CompileContext(ctx, chatFolder, query, memory.DefaultBudget)
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var h slog.Handler
	switch cfg.LogFormat {
	case "json":
		h = slog.NewJSONHandler(os.Stderr, opts)
	default:
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

// buildVersion returns the link-time version if set, otherwise the VCS
// revision embedded by `go build` (Go 1.18+), otherwise "dev".
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				if len(s.Value) > 12 {
					return s.Value[:12]
				}
				return s.Value
			}
		}
	}
	return "dev"
}
