//go:build !unix

package proactive

import "os"

const openFlags = os.O_RDONLY

// hasExtraLinks cannot tell on this platform; the other checks still apply.
func hasExtraLinks(os.FileInfo) bool { return false }
