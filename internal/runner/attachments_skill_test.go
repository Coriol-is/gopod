package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CP-040: the container skill exists, is discoverable (frontmatter name and
// description) and states the same request shape and refusal handling as the
// system-prompt policy.
func TestAttachmentsContainerSkillStatesTheContract(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "container", "skills", "attachments", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	skill := string(b)
	if !strings.HasPrefix(skill, "---\nname: attachments\ndescription: ") {
		t.Fatalf("skill frontmatter missing or wrong:\n%.200s", skill)
	}
	for _, want := range []string{
		`"version":1`, `"capability":"$GOPOD_ARTIFACT_CAP"`, `"as":"auto"`, "/workspace/ipc/messages/",
		"/workspace/ipc/results/messages/", "NOT an attachment", "artifacts_disabled", "path_outside_workspace",
		"file_too_large", "unsupported_type", "20 MiB", "could not", "never claim it was sent",
	} {
		if !strings.Contains(skill, want) {
			t.Errorf("skill lacks %q", want)
		}
	}
}
