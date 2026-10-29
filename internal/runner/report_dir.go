package runner

import (
	"fmt"
	"os"
)

// ensureReportDir creates the task-results directory the task role bind-mounts.
// A symlink or file at that name (planted from a container that had the whole
// namespace mounted) is refused, never followed.
func ensureReportDir(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0:
		return nil
	case err == nil:
		return fmt.Errorf("%w: report directory is not a plain directory", ErrTaskDenied)
	case !os.IsNotExist(err):
		return fmt.Errorf("runner: report directory: %w", err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return fmt.Errorf("runner: report directory: %w", err)
	}
	return nil
}
