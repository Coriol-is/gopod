package runner

import (
	"context"
	"errors"

	"github.com/Coriol-is/gopod/internal/memory"
)

var ErrGroupSensitiveInput = errors.New("authentication material is unavailable in shared agent context; use private owner authentication")

func (r *Runner) checkGroupInput(ctx context.Context, folder, prompt string) error {
	_, group, err := r.groupRegistration(ctx, folder)
	if err != nil {
		return err
	}
	if group && memory.IsGroupSensitiveText(prompt) {
		return ErrGroupSensitiveInput
	}
	return nil
}
