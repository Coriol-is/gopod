package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// pruneDir removes the oldest files (by name ascending) so that at
// most max files remain in dir. Subdirectories are ignored. Errors
// from individual removes are returned wrapped; callers log and move on.
func pruneDir(dir string, max int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("ipc: pruneDir readdir %q: %w", dir, err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		files = append(files, e.Name())
	}
	if len(files) <= max {
		return nil
	}
	sort.Strings(files) // ascending = oldest first (timestamp-prefixed)
	toRemove := files[:len(files)-max]
	for _, name := range toRemove {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("ipc: pruneDir remove %q: %w", name, err)
		}
	}
	return nil
}
