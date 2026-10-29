package taskreport

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// DirName is the report directory under the attempt's IPC namespace.
const DirName = "task-results"

// Environment names that carry the attempt identity into the agent. None of
// them contains KEY, SECRET or TOKEN: Codex's shell_environment_policy strips
// such names from tool subprocesses, which would hide the capability from the
// very commands that write the report.
const (
	EnvRunID      = "GOPOD_TASK_RUN_ID"
	EnvAttemptID  = "GOPOD_TASK_ATTEMPT_ID"
	EnvCapability = "GOPOD_TASK_REPORT_CAP"
)

// EnsureDir creates dir if missing and refuses anything that is not a plain
// directory (a symlink or file planted from the container), without following.
func EnsureDir(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0:
		return nil
	case err == nil:
		return errNotPlainDir
	case !os.IsNotExist(err):
		return err
	}
	return os.Mkdir(dir, 0o755)
}

var errNotPlainDir = errors.New("taskreport: report directory is not a plain directory")

// Sweeper reads report files an agent wrote under one IPC namespace and hands
// admitted ones to Record. It never opens a path named inside a report, never
// follows symlinks and never deletes or moves agent files. Each file name is
// admitted at most once per sweeper, in name order, so a later name is a later
// revision.
type Sweeper struct {
	Grants *Grants
	Dir    string // host directory behind /workspace/ipc/task-results
	Folder string
	// Record persists a provisional report. A returned error leaves the file
	// eligible for the next sweep.
	Record func(ctx context.Context, p Provisional) error

	seen map[string]bool
}

// Result counts one sweep.
type Result struct {
	Admitted int
	Rejected map[Code]int
}

// Sweep admits every new regular .json file in Dir. A file that does not parse
// may still be being written, so outside the final sweep it is neither counted
// nor marked seen and is read again; final=true rules on it for good.
func (s *Sweeper) Sweep(ctx context.Context, final bool) Result {
	res := Result{Rejected: map[Code]int{}}
	if info, err := os.Lstat(s.Dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return res // never read through a symlink planted at the directory
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return res
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	for _, name := range names {
		if ctx.Err() != nil {
			return res
		}
		if s.seen[name] {
			continue
		}
		raw, code := readReport(filepath.Join(s.Dir, name))
		if code != "" {
			if final {
				s.seen[name] = true
				res.Rejected[code]++
			}
			continue
		}
		p, _, err := s.Grants.Admit(s.Folder, raw)
		if err != nil {
			c, _ := CodeOf(err)
			if c == CodeInvalid && !final {
				continue // possibly a partial write
			}
			s.seen[name] = true
			res.Rejected[c]++
			continue
		}
		if err := s.Record(ctx, p); err != nil {
			res.Rejected[CodeStale]++
			continue // not marked seen: retried while the capability lives
		}
		s.seen[name] = true
		res.Admitted++
	}
	return res
}

// readReport reads a regular, non-symlink file of at most MaxEnvelopeBytes. The
// open itself refuses a symlink, so a swap after a listing cannot redirect it.
func readReport(path string) ([]byte, Code) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, CodeInvalid
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, CodeInvalid
	}
	if info.Size() > MaxEnvelopeBytes {
		return nil, CodeTooLarge
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxEnvelopeBytes+1))
	if err != nil {
		return nil, CodeInvalid
	}
	if len(raw) > MaxEnvelopeBytes {
		return nil, CodeTooLarge
	}
	return raw, ""
}
