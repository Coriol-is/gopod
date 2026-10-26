package groupruntime

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Native clients are trusted pinned executables with no file/code tools. Bound
// admission by their own state size; individual native files also have a kernel
// fsize limit. User commands run elsewhere with stricter tmpfs/RLIMIT bounds.
func privateTreeBudget(path string, bytesLimit int64, filesLimit int) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return ErrUnavailable
	}
	defer root.Close()
	var total int64
	count := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return ErrUnavailable
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrUnavailable
		}
		info, err := entry.Info()
		if err != nil {
			return ErrUnavailable
		}
		if !info.IsDir() {
			if !info.Mode().IsRegular() {
				return ErrUnavailable
			}
			total += info.Size()
			count++
		}
		if total > bytesLimit || count > filesLimit || len(filepath.SplitList(name)) > 64 {
			return ErrUnavailable
		}
		return nil
	})
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
