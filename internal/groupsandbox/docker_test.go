package groupsandbox

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/runner"
)

func TestDockerCredentialFreeSnapshots(t *testing.T) {
	if os.Getenv("GOPOD_GROUP_DOCKER_TEST") != "1" {
		t.Skip("explicit disposable Docker qualification")
	}
	image := os.Getenv("GOPOD_GROUP_TOOL_IMAGE")
	if image == "" {
		t.Fatal("preloaded explicit group tool image required")
	}
	ctx := context.Background()
	d, err := runner.NewDocker(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	e := &Executor{Client: d.Client(), Root: root, Image: image, UID: os.Geteuid(), GID: os.Getegid()}
	t.Setenv("OPENAI_API_KEY", "synthetic-private-owner-key")
	out, err := e.Run(ctx, "namespace-first", `test ! -e /var/run/docker.sock && test ! -e /workspace/vault && test -z "$OPENAI_API_KEY" && printf group-proof > proof.txt && mkdir -p nested chat && printf nested > nested/file && printf preserved > chat/file && ulimit -Hv`)
	if err != nil || !strings.Contains(out, "32768") {
		t.Fatalf("isolated command/limits: %q %v", out, err)
	}
	out, err = e.Run(ctx, "namespace-first", `cat proof.txt nested/file chat/file`)
	if err != nil || out != "group-proofnestedpreserved" {
		t.Fatalf("snapshot paths/persistence: %q %v", out, err)
	}
	out, err = e.Run(ctx, "namespace-second", `test ! -e proof.txt && test ! -d nested && printf isolated`)
	if err != nil || out != "isolated" {
		t.Fatalf("namespace separation: %q %v", out, err)
	}
	if _, err = e.Run(ctx, "namespace-first", `ln -s /etc/passwd escape`); err == nil {
		t.Fatal("symlink exported")
	}
	out, err = e.Run(ctx, "namespace-first", `test ! -e escape && cat proof.txt`)
	if err != nil || out != "group-proof" {
		t.Fatalf("failed export changed prior snapshot: %q %v", out, err)
	}
}
