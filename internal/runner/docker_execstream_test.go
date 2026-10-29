package runner

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/client"
)

// execDaemon is a fake Docker daemon speaking just enough of the exec API to
// drive the REAL Docker.ExecStream loop (no handle fields are set by the test).
type execDaemon struct {
	t       *testing.T
	running atomic.Bool
	exit    atomic.Int64
	conn    chan io.Closer
}

func newExecDaemon(t *testing.T) (*Docker, *execDaemon) {
	d := &execDaemon{t: t, conn: make(chan io.Closer, 1)}
	d.running.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch strings.TrimPrefix(req.URL.Path, "/v1.47") {
		case "/containers/c1/exec":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"Id":"e1"}`)
		case "/exec/e1/start":
			_, _ = io.Copy(io.Discard, req.Body)
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			fmt.Fprint(rw, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			_ = rw.Flush()
			d.conn <- conn
		case "/exec/e1/json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"ID":"e1","Running":%v,"ExitCode":%d}`, d.running.Load(), d.exit.Load())
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+srv.Listener.Addr().String()), client.WithVersion("1.47"), client.WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return &Docker{cli: cli, log: quietLog()}, d
}

// die is the kill landing: the process is gone, the daemon reports it, the
// stream ends.
func (d *execDaemon) die(code int64) {
	d.exit.Store(code)
	d.running.Store(false)
	(<-d.conn).Close()
}

func waitDone(t *testing.T, h *StreamHandle) error {
	t.Helper()
	go io.Copy(io.Discard, h.Stdout)
	go io.Copy(io.Discard, h.Stderr)
	select {
	case err := <-h.Done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Done never completed")
	}
	return nil
}

// The exit of a run whose context ended (task timeout) and was then killed is
// still observed: the exit wait does not depend on the cancelled exec context.
func TestExecStreamObservesExitAfterContextEnds(t *testing.T) {
	dk, daemon := newExecDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	h, err := dk.ExecStream(ctx, "c1", []string{"sleep"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel() // timeout fires; the kill exec follows
	time.Sleep(150 * time.Millisecond)
	if _, ok := h.ExitObserved(); ok {
		t.Fatal("exit observed before the process died")
	}
	daemon.die(143)
	var ee *ExecError
	if err := waitDone(t, h); err == nil {
		t.Fatal("want ExecError for 143")
	} else if e, ok := err.(*ExecError); ok {
		ee = e
	}
	if ee == nil || ee.Code != 143 {
		t.Fatalf("done = %v", ee)
	}
	if code, ok := h.ExitObserved(); !ok || code != 143 {
		t.Fatalf("exit = %d %v, want 143 observed", code, ok)
	}
}

// Close abandons the wait: a dropped attachment is never exit proof.
func TestExecStreamCloseIsNotExitProof(t *testing.T) {
	dk, _ := newExecDaemon(t)
	h, err := dk.ExecStream(context.Background(), "c1", []string{"sleep"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	_ = waitDone(t, h) // a demux error from the closed connection is expected
	if _, ok := h.ExitObserved(); ok {
		t.Fatal("Close produced exit evidence")
	}
}

func TestExecStreamNaturalExitStillObserved(t *testing.T) {
	dk, daemon := newExecDaemon(t)
	h, err := dk.ExecStream(context.Background(), "c1", []string{"true"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	daemon.die(0)
	if err := waitDone(t, h); err != nil {
		t.Fatal(err)
	}
	if code, ok := h.ExitObserved(); !ok || code != 0 {
		t.Fatalf("exit = %d %v", code, ok)
	}
}
