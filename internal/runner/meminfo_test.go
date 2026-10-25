package runner

import (
	"strings"
	"testing"
)

// A /proc/meminfo sample from the Raspberry Pi 4B deploy host.
const procMeminfoSample = `MemTotal:        3884432 kB
MemFree:          214160 kB
MemAvailable:    1843200 kB
Buffers:           61840 kB
Cached:          1622512 kB
SwapCached:        11264 kB
Active:          1405288 kB
Inactive:        1792128 kB
SwapTotal:        102396 kB
SwapFree:          22012 kB
`

func TestParseMemAvailableMB(t *testing.T) {
	got, ok := parseMemAvailableMB(strings.NewReader(procMeminfoSample))
	if !ok {
		t.Fatal("MemAvailable not found")
	}
	if got != 1800 { // 1843200 kB / 1024
		t.Fatalf("MemAvailable = %d MB, want 1800", got)
	}
}

func TestParseMemAvailableMBMissingField(t *testing.T) {
	// Kernels before 3.14 have no MemAvailable line.
	in := "MemTotal:        3884432 kB\nMemFree:          214160 kB\n"
	if got, ok := parseMemAvailableMB(strings.NewReader(in)); ok {
		t.Fatalf("got %d MB, want not ok", got)
	}
}

func TestParseMemAvailableMBGarbage(t *testing.T) {
	for _, in := range []string{"", "MemAvailable:\n", "MemAvailable: lots kB\n", "MemAvailable: 12 MB\n"} {
		if got, ok := parseMemAvailableMB(strings.NewReader(in)); ok {
			t.Errorf("%q: got %d MB, want not ok", in, got)
		}
	}
}

func TestReadMemAvailableMBMissingFile(t *testing.T) {
	if _, ok := readMemAvailableMBFrom("/nonexistent/meminfo"); ok {
		t.Fatal("missing file read as ok")
	}
}
