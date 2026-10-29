package groupruntime

import (
	"context"
	"errors"

	"github.com/Coriol-is/gopod/internal/runner"
)

// checkTree enforces one private-tree budget. A refusal is logged on the host
// with its fixed reason code and sanitized path class only, and the typed error
// travels up so owner-private diagnostics (not group chats) can show it.
func (m *Manager) checkTree(ctx context.Context, dir string, p treePolicy) error {
	err := privateTreeBudget(ctx, dir, p)
	var tb *TreeBudgetError
	if errors.As(err, &tb) && m.Log != nil {
		m.Log.Warn("group private tree refused", "tree", tb.Tree, "reason", string(tb.Reason), "class", tb.Class)
	}
	return err
}

func (m *Manager) checkTrees(ctx context.Context, paths runner.NamespacePaths) error {
	if err := m.checkTree(ctx, paths.Native, nativeTreePolicy); err != nil {
		return err
	}
	if err := m.checkTree(ctx, paths.Workspace, workspaceTreePolicy); err != nil {
		return err
	}
	return m.checkTree(ctx, paths.Policy, policyTreePolicy)
}

// newAdmissionError records the structural stage and, when the cause is a tree
// budget refusal, its fixed detail string.
func newAdmissionError(stage string, cause error) *AdmissionError {
	e := &AdmissionError{Stage: stage}
	var tb *TreeBudgetError
	if errors.As(cause, &tb) {
		e.Detail = tb.Detail()
	}
	return e
}
