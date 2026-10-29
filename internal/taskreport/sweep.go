package taskreport

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// DirName is the report directory under the attempt's IPC namespace.
const DirName = "task-results"

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

// Sweep admits every new regular .json file in Dir.
func (s *Sweeper) Sweep(ctx context.Context) Result {
	res := Result{Rejected: map[Code]int{}}
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
			s.seen[name] = true
			res.Rejected[code]++
			continue
		}
		p, _, err := s.Grants.Admit(s.Folder, raw)
		if err != nil {
			s.seen[name] = true
			c, _ := CodeOf(err)
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
