package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/google/uuid"
)

type elevatedEngine interface {
	containerEngine
	ElevatedSupported(context.Context) error
	ElevatedIDs(context.Context) ([]string, error)
	ExecElevated(context.Context, string, []string) (*ExecResult, error)
}

type Elevated struct {
	Notify                  func(context.Context, string)
	claudeImage             string
	admission               *Runner
	heldRelease             func()
	mu                      sync.Mutex
	op                      sync.Mutex
	st                      *store.Store
	engine                  elevatedEngine
	base                    ElevatedSpawnConfig
	paths                   Paths
	codexImage              string
	session                 store.ElevatedSession
	container               string
	busy, blocked, draining bool
	cancel                  context.CancelFunc
	done                    chan struct{}
	notice                  string
}

func NewElevated(st *store.Store, d *Docker, paths Paths, base ElevatedSpawnConfig, codexImage string) *Elevated {
	var engine elevatedEngine
	if d != nil {
		engine = d
	}
	return &Elevated{st: st, engine: engine, paths: paths, base: base, codexImage: codexImage, claudeImage: base.Image}
}

func (e *Elevated) Active() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.session.ID != "" || e.blocked
}
func (e *Elevated) FreshAfter() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return (e.session.StartedAt + 999) / 1000
}
func (e *Elevated) TakeNotice() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := e.notice
	e.notice = ""
	return n
}
func (e *Elevated) Status() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.blocked {
		return "⚠️ ROOT: состояние выполнения не подтверждено. Новые запросы заблокированы. Повторите /root stop после восстановления Docker."
	}
	if e.session.ID == "" {
		return "ROOT выключен. Обычная переписка активна."
	}
	state := "ожидает сообщения"
	if e.busy {
		state = "выполняет запрос; /stop остановит ответ"
	}
	return fmt.Sprintf("⚠️ ROOT · %s · %s\n/root stop — закрыть сессию; /root log — журнал host exec.", e.session.Provider, state)
}

// Recover runs even with the feature flag off, before ordinary leftover inventory.
func (e *Elevated) Recover(ctx context.Context) error {
	e.mu.Lock()
	e.blocked = true
	e.mu.Unlock()
	e.op.Lock()
	defer e.op.Unlock()
	session, active, err := e.st.ActiveElevatedSession(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.session = session
	e.mu.Unlock()
	if e.engine == nil {
		if active {
			e.mu.Lock()
			e.blocked = true
			e.mu.Unlock()
			return ErrExecutionUnproven
		}
		e.mu.Lock()
		e.blocked = false
		e.mu.Unlock()
		return nil
	}
	ids, err := e.engine.ElevatedIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = e.engine.StopRemove(ctx, id); err != nil {
			e.mu.Lock()
			e.blocked = true
			e.mu.Unlock()
			return err
		}
	}
	remaining, err := e.engine.ElevatedIDs(ctx)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return ErrExecutionUnproven
	}
	if active {
		if err = e.st.CloseElevatedSession(ctx, session.ID, "controller interrupted"); err != nil {
			return err
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.session = store.ElevatedSession{}
	e.blocked = false
	if active || len(ids) > 0 {
		e.notice = "⚠️ ROOT: предыдущая сессия прервана перезапуском. Контейнер удалён; автоматического продолжения нет. Для новой сессии используйте /root."
	}
	return nil
}

func (e *Elevated) Start(ctx context.Context, provider string) (string, error) {
	e.op.Lock()
	defer e.op.Unlock()
	e.mu.Lock()
	if e.session.ID != "" || e.blocked || e.draining {
		e.mu.Unlock()
		return "", errors.New("ROOT уже активен, заблокирован или идёт перезапуск; /root status")
	}
	e.mu.Unlock()
	if e.engine == nil {
		return "", errors.New("Docker недоступен. Сессия не запущена")
	}
	if err := e.engine.ElevatedSupported(ctx); err != nil {
		return "", err
	}
	cfg := e.base
	cfg.Image = e.claudeImage
	cfg.Provider = provider
	if provider == "codex" {
		cfg.Image = e.codexImage
	} else if provider != "claude" {
		return "", errors.New("unknown provider")
	}
	w, c, x, err := EnsureElevatedDirs(e.paths)
	if err != nil {
		return "", err
	}
	cfg.WorkspaceDir, cfg.ClaudeDir, cfg.CodexDir = w, c, x
	s, err := e.st.OpenElevatedSession(ctx, provider)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	e.session = s
	e.blocked = true // starting is not ready for input
	e.base = cfg
	e.mu.Unlock()
	id, err := ensureElevatedContainer(ctx, e.engine, cfg)
	e.mu.Lock()
	e.container = id
	e.blocked = err != nil
	e.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("ROOT не запущен: %w; /root stop для проверки и очистки", err)
	}
	return "⚠️ ROOT · " + provider + "\nСессия с полным доступом к хосту запущена. Только новые текстовые сообщения владельца направляются сюда. /stop — остановить ответ; /root stop — закрыть. Вход провайдера используется только из отдельного elevated-home; обычные credentials не копируются.", nil
}

func (e *Elevated) StopAnswer() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel == nil {
		return false
	}
	e.cancel()
	return true
}

func (e *Elevated) Close(ctx context.Context, reason string) error {
	e.op.Lock()
	defer e.op.Unlock()
	e.mu.Lock()
	e.blocked = true // close admission before waiting for any running answer
	if e.cancel != nil {
		e.cancel()
	}
	done := e.done
	s := e.session
	e.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if e.engine == nil {
		if s.ID != "" {
			return ErrExecutionUnproven
		}
		e.mu.Lock()
		e.blocked = false
		e.mu.Unlock()
		return nil
	}
	ids, err := e.engine.ElevatedIDs(ctx)
	if err == nil {
		for _, id := range ids {
			if err = e.engine.StopRemove(ctx, id); err != nil {
				break
			}
		}
	}
	if err == nil {
		var remaining []string
		remaining, err = e.engine.ElevatedIDs(ctx)
		if err == nil && len(remaining) > 0 {
			err = ErrExecutionUnproven
		}
	}
	if err == nil && s.ID != "" {
		err = e.st.CloseElevatedSession(ctx, s.ID, reason)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.blocked = true
		return err
	}
	if s.ID != "" && reason != "owner close" && e.Notify != nil {
		e.Notify(ctx, "ROOT закрыт: контейнер удалён ("+reason+"). Автоматического продолжения не будет.")
	}
	e.session = store.ElevatedSession{}
	e.container = ""
	e.blocked = false
	if e.heldRelease != nil {
		e.heldRelease()
		e.heldRelease = nil
	}
	return nil
}

func (e *Elevated) Run(ctx context.Context, text string) (string, error) {
	e.mu.Lock()
	if e.session.ID == "" || e.blocked || e.draining || e.busy {
		e.mu.Unlock()
		return "", errors.New("ROOT не готов или занят. Сообщение не поставлено в очередь; /root status")
	}
	ctx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.busy = true
	e.done = make(chan struct{})
	done := e.done
	s, cfg, id := e.session, e.base, e.container
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		e.cancel = nil
		e.busy = false
		e.done = nil
		close(done)
		e.mu.Unlock()
	}()
	facts, inspectErr := e.engine.InspectContainer(ctx, ContainerName(ElevatedFolder))
	if inspectErr != nil || facts == nil || !facts.Running || facts.ID != id {
		e.mu.Lock()
		e.blocked = true
		e.mu.Unlock()
		return "", errors.New("ROOT: контейнер потерян; автоматического пересоздания нет. /root stop")
	}
	plan, err := elevatedPlan(cfg)
	image, imageErr := e.engine.ImageID(ctx, cfg.Image)
	if err != nil || imageErr != nil || facts.Labels[LabelPolicyConfig] != plan.Conf.Labels[LabelPolicyConfig] || decideContainer(plan, facts, image, nil).Action != actReuse {
		e.mu.Lock()
		e.blocked = true
		e.mu.Unlock()
		return "", errors.New("ROOT: контейнер потерян или изменён. Выполнение запрещено; /root stop")
	}
	var release func()
	if e.admission != nil {
		release, err = e.admission.acquireAgentSlot(ctx, ElevatedFolder)
		if err != nil {
			return "", err
		}
	}
	if release != nil {
		releaseSlot := release
		releaseGate, gateErr := e.admission.admit(ctx, ElevatedFolder, ClassInteractive)
		if gateErr != nil {
			releaseSlot()
			return "", gateErr
		}
		release = func() { releaseGate(); releaseSlot() }
	}
	if release != nil {
		defer func() {
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.blocked {
				e.heldRelease = release
			} else {
				release()
			}
		}()
	}
	if err = e.st.AppendElevatedTranscript(ctx, s.ID, "user", text); err != nil {
		return "", err
	}
	native, err := e.st.ElevatedNativeContext(ctx, s.ID)
	if err != nil {
		return "", err
	}
	ref := SessionRef{ID: native, Resume: native != ""}
	if native == "" {
		ref.ID = uuid.NewString()
	}
	var p AgentProvider = NewClaudeProvider(cfg.Image)
	if s.Provider == "codex" {
		p = NewCodexProvider(cfg.Image)
	}
	if s.Provider == "claude" {
		prep := []string{"sh", "-c", `if [ ! -f "$HOME/.claude.json" ]; then b=$(ls -t "$HOME/.claude/backups/.claude.json.backup."* 2>/dev/null | head -1); if [ -n "$b" ]; then cp "$b" "$HOME/.claude.json"; fi; fi`}
		result, prepErr := runElevatedJournaled(ctx, e.st, s.ID, prep, func() (*ExecResult, error) { return e.engine.ExecElevated(ctx, id, prep) })
		if result == nil {
			e.mu.Lock()
			e.blocked = true
			e.mu.Unlock()
			return "", fmt.Errorf("ROOT setup uncertain: %w", prepErr)
		}
		if prepErr != nil {
			return "", prepErr
		}
		if result.ExitCode != 0 {
			return "", errors.New("ROOT: не удалось восстановить отдельную конфигурацию Claude")
		}
	}
	if ctx.Err() != nil {
		return "ROOT: запуск ответа отменён; сессия сохранена.", nil
	}
	cmd := elevatedCommand(p, text, ref)
	result, err := runElevatedJournaled(ctx, e.st, s.ID, cmd, func() (*ExecResult, error) { return e.engine.ExecElevated(ctx, id, cmd) })
	if result == nil {
		e.mu.Lock()
		e.blocked = true
		e.mu.Unlock()
		return "", fmt.Errorf("ROOT: результат выполнения не подтверждён; /root stop: %w", err)
	}
	reply := result.Stdout
	if s.Provider == "codex" {
		reply, native = parseElevatedCodex(result.Stdout)
	} else {
		parsed := readStructuredStream(strings.NewReader(result.Stdout), strings.NewReader(result.Stderr), p.(*ClaudeProvider).ParseStreamLine, func(string) {}, func(ProgressEvent) {})
		reply = parsed.Text
		native = parsed.SessionID
		if native == "" && result.ExitCode == 0 {
			native = ref.ID
		}
	}
	if native != "" && validSessionID(native) {
		saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		saveErr := e.st.PutElevatedNativeContext(saveCtx, s.ID, native)
		saveCancel()
		if saveErr != nil {
			return "", saveErr
		}
	}
	if ctx.Err() != nil {
		return "Ответ ROOT остановлен; сессия сохранена. Действия, уже выполненные на хосте, не отменяются. Отделённые от CLI процессы могут продолжать работу.", nil
	}
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("ROOT: провайдер завершился с кодом %d. Требуется отдельный elevated login; проверьте /root log. Автоматического повтора нет", result.ExitCode)
	}
	if err = e.st.AppendElevatedTranscript(ctx, s.ID, "assistant", reply); err != nil {
		return "", err
	}
	return reply, nil
}

func elevatedCommand(p AgentProvider, text string, ref SessionRef) []string {
	cmd := p.RunCmd(text, "You are in an explicitly confirmed host-root session. Workspace /workspace/session, host root /host. No ordinary chat IPC is available.", ref)
	if p.Name() == "claude" {
		out := []string{}
		for _, arg := range cmd {
			if arg == "--dangerously-skip-permissions" {
				out = append(out, "--permission-mode", "dontAsk", "--tools", "Bash,Read,Write,Edit,Glob,Grep", "--allowedTools", "Bash,Read,Write,Edit,Glob,Grep")
			} else {
				out = append(out, arg)
			}
		}
		return p.(*ClaudeProvider).StructuredCmd(out)
	}
	return append(cmd[:len(cmd)-1], "--json", cmd[len(cmd)-1])
}

func (e *Elevated) Log(ctx context.Context, limit int) (string, error) {
	rows, err := e.st.ListElevatedExecs(ctx, limit)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("⚠️ ROOT · журнал только host docker exec; не все действия агента, не защищён от host root.\n")
	for _, r := range rows {
		status := "исход неизвестен"
		if r.ExitCode.Valid {
			status = fmt.Sprintf("exit=%d", r.ExitCode.Int64)
		}
		fmt.Fprintf(&b, "#%d %s %s\n", r.ID, status, r.Argv)
	}
	text := []rune(b.String())
	if len(text) > 1500 {
		return string(text[:1400]) + "\n… вывод сокращён; уменьшите /root log n.", nil
	}
	return b.String(), nil
}
func (e *Elevated) BeginDrain()   { e.mu.Lock(); e.draining = true; e.mu.Unlock() }
func (e *Elevated) CancelDrain()  { e.mu.Lock(); e.draining = false; e.mu.Unlock() }
func (e *Elevated) CancelActive() { e.BeginDrain(); e.StopAnswer() }
func (e *Elevated) WaitDrained(ctx context.Context) error {
	e.mu.Lock()
	done := e.done
	e.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return e.Close(ctx, "restart drain")
}
func (e *Elevated) ForceDrain(ctx context.Context) error {
	e.CancelActive()
	return e.Close(ctx, "restart force")
}

// SetAdmission wires the shared host resource gate before serving requests.
func (e *Elevated) SetAdmission(r *Runner) { e.admission = r }
