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
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Coriol-is/gopod/internal/config"
	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/ipc"
	picoclog "github.com/Coriol-is/gopod/internal/log"
	"github.com/Coriol-is/gopod/internal/maintenance"
	"github.com/Coriol-is/gopod/internal/memory"
	"github.com/Coriol-is/gopod/internal/observability"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"github.com/Coriol-is/gopod/internal/scheduler"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/Coriol-is/gopod/internal/taskexec"
	"github.com/Coriol-is/gopod/internal/taskreport"
	"github.com/Coriol-is/gopod/internal/telegram"
)

// version is overridden at link time via -ldflags "-X main.version=...".
// In dev builds it falls back to the VCS info embedded by `go build`.
var version = "dev"

func main() {
	// `gopod backup ...` is an operator CLI: it never starts the application.
	if len(os.Args) > 1 && os.Args[1] == "backup" {
		os.Exit(runBackupCLI(os.Args[2:], os.Stdout, os.Stderr))
	}
	if err := run(); err != nil {
		// run() may exit before slog is wired, so go to stderr.
		fmt.Fprintf(os.Stderr, "gopod: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	processStart := time.Now()
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

	// CP-007: a restore latch or GOPOD_RECOVERY_QUARANTINE runs the control
	// plane only. This precedes the metrics listener, the store open
	// (migrations), polling, scheduler, IPC, warmup and delivery.
	if q, why := recoveryQuarantine(cfg); q {
		qctx, qstop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer qstop()
		return runQuarantine(qctx, cfg, why, logger)
	}

	// Observability (M3.6). Opt-in Prometheus /metrics listener gated by
	// GOPOD_METRICS_ADDR; tracing is still the otel no-op provider (O4).
	// A failed Init (e.g. the address cannot be bound) is fatal so an
	// operator who asked for metrics never runs silently without them.
	obsCfg := observability.LoadConfig()
	obsCfg.Version = buildVersion()
	obs, err := observability.Init(obsCfg)
	if err != nil {
		return fmt.Errorf("init observability: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := obs.Shutdown(shutdownCtx); err != nil {
			logger.Error("observability shutdown", slog.Any("err", err))
		}
	}()

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The controller instance lock is the stopped-writer backup fence.
	instanceLock, err := maintenance.LockWait(sigCtx, maintenance.ControllerLockPath(cfg.DataDir), 60*time.Second)
	if err != nil {
		return fmt.Errorf("controller instance lock (a backup capture or another controller holds it): %w", err)
	}
	defer instanceLock.Release()
	// ctx is the app context: cancelled after the signal once the
	// shutdown hooks (restart notice, D027) have run.
	ctx, onShutdown := gracefulContext(sigCtx, stop)

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

	restart := newControllerRestart(st, cfg.ChatsDir, cfg.OwnerChatID, stop, logger)
	var restartRecoveryErr error
	var subsystems sync.WaitGroup

	// Runner / Docker subsystem. Optional at boot: if GOPOD_NO_CONTAINER
	// is set, or the daemon is unreachable, gopod logs and continues
	// in "no runner" mode (telegram still echoes /ping).
	var (
		agentRunner *runner.Runner
		folderHeld  func(string) bool // set once the queue exists (managed tasks)
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

			restart.docker = d.Client()
			restartCtx, restartCancel := context.WithTimeout(ctx, 30*time.Second)
			restartRecoveryErr = restart.recycle(restartCtx)
			restartCancel()
			if restartRecoveryErr != nil {
				runLog.Warn("restart container recovery incomplete", slog.Any("err", restartRecoveryErr))
			}

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
			d.RequirePolicyLabels(cfg.ManagedTasksEnabled)
			if cfg.LeftoverCleanupEnabled && restartRecoveryErr == nil {
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
				MemoryBytes: 4 << 30,       // 4 GiB; GOPOD_CONTAINER_MEM later
				NanoCPUs:    2_000_000_000, // 2 CPUs
				PidsLimit:   1024,
			}
			// Create the agent provider. Currently always Claude;
			// P3/P4 will add per-chat provider selection.
			agentProvider := runner.NewClaudeProvider(cfg.ContainerImage)

			// Additional env vars beyond what the provider requires.
			envAllow := []string{"OPENAI_API_KEY", "FREEFEED_BASE_URL", "FREEFEED_USERNAME", "FREEFEED_PASSWORD"}
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
			r.SetAdmissionPolicy(cfg.OwnerChatID, st)
			if cfg.ManagedTasksEnabled {
				// Before any container use, so the foreground container is already
				// created under the policy guard. Holds fail closed until the queue
				// exists. No task credential adapter is qualified: tasks stay denied.
				if err := r.SetManagedTasks(runner.ManagedTaskConfig{
					Scopes: st,
					Holds: func(folder string) bool {
						if folderHeld == nil {
							return true
						}
						return folderHeld(folder)
					},
					Providers: map[string]runner.AgentProvider{
						"claude": runner.NewClaudeProvider(cfg.ContainerImage),
						"codex":  runner.NewCodexProvider(""),
					},
				}); err != nil {
					return fmt.Errorf("managed tasks: %w", err)
				}
				runLog.Info("managed task runtime enabled (task role denied until a credential adapter is qualified)")
			}
			r.SetAgentLimits(runner.AgentLimits{
				MaxAgents:      cfg.MaxAgents,
				MinAvailableMB: cfg.MinAvailableMB,
				NodeHeapMB:     cfg.AgentNodeHeapMB,
			})
			runLog.Info("agent admission",
				slog.Int("max_agents", cfg.MaxAgents),
				slog.Int("min_available_mb", cfg.MinAvailableMB),
				slog.Int("agent_node_heap_mb", cfg.AgentNodeHeapMB))
			if cfg.CompactAfter > 0 {
				r.SetCompactAfter(cfg.CompactAfter)
			}
			r.SetProgress(cfg.ProgressEnabled)

			runLog.Info("runner ready",
				slog.String("image", defaults.Image),
				slog.Int("uid", defaults.UID),
				slog.Int("gid", defaults.GID))

			// Memory layer (M9). OpenAI embedder + hybrid search.
			// Optional: if OPENAI_API_KEY is unset, memory is disabled.
			embedder, err := memory.NewOpenAIEmbedder("", "", 0)
			if err != nil {
				runLog.Warn("memory disabled: embedder unavailable",
					slog.Any("err", err))
			} else {
				agentMemory = memory.New(st, embedder, runLog)
				r.SetMemory(&memoryCompilerAdapter{mem: agentMemory, st: st, ownerID: cfg.OwnerChatID})

				// Session compact callback: summarize + store + supersede.
				r.SetCompactFn(func(ctx context.Context, chatFolder string, thread int) error {
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
						if localMemoryScope(ctx, st, chatFolder, cfg.OwnerChatID) {
							if _, err := agentMemory.AddLocalInThread(ctx, chatFolder, thread, "conversation_summary", "session compact", summary, "compact"); err != nil {
								return err
							}
						} else {
							agentMemory.AddInThread(ctx, chatFolder, thread, "conversation_summary", "session compact", summary, "compact")
						}
					}
					return nil
				})

				// Phase A: extraction callback. After each agent turn,
				// sends the conversation through an extraction prompt
				// to Claude, stores structured facts. Uses the same
				// runner.Run as taskPromptFn for the LLM call. Facts are
				// stored in the turn's conversation (D023).
				r.SetExtractFn(func(ctx context.Context, chatFolder string, thread int, userMsg, agentReply string) {
					if localMemoryScope(ctx, st, chatFolder, cfg.OwnerChatID) {
						return
					} // Shared automatic learning waits for qualified CP-006 maintenance; explicit local memory tools remain available.
					promptFn := func(c context.Context, f, p string) (string, error) {
						return r.RunFresh(c, f, runner.TierRegistered, allowlist, p)
					}
					facts, err := agentMemory.ExtractFromTurn(ctx, chatFolder, userMsg, agentReply, promptFn)
					if err != nil {
						runLog.Warn("extraction failed", slog.Any("err", err))
						return
					}
					if len(facts) > 0 {
						if localMemoryScope(ctx, st, chatFolder, cfg.OwnerChatID) {
							for _, fact := range facts {
								_, _ = agentMemory.AddLocalInThread(ctx, chatFolder, thread, fact.Kind, "extracted group fact", fact.Content, "extraction")
							}
						} else {
							agentMemory.IngestExtracted(ctx, chatFolder, thread, facts)
						}
						runLog.Debug("extracted facts from turn",
							slog.String("chat", chatFolder),
							slog.Int("thread", thread),
							slog.Int("facts", len(facts)))
					}
				})

				// Memory API server: tiny localhost HTTP endpoint for
				// the agent to call via curl from inside the container.
				// Per-exec capabilities (CP-001): the runner mints one
				// per agent run and the API resolves the chat from it.
				caps := memory.NewTokens()
				r.SetCapabilities(caps)
				memAPI := memory.NewAPIServer(agentMemory, memory.MemoryAPIAddr(), caps, runLog)
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

	groupManager, stopGroups, err := wireGroups(ctx, cfg, st, agentRunner, agentMemory, logger)
	if err != nil {
		return err
	}
	defer stopGroups()
	if groupManager != nil {
		groupManager.Slot = agentRunner.WithChatSlot
	}

	// Control plane Router (M3.5). Built-in commands are registered
	// here; subsystem-specific commands are registered by the subsystems
	// that own them (e.g. telegram registers /login directly because
	// it's a stateful interactive session, not a Command→Response).
	ctlLog := logger.With(slog.String("subsys", "control"))
	router := control.New(ctlLog)
	control.RegisterBuiltins(router)
	if groupManager != nil {
		control.RegisterIsolationCommands(router, control.IsolationActions{Status: groupManager.Status, Prepare: groupAction(st, agentRunner, groupManager.Prepare), Enable: groupAction(st, agentRunner, groupManager.Enable), Disable: groupManager.Disable})
	}

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

	var fallbackSwitch control.ProviderFallbackSwitchFunc
	// /provider command — per-chat agent provider switching.
	if agentRunner != nil {
		providers := map[string]runner.AgentProvider{
			"claude": runner.NewClaudeProvider(cfg.ContainerImage),
			"codex":  runner.NewCodexProvider(""),
		}
		fallbackSwitch = func(ctx context.Context, folder, expected, target string) error {
			p, ok := providers[target]
			if !ok {
				return fmt.Errorf("unknown fallback provider")
			}
			err := agentRunner.SwitchChatProviderIfCurrent(ctx, folder, expected, p)
			if errors.Is(err, runner.ErrProviderChanged) {
				return control.ErrFallbackStale
			}
			return err
		}
		// Restore choices first, then warm/check only the configured owner before
		// queue recovery, scheduled work or Telegram command dispatch can race it.
		warmOwnerID := cfg.OwnerChatID
		if restartRecoveryErr != nil {
			warmOwnerID = 0
		} // restore choices, but never recycle after failed recovery evidence
		if err := restoreAndWarmOwner(ctx, st, agentRunner, warmOwnerID, providers, allowlist, logger); err != nil {
			return err
		}
		// Maintenance must not race restoration or warm up the default image.
		var compactInterval time.Duration
		if cfg.CompactInterval != "" {
			compactInterval, _ = time.ParseDuration(cfg.CompactInterval)
		}
		agentRunner.StartCompactWatcher(ctx, runner.CompactConfig{After: cfg.CompactAfter, Interval: compactInterval, TimeOfDay: cfg.CompactTime}, allowlist)
		agentRunner.StartIdleWatcher(ctx, runner.DefaultIdleTimeout)

		// /model (CP-008): per-chat model override, persisted like /provider.
		control.RegisterModelCommand(router,
			agentRunner.ModelForChat,
			func(chatFolder, model string) (string, error) {
				agentRunner.SetChatModel(chatFolder, model)
				if err := st.SetState(context.Background(), "model:"+chatFolder, model); err != nil {
					return "", fmt.Errorf("persist model: %w", err)
				}
				if model == "" {
					return "Model reset to the provider default. Applies from the next message.", nil
				}
				return "Model set to " + model + ". Applies from the next message.", nil
			},
			// Picker choices (D028): the Claude aliases; Codex takes any
			// model id the account has, so it offers only "default".
			func(chatFolder string) []string {
				if agentRunner.ProviderForChat(chatFolder).Name() == "claude" {
					return []string{"fable", "opus", "sonnet", "haiku"}
				}
				return nil
			}, chatFolderLookup)

		control.RegisterProviderCommand(router, func(ctx context.Context, chatFolder, name string) (string, error) {
			p, ok := providers[name]
			if !ok {
				return "", fmt.Errorf("unknown provider %q (available: claude, codex)", name)
			}
			hadModel := agentRunner.ModelForChat(chatFolder) != ""
			if err := agentRunner.SwitchChatProvider(ctx, chatFolder, p); err != nil {
				return "", err
			}
			msg := fmt.Sprintf("Switched to %s. Conversations continue with their own recent history; sessions, workspace and memory are preserved.", name)
			if hadModel {
				msg += " Model reset to the provider default; use /model to pick one for " + name + "."
			}
			return msg, nil
		}, func(chatFolder string) string {
			return agentRunner.ProviderForChat(chatFolder).Name()
		}, []string{"claude", "codex"}, chatFolderLookup)
	}

	// /clear + /compact commands.
	if agentRunner != nil {
		// /clear and /compact act on the conversation they were sent
		// in: thread comes from the command's Caller (0 = main chat).
		control.RegisterSessionCommands(router, func(ctx context.Context, folder string, thread int, clearOnly bool) (string, error) {
			if clearOnly {
				return "", agentRunner.ClearSession(ctx, folder, thread, runner.TierRegistered, allowlist)
			}
			return agentRunner.CompactSession(ctx, folder, thread, runner.TierRegistered, allowlist)
		}, chatFolderLookup)
	}

	// Shared memory stays local even without a personal embedding service.
	commandsMemory := agentMemory
	localOnlyMemory := false
	if commandsMemory == nil && groupManager != nil {
		commandsMemory = memory.New(st, nil, logger)
		localOnlyMemory = true
	}
	if commandsMemory != nil {
		control.RegisterMemoryCommands(router, &memoryOpsAdapter{mem: commandsMemory, st: st, ownerID: cfg.OwnerChatID, localOnly: localOnlyMemory}, chatFolderLookup)
		control.RegisterMemoryGC(router,
			func(ctx context.Context, folder string) (int64, error) {
				if localOnlyMemory && !localMemoryScope(ctx, st, folder, cfg.OwnerChatID) {
					return 0, fmt.Errorf("personal memory service unavailable")
				}
				return commandsMemory.ArchiveStale(ctx, folder, 90, 50)
			},
			func(ctx context.Context, folder string) (string, error) {
				if localOnlyMemory && !localMemoryScope(ctx, st, folder, cfg.OwnerChatID) {
					return "", fmt.Errorf("personal memory service unavailable")
				}
				items, _ := commandsMemory.List(ctx, folder, 100)
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
	// Registering a group also prepares its clean runtime epoch (D047).
	var prepareGroup control.GroupPreparer
	if groupManager != nil {
		prepareGroup = func(ctx context.Context, jid string) (string, error) {
			return groupManager.EnsurePrepared(ctx, jid, true)
		}
	}
	control.RegisterChatCommandsWith(router, st, cfg.OwnerChatID, prepareGroup)

	if cfg.TelegramBotToken == "" {
		logger.Warn("telegram subsystem skipped: TELEGRAM_BOT_TOKEN is unset (store-only mode)")
	} else {
		tgLog := logger.With(slog.String("subsys", "telegram"))

		tgBot, err := telegram.New(cfg.TelegramBotToken, telegram.Deps{
			Store:         st,
			Runner:        agentRunner,
			Router:        router,
			Allowlist:     allowlist,
			OwnerChatID:   cfg.OwnerChatID,
			GroupReason:   groupReasonFunc(groupManager),
			Log:           tgLog,
			StreamEnabled: cfg.StreamEnabled,
		})
		if err != nil {
			return fmt.Errorf("init telegram: %w", err)
		}
		if groupManager != nil {
			groupManager.Notify = tgBot.NotifyOwner
			go groupManager.ReconcileAll(ctx)
		}

		restart.notify = tgBot.SendRestartNotice
		control.RegisterRestartCommand(router, restart.request)
		control.RegisterTelegramCommands(router, tgBot.TelegramSnapshot, tgBot.RefreshTelegramSnapshot, func(ctx context.Context, id int64) bool {
			_, err := st.GetRegistered(ctx, fmt.Sprintf("tg:%d", id))
			return err == nil
		})

		if fallbackSwitch != nil {
			control.RegisterProviderFallbackCommand(router, st, fallbackSwitch, tgBot.EnqueueProviderFallback)
		}

		// Wire the GroupQueue if the runner is available. Turns are
		// persisted in the store so a restart can recover them.
		var agentQueue *queue.Queue
		if agentRunner != nil {
			queueLog := logger.With(slog.String("subsys", "queue"))
			agentQueue = queue.New(ctx, tgBot.NewAgentHandler(), queue.DefaultMaxConcurrent,
				turnStoreAdapter{st: st}, queueLog)
			tgBot.SetQueue(agentQueue)
			// Managed task recovery is inspect-only until execution lands (CP-004
			// PR 3): no handler, so nothing managed runs, but possibly started
			// attempts are never replayed and held capacity is rebuilt.
			managedRT := queue.ManagedRuntime{Store: managedLedger{st: st}}
			if cfg.ManagedTasksEnabled && agentRunner != nil {
				folderHeld = agentQueue.FolderHeld
				exec := &taskexec.Executor{
					Runner:  agentRunner,
					Reports: st,
					DataDir: cfg.DataDir,
					Log:     queueLog,
					Grants: taskreport.NewGrants(nil, func(s store.ScopeRef) error {
						return st.ValidateScope(context.Background(), s)
					}),
				}
				managedRT.Handler, managedRT.Snapshot = exec.Handle, exec.Snapshot
			}
			agentQueue.SetManaged(managedRT)
			agentQueue.OnDone(tgBot.NewDoneHook())
			subsystems.Add(1)
			go func() {
				defer subsystems.Done()
				<-ctx.Done()
				agentQueue.Close()
			}()
			queueLog.Info("queue ready",
				slog.Int("max_concurrent", queue.DefaultMaxConcurrent))
		}

		// Scheduler (M4): polls store for due tasks and pushes them
		// through the same queue as Telegram messages. Only starts
		// if both the queue and the store are available.
		// New registers the scheduler's OnDone hook; it must exist
		// before Recover so recovered task turns finalize their logs.
		var sched *scheduler.Scheduler
		if agentQueue != nil {
			schedLog := logger.With(slog.String("subsys", "scheduler"))
			sched = scheduler.New(st, agentQueue, schedLog)
		}

		// Recover turns left unfinished by a crash/shutdown. Must run after
		// the scheduler hook is registered and before the bot polls.
		replayed := 0
		if agentQueue != nil {
			n, err := agentQueue.Recover(ctx)
			if errors.Is(err, queue.ErrHoldsUnknown) {
				// Serving beside a possibly running task attempt is unsafe.
				return fmt.Errorf("queue recovery: %w", err)
			} else if err != nil {
				logger.Error("queue: recover unfinished turns", slog.Any("err", err))
			} else if n > 0 {
				logger.Info("queue: recovered unfinished turns", slog.Int("count", n))
			}
			replayed = n
		}

		// Start polling only after Recover, so the first poll cannot
		// race the replay of an interrupted task turn for the same slot.
		if sched != nil {
			subsystems.Add(1)
			go func() {
				defer subsystems.Done()
				sched.Start(ctx)
			}()
		}

		// Durable outbox dispatcher (CP-002 PR 2). Off by default; the legacy
		// send paths stay authoritative unless a route below publishes to it.
		// It starts before the IPC watcher so the durable IPC route (PR 3) is
		// enabled only when the dispatcher is really running.
		outboxRunning := false
		var artifactIPCConfig ipc.ArtifactConfig
		if cfg.OutboxDispatcherEnabled {
			outboxLog := logger.With(slog.String("subsys", "outbox"))
			// A failure here must not take the whole bot down: the legacy send
			// paths stay authoritative, so run with the dispatcher disabled.
			if owner, err := acquireOutboxOwner(ctx, st, 10*time.Second, 500*time.Millisecond); err != nil {
				outboxLog.Error("outbox dispatcher DISABLED: could not take the exclusive outbox lock", slog.Any("err", err))
			} else {
				// CP-003 artifact snapshots: off unless GOPOD_OUTBOX_ARTIFACTS=1.
				// A failure to open the store leaves uploads refused, not the bot down.
				var opener telegram.ArtifactOpener
				if cfg.OutboxArtifactsEnabled {
					if ac, blobs, err := openArtifactStore(cfg, st); err != nil {
						outboxLog.Error("artifact delivery DISABLED", slog.Any("err", err))
					} else {
						artifactIPCConfig = ac
						tgBot.EnablePreparedDelivery(preparedDelivery(cfg, st, blobs, outboxLog))
						opener = &telegram.SnapshotOpener{Inventory: st, Blobs: blobs, MaxBytes: st.ArtifactLimits().MaxFileBytes, Log: outboxLog}
						onShutdown(func() { blobs.Close() })
					}
				}
				dispatcher := telegram.NewOutboxDispatcher(owner, tgBot.NewOutboxTransport(opener), telegram.OutboxConfig{}, outboxLog)
				if err := dispatcher.Start(ctx); err != nil {
					owner.Release()
					outboxLog.Error("outbox dispatcher DISABLED: start failed", slog.Any("err", err))
				} else {
					st.SetPublishNotifier(dispatcher.Notify)
					onShutdown(func() {
						if err := dispatcher.Stop(10 * time.Second); err != nil {
							outboxLog.Error("outbox dispatcher stop", slog.Any("err", err))
						}
						owner.Release()
					})
					outboxRunning = true
					outboxLog.Info("outbox dispatcher running")
				}
			}
		}

		// IPC watcher (M7). Reads agent-authored JSON files under
		// data/ipc/<chat>/{messages,tasks} and dispatches them into
		// telegram + scheduler. See
		// docs/superpowers/specs/2026-05-07-m7-ipc-design.md.
		ipcLog := logger.With(slog.String("subsys", "ipc"))
		ownerFolder := ""
		if cfg.OwnerChatID > 0 && agentRunner != nil {
			if rc, err := st.GetRegistered(ctx, fmt.Sprintf("tg:%d", cfg.OwnerChatID)); err == nil && agentRunner.CheckAdmission(ctx, rc.Folder) == nil {
				ownerFolder = rc.Folder
			}
		}
		// Prepared personal attachments (CP-003 PR 5) need the ledger sweep and, when
		// the IPC watcher does not reconcile artifacts itself, the reconciler too.
		if artifactIPCConfig.Store != nil {
			durable := durableIPC(cfg.OutboxIPCEnabled, outboxRunning, st, ownerFolder, cfg.OwnerChatID)
			var reconcile func(context.Context)
			if artifactIPC(durable, artifactIPCConfig).Store == nil {
				ac := artifactIPCConfig
				reconcile = func(c context.Context) {
					if _, err := ac.Reconcile(c, time.Now(), ipcLog); err != nil {
						ipcLog.Warn("artifact reconcile", slog.Any("err", err))
					}
				}
			}
			// Synchronous, before tgBot.Run starts below: rows older than this
			// process are the only ones a restart may flip to unknown.
			sweepGenerationsAtStartup(ctx, st, processStart, ipcLog)
			subsystems.Add(1)
			go func() {
				defer subsystems.Done()
				maintainGenerations(ctx, st, 5*time.Minute, time.Duration(cfg.GenerationRetentionDays)*24*time.Hour, reconcile, ipcLog)
			}()
		}
		ipcMsgs := telegram.NewIPCSink(tgBot)
		ipcTasks := scheduler.NewIPCSink(st)
		ipcWatcher := ipc.New(ipc.Config{
			DataDir:     cfg.DataDir,
			OwnerFolder: ownerFolder,
			NamespacePath: func(folder string) (string, bool) {
				if agentRunner == nil || agentRunner.CheckAdmission(ctx, folder) != nil {
					return "", false
				}
				if groupManager != nil {
					return groupManager.NamespacePath(folder)
				}
				return filepath.Join(cfg.DataDir, "ipc", folder), true
			},
			Namespaces: func(c context.Context) ([]string, error) {
				rows, e := st.ListRegistered(c)
				if e != nil {
					return nil, e
				}
				out := make([]string, 0, len(rows))
				for _, row := range rows {
					out = append(out, row.Folder)
				}
				return out, nil
			},
			NamespaceAllowed: func(c context.Context, folder string) bool {
				return agentRunner != nil && agentRunner.CheckAdmission(c, folder) == nil
			},
			Durable:   durableIPC(cfg.OutboxIPCEnabled, outboxRunning, st, ownerFolder, cfg.OwnerChatID),
			Artifacts: artifactIPC(durableIPC(cfg.OutboxIPCEnabled, outboxRunning, st, ownerFolder, cfg.OwnerChatID), artifactIPCConfig),
		}, ipcMsgs, ipcTasks, ipcLog,
			func(folder string) (string, bool) {
				c, err := st.GetByFolder(ctx, folder)
				if err != nil {
					return "", false
				}
				return c.JID, true
			})
		subsystems.Add(1)
		go func() {
			defer subsystems.Done()
			if err := ipcWatcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				ipcLog.Error("ipc watcher", slog.Any("err", err))
			}
		}()
		ipcLog.Info("ipc watcher running",
			slog.String("data_dir", cfg.DataDir),
			slog.String("owner_folder", ownerFolder),
			slog.Bool("durable_text", cfg.OutboxIPCEnabled && outboxRunning))

		// Lifecycle notices (D027): "back" to the owner now, "restarting"
		// to the owner and unfinished conversations on SIGTERM/SIGINT.
		restartRecovered := restart.recovered(ctx, agentRunner, restartRecoveryErr)
		lifecycle := tgBot.NewLifecycle(telegram.LifecycleConfig{
			Store:       st,
			OwnerChatID: cfg.OwnerChatID,
			Version:     buildVersion(),
			Enabled:     cfg.NotifyLifecycle,
		})
		onShutdown(func() {
			if !restart.requested.Load() {
				lifecycle.Shutdown(context.Background())
			}
		})
		if !restartRecovered {
			lifecycle.Started(sigCtx, replayed)
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
	if restart.requested.Load() {
		restartCtx, restartCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer restartCancel()
		if err := restart.recycle(restartCtx); err != nil {
			logger.Warn("restart container shutdown incomplete", slog.Any("err", err))
		}
	}
	return nil
}

// memoryOpsAdapter adapts memory.Memory → control.MemoryOps for
// /remember and /recall commands.
type memoryOpsAdapter struct {
	localOnly bool
	ownerID   int64
	st        *store.Store
	mem       *memory.Memory
}

func (a *memoryOpsAdapter) Add(ctx context.Context, chatFolder, kind, title, content, source string) (int64, error) {
	if kind == "conversation_summary" {
		return 0, fmt.Errorf("kind conversation_summary is written by session compact only")
	}
	if localMemoryScope(ctx, a.st, chatFolder, a.ownerID) {
		return a.mem.AddLocalInThread(ctx, chatFolder, 0, kind, title, content, source)
	}
	if a.localOnly {
		return 0, fmt.Errorf("personal memory service unavailable")
	}
	return a.mem.Add(ctx, chatFolder, kind, title, content, source)
}

func (a *memoryOpsAdapter) SearchText(ctx context.Context, chatFolder, query string, k int) ([]control.MemoryResult, error) {
	if localMemoryScope(ctx, a.st, chatFolder, a.ownerID) {
		items, err := a.mem.SearchLocalInThread(ctx, chatFolder, 0, query, k)
		if err != nil {
			return nil, err
		}
		out := make([]control.MemoryResult, 0, len(items))
		for _, it := range items {
			out = append(out, control.MemoryResult{ID: it.ID, Kind: it.Kind, Title: it.Title, Content: it.Content, Score: it.Score})
		}
		return out, nil
	}
	if a.localOnly {
		return nil, fmt.Errorf("personal memory service unavailable")
	}
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
	localOnly bool
	ownerID   int64
	st        *store.Store
	mem       *memory.Memory
}

func (a *memoryCompilerAdapter) CompileContext(ctx context.Context, chatFolder string, thread int, query string) string {
	if localMemoryScope(ctx, a.st, chatFolder, a.ownerID) {
		return a.mem.CompileLocalContext(ctx, chatFolder, thread, query, memory.DefaultBudget)
	}
	if a.localOnly {
		return ""
	}
	return a.mem.CompileContext(ctx, chatFolder, thread, query, memory.DefaultBudget)
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

// turnStoreAdapter maps queue.Item <-> store.Turn so the queue package
// can persist turns without importing store (and vice versa).
type turnStoreAdapter struct{ st *store.Store }

func (a turnStoreAdapter) InsertTurn(ctx context.Context, it queue.Item) (int64, bool, error) {
	return a.st.InsertTurn(ctx, store.Turn{
		Source:      it.Source,
		SourceID:    it.SourceID,
		ChatFolder:  it.Folder,
		ChatID:      it.ChatID,
		TGMessageID: int64(it.MessageID),
		ThreadID:    it.ThreadID,
		IsOwner:     it.IsOwner,
		IsVoice:     it.IsVoice,
		Text:        it.Text,
		FilePath:    it.FilePath,
	})
}

func (a turnStoreAdapter) MarkRunning(ctx context.Context, id int64) error {
	return a.st.MarkTurnRunning(ctx, id)
}

func (a turnStoreAdapter) MarkFinished(ctx context.Context, id int64, status, errText string) error {
	return a.st.MarkTurnFinished(ctx, id, status, errText)
}

func (a turnStoreAdapter) LoadUnfinished(ctx context.Context) ([]queue.Item, error) {
	turns, err := a.st.ListUnfinishedTurns(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]queue.Item, 0, len(turns))
	for _, t := range turns {
		out = append(out, turnToItem(t))
	}
	return out, nil
}

// Non-owner and unresolved contexts never inherit personal embedding services.
func localMemoryScope(ctx context.Context, st *store.Store, folder string, ownerID int64) bool {
	if st == nil {
		return false
	} // legacy isolated adapter tests
	c, err := st.GetByFolder(ctx, folder)
	return err != nil || !c.IsOwner || ownerID <= 0 || c.JID != fmt.Sprintf("tg:%d", ownerID)
}
