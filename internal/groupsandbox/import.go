package groupsandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path"
	"strings"
)

// ImportUploads admits regular controller-downloaded files through a protected
// directory handle. The native CLI never mounts these files or this directory.
func (e *Executor) ImportUploads(ctx context.Context, scope, workspace, guide string, validate func(context.Context) error) error {
	unlock := e.lock(scope)
	defer unlock()
	if !validScope(scope) || validate == nil || validate(ctx) != nil {
		return ErrUnavailable
	}
	seed, err := e.snapshot(scope)
	if err != nil {
		return err
	}
	files := map[string][]byte{}
	var total int64
	put := func(name string, data []byte) error {
		next := total - int64(len(files[name])) + int64(len(data))
		if next > maxSnapshot || len(data) > 4<<20 {
			return ErrUnavailable
		}
		files[name] = data
		total = next
		return nil
	}
	tr := tar.NewReader(bytes.NewReader(seed))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ErrUnavailable
		}
		// Uploads are a bounded input window, refreshed each turn. Preserve other
		// tool-created files but do not accumulate old imported uploads forever.
		if strings.HasPrefix(h.Name, "uploads/") {
			continue
		}
		if h.Typeflag == tar.TypeReg {
			b, err := io.ReadAll(io.LimitReader(tr, 4<<20))
			if err != nil {
				return ErrUnavailable
			}
			if err = put(h.Name, b); err != nil {
				return err
			}
		}
	}
	if err = put("SCHEDULING.md", []byte(guide)); err != nil {
		return err
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return ErrUnavailable
	}
	defer root.Close()
	base, err := root.Open(".")
	if err != nil {
		return ErrUnavailable
	}
	defer base.Close()
	fd, err := unix.Openat(int(base.Fd()), "uploads", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	var dir *os.File
	if err == nil {
		dir = os.NewFile(uintptr(fd), "uploads")
	}
	if err != nil && !os.IsNotExist(err) {
		return ErrUnavailable
	}
	if err == nil {
		defer dir.Close()
		// Bound directory scanning as well as retained bytes. This is the first
		// 128 directory entries, not newest-first: a full input directory can
		// defer newer attachments until earlier host uploads are removed.
		entries, err := dir.ReadDir(128)
		if err != nil && err != io.EOF {
			return ErrUnavailable
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.ContainsAny(name, "/\\") || name == "." || name == ".." || entry.IsDir() {
				continue
			}
			fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if err != nil {
				continue
			}
			f := os.NewFile(uintptr(fd), name)
			info, err := f.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
				_ = f.Close()
				continue
			}
			key := "uploads/" + name
			available := int64(maxSnapshot) - total + int64(len(files[key]))
			if available < 0 || info.Size() > available {
				_ = f.Close()
				continue
			}
			if available > 4<<20 {
				available = 4 << 20
			}
			b, err := io.ReadAll(io.LimitReader(f, available+1))
			_ = f.Close()
			if err != nil || int64(len(b)) > available {
				continue
			}
			if err = put(key, b); err != nil {
				return err
			}
		}
	}
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	parents := map[string]bool{}
	for name, data := range files {
		parent := path.Dir(name)
		if parent != "." && !parents[parent] {
			if err = tw.WriteHeader(&tar.Header{Name: parent, Typeflag: tar.TypeDir, Mode: 0700, Uid: e.UID, Gid: e.GID}); err != nil {
				return ErrUnavailable
			}
			parents[parent] = true
		}
		if err = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data)), Uid: e.UID, Gid: e.GID}); err != nil {
			return ErrUnavailable
		}
		if _, err = tw.Write(data); err != nil {
			return ErrUnavailable
		}
	}
	if err = tw.Close(); err != nil {
		return ErrUnavailable
	}
	return e.saveChecked(ctx, scope, bytes.NewReader(out.Bytes()), validate)
}
