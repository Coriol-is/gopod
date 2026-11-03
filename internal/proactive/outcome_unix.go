//go:build unix

package proactive

import (
	"os"
	"syscall"
)

// openFlags: never follow a symlink, never become a controlling terminal, never block.
const openFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NOCTTY | syscall.O_NONBLOCK

// hasExtraLinks reports whether the file has more than one hard link.
func hasExtraLinks(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
