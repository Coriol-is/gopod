package runner

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// testdataUUIDAllow lists the UUIDs testdata may contain. Recorded
// fixtures must use placeholders (sess-0001, uuid-0001, msg_0001, ...)
// instead of real session or request ids.
var testdataUUIDAllow = map[string]bool{
	// Codex thread ids in the codex_exec_* fixtures (published on main
	// before this check existed; throwaway sessions).
	"01a16df7-698c-7ad3-b9f7-1e54b7681b73": true,
	"01a16df7-d74d-76d1-90d4-e2b3d3ad2717": true,
}

// forbiddenInTestdata are the recording machine's user name, home and
// temp paths. Split so that this file itself does not contain them
// (history is grepped for them).
var forbiddenInTestdata = []string{"space" + "invaderz", "/Users" + "/", "/private" + "/tmp", "claude" + "-501"}

var uuidLike = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// TestTestdataHasNoPersonalData guards recorded fixtures against the
// recording machine's paths and real ids.
func TestTestdataHasNoPersonalData(t *testing.T) {
	files, err := filepath.Glob("testdata/*")
	if err != nil || len(files) == 0 {
		t.Fatalf("no testdata: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, bad := range forbiddenInTestdata {
			if strings.Contains(s, bad) {
				t.Errorf("%s contains %q", f, bad)
			}
		}
		for _, id := range uuidLike.FindAllString(s, -1) {
			if !testdataUUIDAllow[strings.ToLower(id)] {
				t.Errorf("%s contains a real-looking id %s; use a placeholder", f, id)
			}
		}
	}
}
