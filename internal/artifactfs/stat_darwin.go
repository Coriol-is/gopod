//go:build darwin

package artifactfs

import "golang.org/x/sys/unix"

func mtimeNs(st *unix.Stat_t) int64 { return st.Mtim.Nano() }
func ctimeNs(st *unix.Stat_t) int64 { return st.Ctim.Nano() }
