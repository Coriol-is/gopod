package runner

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
)

// procMeminfo is the kernel's memory report. Inside the gopod container
// it is the host's (/proc is not namespaced for memory).
const procMeminfo = "/proc/meminfo"

// readMemAvailableMB returns the host's MemAvailable in MiB. ok is false
// when it cannot be read (not Linux, no MemAvailable line); the agent
// gate then treats memory as sufficient.
func readMemAvailableMB() (int64, bool) { return readMemAvailableMBFrom(procMeminfo) }

func readMemAvailableMBFrom(path string) (int64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	return parseMemAvailableMB(f)
}

// parseMemAvailableMB finds the "MemAvailable: <n> kB" line of a
// /proc/meminfo text and returns n in MiB.
func parseMemAvailableMB(r io.Reader) (int64, bool) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		rest, found := strings.CutPrefix(sc.Text(), "MemAvailable:")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) != 2 || fields[1] != "kB" {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || kb < 0 {
			return 0, false
		}
		return kb / 1024, true
	}
	return 0, false
}
