package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type elevatedExecJournal interface {
	BeginElevatedExec(context.Context, string, []string) (int64, error)
	FinishElevatedExec(context.Context, int64, int) error
}

// runElevatedJournaled is the mandatory boundary for host execs into elevated.
// A failed intent prevents the exec; failed settlement prevents result delivery.
func runElevatedJournaled(ctx context.Context, j elevatedExecJournal, session string, argv []string, run func() (*ExecResult, error)) (*ExecResult, error) {
	id, err := j.BeginElevatedExec(ctx, session, argv)
	if err != nil {
		return nil, fmt.Errorf("elevated exec intent: %w", err)
	}
	result, runErr := run()
	code := -1 // transport failure: no attested exit code
	if result != nil {
		code = result.ExitCode
	}
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := j.FinishElevatedExec(settle, id, code); err != nil {
		return nil, errors.Join(runErr, fmt.Errorf("elevated exec settlement: %w", err))
	}
	return result, runErr
}

// EnsureElevatedDirs creates only reserved workspace and native-state homes.
// It never creates an IPC namespace or inherits ordinary chat templates.
func EnsureElevatedDirs(p Paths) (workspace, claude, codex string, err error) {
	if err = p.Validate(); err != nil {
		return
	}
	for base, dirs := range map[string][]string{p.ChatsDir: {ElevatedFolder}, p.DataDir: {"sessions", filepath.Join("sessions", ElevatedFolder), filepath.Join("sessions", ElevatedFolder, ".claude"), filepath.Join("sessions", ElevatedFolder, ".codex")}} {
		if err = makeElevatedDirs(base, dirs); err != nil {
			return
		}
	}
	workspace = filepath.Join(p.ChatsDir, ElevatedFolder)
	claude = filepath.Join(p.DataDir, "sessions", ElevatedFolder, ".claude")
	codex = filepath.Join(p.DataDir, "sessions", ElevatedFolder, ".codex")
	return
}

func makeElevatedDirs(base string, dirs []string) error {
	root, err := os.OpenRoot(base)
	if err != nil {
		return fmt.Errorf("elevated open root: %w", err)
	}
	defer root.Close()
	for _, dir := range dirs {
		if err := root.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("elevated mkdir: %w", err)
		}
		info, err := root.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("elevated: reserved state path is not a real directory")
		}
		if dir == ElevatedFolder || dir == filepath.Join("sessions", ElevatedFolder) {
			if err := claimElevatedDirectory(root, dir); err != nil {
				return err
			}
		}
	}
	return nil
}

// The marker distinguishes directories bootstrapped by this feature from old
// ordinary workspaces. It is provenance evidence, not a security seal against root.
func claimElevatedDirectory(root *os.Root, dir string) error {
	const marker = ".gopod-elevated-v1"
	name := filepath.Join(dir, marker)
	info, err := root.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("elevated: invalid provenance marker")
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		body, err := io.ReadAll(io.LimitReader(f, 64))
		if err != nil {
			return err
		}
		if string(body) != "gopod elevated v1\n" {
			return errors.New("elevated: invalid provenance marker")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	d, err := root.Open(dir)
	if err != nil {
		return err
	}
	names, readErr := d.Readdirnames(1)
	closeErr := d.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(names) > 0 {
		return errors.New("elevated: refusing pre-existing unowned workspace or credentials")
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString("gopod elevated v1\n")
	closeErr = f.Close()
	return errors.Join(writeErr, closeErr)
}
