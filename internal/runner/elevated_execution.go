package runner

import (
	"context"
	"errors"
	"github.com/Coriol-is/gopod/internal/store"
)

type elevatedExecution struct {
	ctx     context.Context
	session store.ElevatedSession
	cfg     ElevatedSpawnConfig
	id      string
	finish  func()
	release func()
}

// beginExecution shares admission and teardown fencing between inference and login.
func (e *Elevated) beginExecution(ctx context.Context) (*elevatedExecution, error) {
	e.mu.Lock()
	if e.session.ID == "" || e.blocked || e.draining || e.busy {
		e.mu.Unlock()
		return nil, errors.New("ROOT не готов или занят. Сообщение не поставлено в очередь; /root status")
	}
	ctx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.busy = true
	e.done = make(chan struct{})
	done := e.done
	s, cfg, id := e.session, e.base, e.container
	e.mu.Unlock()
	x := &elevatedExecution{ctx: ctx, session: s, cfg: cfg, id: id}
	x.finish = func() {
		cancel()
		e.mu.Lock()
		defer e.mu.Unlock()
		if x.release != nil {
			if e.blocked {
				e.heldRelease = x.release
			} else {
				x.release()
			}
		}
		e.cancel = nil
		e.busy = false
		e.done = nil
		close(done)
	}
	acquired := false
	defer func() {
		if !acquired {
			x.finish()
		}
	}()
	facts, inspectErr := e.engine.InspectContainer(ctx, ContainerName(ElevatedFolder))
	if inspectErr != nil || facts == nil || !facts.Running || facts.ID != id {
		e.mu.Lock()
		e.blocked = true
		e.mu.Unlock()
		return nil, errors.New("ROOT: контейнер потерян; автоматического пересоздания нет. /root stop")
	}
	plan, err := elevatedPlan(cfg)
	image, imageErr := e.engine.ImageID(ctx, cfg.Image)
	if err != nil || imageErr != nil || facts.Labels[LabelPolicyConfig] != plan.Conf.Labels[LabelPolicyConfig] || decideContainer(plan, facts, image, nil).Action != actReuse {
		e.mu.Lock()
		e.blocked = true
		e.mu.Unlock()
		return nil, errors.New("ROOT: контейнер потерян или изменён. Выполнение запрещено; /root stop")
	}
	var release func()
	if e.admission != nil {
		release, err = e.admission.acquireAgentSlot(ctx, ElevatedFolder)
		if err != nil {
			return nil, err
		}
	}
	if release != nil {
		releaseSlot := release
		releaseGate, gateErr := e.admission.admit(ctx, ElevatedFolder, ClassInteractive)
		if gateErr != nil {
			releaseSlot()
			return nil, gateErr
		}
		release = func() { releaseGate(); releaseSlot() }
	}
	x.release = release
	acquired = true
	return x, nil
}
