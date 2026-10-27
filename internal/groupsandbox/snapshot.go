package groupsandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

const maxSnapshot = 8 << 20

// canonicalArchive never extracts agent-controlled archives on the host.
// Symlinks, hardlinks, special files, traversal, duplicate names and unbounded
// metadata are rejected. Re-emitted metadata cannot smuggle alternate ownership.
func canonicalArchive(r io.Reader, uid, gid int) ([]byte, error) {
	return archiveMode(r, uid, gid, false)
}
func archiveMode(r io.Reader, uid, gid int, stripDockerRoot bool) ([]byte, error) {
	tr := tar.NewReader(io.LimitReader(r, maxSnapshot+(1<<20)))
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	seen := map[string]bool{}
	var size int64
	rootPrefix := ""
	for count := 0; ; count++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || count > 512 {
			return nil, ErrUnavailable
		}
		name := strings.TrimPrefix(h.Name, "./")
		if count == 0 && stripDockerRoot && h.Typeflag == tar.TypeDir && strings.TrimSuffix(name, "/") == "chat" {
			rootPrefix = "chat/"
			continue
		}
		if rootPrefix != "" {
			if !strings.HasPrefix(name, rootPrefix) {
				return nil, ErrUnavailable
			}
			name = strings.TrimPrefix(name, rootPrefix)
		}
		if (name == "." || name == "") && h.Typeflag == tar.TypeDir {
			continue
		}
		name = strings.TrimSuffix(name, "/")
		if !path.IsAbs(name) && path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../") && !strings.Contains(name, "\\") && strings.Count(name, "/") < 16 && !seen[name] {
		} else {
			return nil, ErrUnavailable
		}
		seen[name] = true
		clean := &tar.Header{Name: name, Uid: uid, Gid: gid, Mode: 0600, Typeflag: h.Typeflag, Size: h.Size}
		switch h.Typeflag {
		case tar.TypeDir:
			clean.Mode = 0700
			clean.Size = 0
		case tar.TypeReg:
			if h.Size < 0 || h.Size > 4<<20 {
				return nil, ErrUnavailable
			}
			size += h.Size
			if size > maxSnapshot {
				return nil, ErrUnavailable
			}
		default:
			return nil, ErrUnavailable
		}
		if err = tw.WriteHeader(clean); err != nil {
			return nil, ErrUnavailable
		}
		if clean.Typeflag == tar.TypeReg {
			if _, err = io.CopyN(tw, tr, clean.Size); err != nil {
				return nil, ErrUnavailable
			}
		}
		if out.Len() > maxSnapshot+(512<<10) {
			return nil, ErrUnavailable
		}
	}
	if err := tw.Close(); err != nil {
		return nil, ErrUnavailable
	}
	return out.Bytes(), nil
}

func (e *Executor) snapshot(scope string) ([]byte, error) {
	dir, err := e.scopeDir(scope)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile("snapshot.tar", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxSnapshot+(1<<20) {
		return nil, ErrUnavailable
	}
	return canonicalArchive(f, e.UID, e.GID)
}
func (e *Executor) save(scope string, r io.Reader) error {
	return e.saveChecked(context.Background(), scope, r, func(context.Context) error { return nil })
}
func (e *Executor) saveChecked(ctx context.Context, scope string, r io.Reader, validate func(context.Context) error) error {
	return e.saveArchive(ctx, scope, r, validate, false)
}
func (e *Executor) saveDockerChecked(ctx context.Context, scope string, r io.Reader, validate func(context.Context) error) error {
	return e.saveArchive(ctx, scope, r, validate, true)
}
func (e *Executor) saveArchive(ctx context.Context, scope string, r io.Reader, validate func(context.Context) error, stripDockerRoot bool) error {
	data, err := archiveMode(r, e.UID, e.GID, stripDockerRoot)
	if err != nil {
		return err
	}
	dir, err := e.scopeDir(scope)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if ctx.Err() != nil || validate(ctx) != nil {
		return ErrUnavailable
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, "snapshot.tar")); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
