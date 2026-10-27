package runner

import (
	"context"
	"errors"

	"github.com/Coriol-is/gopod/internal/memory"
)

var ErrGroupSensitiveInput = errors.New("authentication material is unavailable in shared agent context; use private owner authentication")

func (r *Runner) checkGroupInput(ctx context.Context, folder, prompt string) error {
	if _, group := r.groupRegistration(ctx, folder); group && memory.IsGroupSensitiveText(prompt) {
		return ErrGroupSensitiveInput
	}
	return nil
}
