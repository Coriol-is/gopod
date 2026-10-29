package control

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/Coriol-is/gopod/internal/backup"
	"github.com/Coriol-is/gopod/internal/maintenance"
	"github.com/Coriol-is/gopod/internal/store"
)

// BackupDeps wires the operator backup commands (CP-007). They are CLI-only:
// registered with an empty slash name and PermOwnerOnly, so no Telegram message
// can reach them, and every one works on files without Store.Open or sqlite3.
type BackupDeps struct {
	DataDir      string
	StorePath    string
	ArtifactsDir string
	Forbidden    []string // extra trees a backup root must not overlap
	DefaultRoot  string   // used when --root is not given; may be empty
	Version      string
	// Containers builds the live-container inventory lazily (it may open the
	// Docker client) and returns a closer. Nil means no inventory is available.
	Containers func(ctx context.Context) (maintenance.ContainerLister, func(), error)
	Log        *slog.Logger
	Now        func() time.Time
}

// RegisterBackup registers backup.create, backup.verify, backup.list,
// backup.idle and backup.quarantine.
func RegisterBackup(r *Router, d BackupDeps) {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	r.Register("backup.create", "", "(owner, CLI) create a backup set", PermOwnerOnly, d.create)
	r.Register("backup.verify", "", "(owner, CLI) verify a backup set offline", PermOwnerOnly, d.verify)
	r.Register("backup.list", "", "(owner, CLI) list backup sets", PermOwnerOnly, d.list)
	r.Register("backup.idle", "", "(owner, CLI) report unsettled durable work", PermOwnerOnly, d.idle)
	r.Register("backup.quarantine", "", "(owner, CLI) show, set or release the restore quarantine latch", PermOwnerOnly, d.quarantine)
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func jsonData(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return map[string]any{"result": m}
}

func usage(text string) (Response, error) { return Response{Text: text, Code: 1}, nil }

func (d BackupDeps) root(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	return d.DefaultRoot
}

func (d BackupDeps) create(ctx context.Context, cmd Command) (Response, error) {
	fs := newFlags("backup create")
	root := fs.String("root", "", "backup root directory (absolute, outside data dir)")
	mode := fs.String("mode", backup.ModeDatabaseOnly, "database_only or matched_stopped")
	assert := fs.Bool("assert-no-containers", false, "declare that no agent containers exist (recorded as operator-asserted)")
	maxBytes := fs.Int64("max-bytes", 0, "cap for total bytes under the root (0 = free space only)")
	timeout := fs.Duration("timeout", 5*time.Minute, "overall capture deadline")
	var cfgs listFlag
	fs.Var(&cfgs, "config", "declared config file to copy (matched_stopped; repeatable)")
	if err := fs.Parse(cmd.Args); err != nil || fs.NArg() != 0 {
		return usage("usage: backup create --root DIR [--mode database_only|matched_stopped] [--config FILE]... [--assert-no-containers] [--max-bytes N] [--timeout 5m]")
	}
	rootDir := d.root(*root)
	if rootDir == "" {
		return usage("backup create: --root is required")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	o := backup.Options{
		Root: rootDir, Mode: *mode, StorePath: d.StorePath, ArtifactsDir: d.ArtifactsDir,
		ConfigFiles: cfgs, Forbidden: append([]string{d.DataDir}, d.Forbidden...),
		Version: d.Version, MaxBytes: *maxBytes, Log: d.Log, Now: d.Now,
	}
	if *mode == backup.ModeMatchedStopped {
		s := &maintenance.Settlement{LockPath: maintenance.ControllerLockPath(d.DataDir), AssertNoContainers: *assert}
		if !*assert && d.Containers != nil {
			lister, closeFn, err := d.Containers(ctx)
			if err != nil {
				return Response{Text: fmt.Sprintf("backup refused: container inventory unavailable (%v). Nothing was stopped. Pass --assert-no-containers only if no agent containers can exist.", err), Code: 1}, nil
			}
			defer closeFn()
			s.Containers = lister
		}
		o.Settlement = s
	}
	res, err := backup.Create(ctx, o)
	if err != nil {
		var ue *maintenance.UnsettledError
		if errors.As(err, &ue) {
			return Response{Text: "backup refused, nothing was stopped or changed: " + strings.Join(ue.Reasons, "; "), Code: 3}, nil
		}
		return Response{Text: "backup failed, nothing was published: " + err.Error(), Code: 1}, nil
	}
	m := res.Manifest
	var sb strings.Builder
	fmt.Fprintf(&sb, "backup set %s (%s)\npath: %s\ndatabase: %d bytes, %d tables, integrity ok\nfiles: %d\n",
		res.SetID, m.Mode, res.Path, m.Database.Size, len(m.Database.Tables), len(m.Files)+1)
	if m.Work.Unsettled() {
		sb.WriteString("note: durable work was unsettled at capture time; a restore must hold it as quarantined evidence, never replay it\n")
	}
	fmt.Fprintf(&sb, "covers: %s\nnot covered: %s\nnext: gopod backup verify %s", strings.Join(m.Coverage, ", "), strings.Join(m.NotCovered, ", "), res.SetID)
	return Response{Text: sb.String(), Data: jsonData(m)}, nil
}

func (d BackupDeps) verify(ctx context.Context, cmd Command) (Response, error) {
	fs := newFlags("backup verify")
	root := fs.String("root", "", "backup root directory (when the set is given by id)")
	if err := fs.Parse(cmd.Args); err != nil || fs.NArg() != 1 {
		return usage("usage: backup verify [--root DIR] <set-id|set-directory>")
	}
	target := fs.Arg(0)
	if !strings.ContainsRune(target, filepath.Separator) {
		rootDir := d.root(*root)
		if rootDir == "" {
			return usage("backup verify: a bare set id needs --root")
		}
		target = filepath.Join(rootDir, target)
	}
	rep, err := backup.Verify(ctx, target)
	var sb strings.Builder
	if err != nil {
		fmt.Fprintf(&sb, "set %s: NOT restorable\n", rep.SetID)
		for _, p := range rep.Problems {
			fmt.Fprintf(&sb, "  problem: %s\n", p)
		}
		if len(rep.Problems) == 0 {
			fmt.Fprintf(&sb, "  problem: %v\n", err)
		}
		return Response{Text: strings.TrimRight(sb.String(), "\n"), Data: jsonData(rep), Code: 1}, nil
	}
	fmt.Fprintf(&sb, "set %s (%s): OK\nfiles checked: %d, tables: %d, rows: %d\ncovers: %s\nnot covered: %s",
		rep.SetID, rep.Mode, rep.Files, rep.Tables, rep.Rows, strings.Join(rep.Coverage, ", "), strings.Join(rep.NotCovered, ", "))
	for _, w := range rep.Warnings {
		fmt.Fprintf(&sb, "\nwarning: %s", w)
	}
	if rep.Unsettled {
		sb.WriteString("\nwarning: the set records unsettled durable work; hold it as quarantined evidence after any restore")
	}
	sb.WriteString("\nVerification used a scratch copy; the set and the live store were not touched. It does not activate anything.")
	return Response{Text: sb.String(), Data: jsonData(rep)}, nil
}

func (d BackupDeps) list(_ context.Context, cmd Command) (Response, error) {
	fs := newFlags("backup list")
	root := fs.String("root", "", "backup root directory")
	if err := fs.Parse(cmd.Args); err != nil || fs.NArg() != 0 {
		return usage("usage: backup list [--root DIR]")
	}
	rootDir := d.root(*root)
	if rootDir == "" {
		return usage("backup list: --root is required")
	}
	sets, err := backup.List(rootDir)
	if err != nil {
		return Response{Text: "backup list: " + err.Error(), Code: 1}, nil
	}
	var sb strings.Builder
	for _, s := range sets {
		state := "sealed"
		if !s.Sealed {
			state = "UNSEALED: " + s.Problem
		}
		fmt.Fprintf(&sb, "%s  %s  %s  db=%dB  %s\n", s.SetID, s.Mode, s.CompletedAt, s.DBBytes, state)
	}
	if len(sets) == 0 {
		sb.WriteString("no backup sets\n")
	}
	sb.WriteString("listing reads manifests only; run backup verify to prove a set is intact")
	return Response{Text: sb.String(), Data: jsonData(map[string]any{"sets": sets})}, nil
}

// idle reports unsettled durable work read-only (safe while the controller
// runs). Exit code 1 means a matched capture should be deferred.
func (d BackupDeps) idle(ctx context.Context, cmd Command) (Response, error) {
	if len(cmd.Args) != 0 {
		return usage("usage: backup idle")
	}
	ev, err := store.ReadWorkEvidence(ctx, d.StorePath)
	if err != nil {
		return Response{Text: "backup idle: " + err.Error(), Code: 1}, nil
	}
	text := fmt.Sprintf("turns pending=%d running=%d; outbox parts in flight=%d; unresolved deliveries=%d; artifact reservations=%d; task runs executing=%d; task attempts held=%d",
		ev.TurnsPending, ev.TurnsRunning, ev.OutboxPartsInFlight, ev.DeliveriesUnresolved, ev.ArtifactReservations, ev.TaskRunsExecuting, ev.TaskAttemptsHeld)
	code := 0
	if ev.Unsettled() {
		text = "BUSY (defer a matched capture): " + text
		code = 1
	} else {
		text = "idle: " + text
	}
	return Response{Text: text, Data: jsonData(ev), Code: code}, nil
}

func (d BackupDeps) quarantine(_ context.Context, cmd Command) (Response, error) {
	if len(cmd.Args) != 1 || (cmd.Args[0] != "status" && cmd.Args[0] != "set" && cmd.Args[0] != "release") {
		return usage("usage: backup quarantine status|set|release [--yes]")
	}
	switch cmd.Args[0] {
	case "status":
		active, info := maintenance.LatchActive(d.DataDir)
		if !active {
			return Response{Text: "restore quarantine: not set"}, nil
		}
		return Response{Text: fmt.Sprintf("restore quarantine: SET (%s) since %s; normal startup is refused", info.Reason, info.At), Data: jsonData(info)}, nil
	case "set":
		if err := maintenance.SetLatch(d.DataDir, "", "operator set", d.Now()); err != nil {
			return Response{Text: "quarantine set: " + err.Error(), Code: 1}, nil
		}
		return Response{Text: "restore quarantine set: the next start will run quarantined (control plane only)"}, nil
	default:
		if !cmd.Confirmed {
			return Response{Text: "release the restore quarantine? Only after reconciliation: effects since the capture were checked against reality and no other instance polls this bot. Re-run with --yes.", Code: 1}, nil
		}
		if err := maintenance.ReleaseLatch(d.DataDir); err != nil {
			return Response{Text: "quarantine release: " + err.Error(), Code: 1}, nil
		}
		return Response{Text: "restore quarantine released: the next start is a normal start"}, nil
	}
}
