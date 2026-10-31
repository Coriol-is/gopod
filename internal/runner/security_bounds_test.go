package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/docker/docker/client"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

type transientRegistrations struct{ calls int }

func (s *transientRegistrations) ListRegistered(context.Context) ([]store.RegisteredChat, error) {
	s.calls++
	if s.calls == 1 {
		return nil, errors.New("SQLITE_BUSY")
	}
	return []store.RegisteredChat{{JID: "tg:-2", Folder: "group"}}, nil
}
func TestTransientRegistrationFailureNeverFallsBackToOwner(t *testing.T) {
	for _, path := range []string{"ensure", "turn", "run", "stream", "fresh", "input", "workspace"} {
		t.Run(path, func(t *testing.T) {
			r := &Runner{}
			r.SetAdmissionPolicy(1, &transientRegistrations{})
			r.groups = &selectedGroupRuntime{}
			ctx := context.Background()
			var err error
			switch path {
			case "ensure":
				_, err = r.Ensure(ctx, "group", TierOwner, nil)
			case "turn":
				_, _, _, err = r.turnRuntime(ctx, "group", 0, TierOwner, nil)
			case "run":
				_, err = r.Run(ctx, "group", 0, TierOwner, nil, "hello")
			case "stream":
				_, err = r.RunStream(ctx, "group", 0, TierOwner, nil, "hello")
			case "fresh":
				_, err = r.RunFresh(ctx, "group", TierOwner, nil, "hello")
			case "input":
				err = r.checkGroupInput(ctx, "group", "hello")
			case "workspace":
				_, err = r.ChatWorkspace(ctx, "group")
			}
			if !errors.Is(err, ErrProfileUnavailable) {
				t.Fatalf("not denied: %v", err)
			}
		})
	}
}
func TestPersonalRuntimeRequiresPositiveOwner(t *testing.T) {
	r := &Runner{}
	r.SetAdmissionPolicy(1, profileRegistrations{rows: []store.RegisteredChat{{JID: "tg:-2", Folder: "group"}}})
	r.groups = &selectedGroupRuntime{}
	if _, err := r.Ensure(t.Context(), "group", TierOwner, nil); !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("group admitted to personal runtime: %v", err)
	}
}

type validationProvider struct {
	AgentProvider
	calls int
	err   error
}

func (p *validationProvider) ValidateExecution(context.Context) error { p.calls++; return p.err }
func TestOutputValidationBoundedAndDenialLatched(t *testing.T) {
	p := &validationProvider{AgentProvider: NewClaudeProvider("")}
	now := time.Now()
	check := outputValidator(t.Context(), p, func() time.Time { return now })
	for range 10000 {
		if err := check(); err != nil {
			t.Fatal(err)
		}
	}
	if p.calls != 1 {
		t.Fatalf("checks=%d", p.calls)
	}
	now = now.Add(outputValidationInterval)
	p.err = ErrProfileUnavailable
	if !errors.Is(check(), ErrProfileUnavailable) {
		t.Fatal("revocation missed")
	}
	p.err = nil
	now = now.Add(outputValidationInterval)
	if !errors.Is(check(), ErrProfileUnavailable) || p.calls != 2 {
		t.Fatal("denial not latched")
	}
}
func TestGroupTurnDeadlineLeavesOwnerUnbounded(t *testing.T) {
	ctx, cancel := groupTurnContext(t.Context(), true)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > groupTurnTimeout {
		t.Fatal("group deadline missing")
	}
	owner, stop := groupTurnContext(context.Background(), false)
	defer stop()
	if _, ok := owner.Deadline(); ok {
		t.Fatal("owner deadline added")
	}
}
func TestOutputDrainsBeyondMemoryBound(t *testing.T) {
	text := strings.Repeat("a", maxAgentOutputBytes+1024) + "tail"
	emitted := 0
	out, stderr := readExecStream(strings.NewReader(text), strings.NewReader(text), func(s string) { emitted += len(s) })
	if emitted != len(text) || len(out) > maxAgentOutputBytes || len(stderr) > maxAgentOutputBytes || !strings.HasSuffix(out, "tail") {
		t.Fatal("output not bounded and drained")
	}
}
func TestOversizedStructuredLineDrainedBeforeNextEvent(t *testing.T) {
	br := bufio.NewReaderSize(strings.NewReader(strings.Repeat("a", maxAgentOutputBytes+1)+"\nnext\n"), 64<<10)
	line, err := readBoundedLine(br)
	if err != nil || len(line) != 0 {
		t.Fatalf("oversized line retained: %d %v", len(line), err)
	}
	line, err = readBoundedLine(br)
	if err != nil || string(line) != "next\n" {
		t.Fatal("next line lost")
	}
}
func TestRootUIDWithNonRootGIDDenied(t *testing.T) {
	c := SpawnConfig{Image: "image", ChatFolder: "group", Version: "test", UID: 0, GID: 1000}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "uid=0") {
		t.Fatalf("root uid not denied: %v", err)
	}
}

func TestIdleCleanupFindsNativeContainerByLabel(t *testing.T) {
	var mu sync.Mutex
	var mutations []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := strings.TrimPrefix(req.URL.Path, "/v1.47")
		w.Header().Set("Content-Type", "application/json")
		if path == "/containers/json" {
			fmt.Fprint(w, `[{"Id":"native-id","Names":["/gopod-native-opaque"],"Labels":{"gopod.chat":"group"}},{"Id":"other-id","Labels":{"gopod.chat":"owner"}}]`)
			return
		}
		mu.Lock()
		mutations = append(mutations, req.Method+" "+path)
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer srv.Close()
	cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithVersion("1.47"), client.WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	r := silentRunner()
	r.d = &Docker{cli: cli, log: quietLog()}
	r.touch("group")
	r.killIdleChat(t.Context(), "group")
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(mutations, ",") != "POST /containers/native-id/stop,DELETE /containers/native-id" {
		t.Fatalf("wrong cleanup: %v", mutations)
	}
	if !r.LastActivity("group").IsZero() {
		t.Fatal("activity retained")
	}
}
