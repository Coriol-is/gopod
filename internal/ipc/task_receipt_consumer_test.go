package ipc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/runner/chattmpl"
)

// Execute the shipped guide's consumer, not a Go reimplementation of its rules.
// CI must provide Node; local environments without it explicitly skip qualification.
func runTaskReceiptConsumer(t *testing.T, scenario string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("receipt consumer qualification requires Node")
		}
		t.Skip("Node unavailable: bundled consumer not qualified")
	}
	dir := t.TempDir()
	mem := filepath.Join(dir, "memory")
	if err = os.Mkdir(mem, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = chattmpl.Seed(dir, mem, chattmpl.Vars{ChatFolder: "alice"}); err != nil {
		t.Fatal(err)
	}
	guide, err := os.ReadFile(filepath.Join(dir, "SCHEDULING.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, code, ok := strings.Cut(string(guide), "```javascript\n")
	if !ok {
		t.Fatal("consumer example missing")
	}
	code, _, ok = strings.Cut(code, "// Submit one new operation.")
	if !ok {
		t.Fatal("consumer boundary missing")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", code+"\n"+scenario)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bundled consumer: %v\n%s", err, out)
	}
}

func TestTaskReceiptConsumerRejectsOldSuccessAfterCrash(t *testing.T) {
	for _, identical := range []bool{false, true} {
		t.Run(fmt.Sprint(identical), func(t *testing.T) {
			w, sink, base := claimHarness(t)
			raw, err := os.ReadFile(filepath.Join(base, "tasks", "replay.json"))
			if err != nil {
				t.Fatal(err)
			}
			next := raw
			if !identical {
				next = []byte(`{"op":"schedule","prompt":"changed","scheduleType":"interval","scheduleValue":"1h"}`)
			}
			expected := fmt.Sprintf("%x", sha256.Sum256(next))
			hit := false
			w.taskClaimHook = func(stage string) error {
				if stage == "receipt" {
					hit = true
					if err := os.Remove(filepath.Join(base, "results", "replay.result.json")); err != nil {
						t.Fatal(err)
					}
					replaceClaimInput(t, base, next)
					return errors.New("exit before terminal claim persistence")
				}
				return nil
			}
			w.tickOnce(t.Context())
			if !hit {
				t.Fatal("receipt checkpoint not reached")
			}
			w = restarted(w)
			w.tickOnce(t.Context())
			recovered := claimReceipt(t, base)
			w.tickOnce(t.Context())
			current := claimReceipt(t, base)
			if current.RequestHash != expected {
				t.Fatal("receipt bound to wrong bytes")
			}
			want := "error"
			if identical {
				want = "ok"
			}
			if current.Status != want {
				t.Fatalf("current receipt: %+v", current)
			}
			requireOneClaimTask(t, sink)
			oldJSON, _ := json.Marshal(recovered)
			newJSON, _ := json.Marshal(current)
			runTaskReceiptConsumer(t, fmt.Sprintf(`
const oldReceipt = %s;
const currentReceipt = %s;
const hash = %q;
if (matchesTaskReceipt(oldReceipt, 'replay.json', hash) !== %t) throw new Error('wrong recovered receipt attribution');
if (!matchesTaskReceipt(currentReceipt, 'replay.json', hash)) throw new Error('current result rejected');
await writeFile('result.json', JSON.stringify(oldReceipt));
const waiting = waitForTaskReceipt('result.json', 'replay.json', hash, 2000);
await delay(150);
await writeFile('result.tmp', JSON.stringify(currentReceipt));
await rename('result.tmp', 'result.json');
const accepted = await waiting;
if (accepted.status !== %q) throw new Error('accepted stale status');
`, oldJSON, newJSON, expected, identical, want))
		})
	}
}

func TestTaskReceiptHashUsesCompleteExactBytes(t *testing.T) {
	for _, raw := range []string{
		`{"op":"schedule","prompt":"original","scheduleType":"interval","scheduleValue":"1h"}`,
		" {\"op\":\"schedule\",\"prompt\":\"original\",\"scheduleType\":\"interval\",\"scheduleValue\":\"1h\"}\n",
		`{"invalid":`,
	} {
		t.Run(fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))[:8], func(t *testing.T) {
			w, _, base := claimHarness(t)
			replaceClaimInput(t, base, []byte(raw))
			w.tickOnce(t.Context())
			r := claimReceipt(t, base)
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
			if r.RequestHash != hash {
				t.Fatal("not exact-byte hash", r.RequestHash, hash)
			}
		})
	}
	w, _, base := claimHarness(t)
	replaceClaimInput(t, base, []byte(strings.Repeat(" ", (64<<10)+1)))
	w.tickOnce(t.Context())
	r := claimReceipt(t, base)
	if r.Status != "error" || r.RequestHash != "" {
		t.Fatalf("oversized prefix misidentified as request: %+v", r)
	}
}

func TestTaskReceiptConsumerIgnoresUnboundMalformedAndForeignReceipts(t *testing.T) {
	runTaskReceiptConsumer(t, `
const hash = createHash('sha256').update('exact bytes\n').digest('hex');
const good = {requestFile:'request.json', requestHash:hash, status:'error', error:'conflict'};
const invalid = [null, {}, {...good, requestHash:undefined}, {...good, requestHash:'0'.repeat(64)}, {...good, requestFile:'other.json'}, {...good, status:'queued'}];
for (const value of invalid) {
 if (matchesTaskReceipt(value, 'request.json', hash)) throw new Error('stale/unbound receipt accepted');
}
await writeFile('result.json', '{not JSON');
const pending = waitForTaskReceipt('result.json','request.json',hash,2000);
await delay(150);
await writeFile('result.tmp',JSON.stringify(good));
await rename('result.tmp','result.json');
const got = await pending;
if (got.error !== 'conflict') throw new Error('matching conflict lost');
let timedOut = false;
try { await waitForTaskReceipt('absent.json','request.json',hash,100); }
catch (error) { timedOut = error.message.includes('outcome unknown'); }
if (!timedOut) throw new Error('missing receipt claimed success');
`)
}
