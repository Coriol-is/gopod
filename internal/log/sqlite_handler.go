// Package log provides a slog.Handler that writes structured log
// entries to gopod's SQLite logs table alongside stderr output.
//
// Design: docs/CONTROL.md §9
package log

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
)

// SecretWords are the D012 words that mark a name as holding a secret.
// Other packages that redact (the Telegram progress lines) build their
// patterns from it, so there is one list.
const SecretWords = `token|key|secret|password|cookie|auth`

// secretPattern matches attribute keys that should be redacted per D012.
var secretPattern = regexp.MustCompile(`(?i)` + SecretWords)

// SQLiteHandler is a slog.Handler that writes to the logs SQLite table.
// It also forwards to a wrapped handler (typically stderr) so logs
// appear in both places.
type SQLiteHandler struct {
	wrapped slog.Handler
	attrs   []slog.Attr
	group   string
	sink    *sqliteSink
}

type logWrite struct {
	record slog.Record
	attrs  []slog.Attr
	group  string
}
type sqliteSink struct {
	db       *sql.DB
	redactor *Redactor
	mu       sync.Mutex
	closed   bool
	queue    chan logWrite
	done     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewSQLiteHandler uses one bounded writer, so a locked database cannot spawn
// unbounded goroutines or stall stderr. Full queues drop the SQLite copy.
func NewSQLiteHandler(db *sql.DB, wrapped slog.Handler) *SQLiteHandler {
	ctx, cancel := context.WithCancel(context.Background())
	sink := &sqliteSink{db: db, redactor: NewRedactor(), queue: make(chan logWrite, 256), done: make(chan struct{}), ctx: ctx, cancel: cancel}
	go sink.run()
	return &SQLiteHandler{wrapped: wrapped, sink: sink}
}

func (h *SQLiteHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.wrapped.Enabled(ctx, level)
}

func (h *SQLiteHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, h.sink.redactor.Text(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool { clean.AddAttrs(h.sink.redactor.ReplaceAttr([]string{h.group}, a)); return true })
	err := h.wrapped.Handle(ctx, clean)
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	if !h.sink.closed {
		select {
		case h.sink.queue <- logWrite{clean.Clone(), h.attrs, h.group}:
		default:
		}
	}
	return err
}

func (h *SQLiteHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = h.sink.redactor.ReplaceAttr([]string{h.group}, a)
	}
	joined := append(append([]slog.Attr(nil), h.attrs...), clean...)
	return &SQLiteHandler{wrapped: h.wrapped.WithAttrs(clean), attrs: joined, group: h.group, sink: h.sink}
}
func (h *SQLiteHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	group := name
	if h.group != "" {
		group = h.group + "." + name
	}
	return &SQLiteHandler{wrapped: h.wrapped.WithGroup(h.sink.redactor.Text(name)), attrs: h.attrs, group: h.sink.redactor.Text(group), sink: h.sink}
}

// Close flushes queued records for up to five seconds, then cancels the writer
// and waits for it to exit. Call before closing the database. All derived
// handlers share this lifecycle; later records go only to stderr.
func (h *SQLiteHandler) Close() {
	h.sink.mu.Lock()
	if !h.sink.closed {
		h.sink.closed = true
		close(h.sink.queue)
	}
	h.sink.mu.Unlock()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-h.sink.done:
	case <-timer.C:
		h.sink.cancel()
		<-h.sink.done
	}
	h.sink.cancel()
}
func (s *sqliteSink) run() {
	defer close(s.done)
	for w := range s.queue {
		if s.ctx.Err() != nil {
			return
		}
		if s.db == nil {
			continue
		}
		s.write(w)
	}
}
func (s *sqliteSink) write(w logWrite) {
	attrs := make(map[string]any)
	subsys := w.group
	for _, a := range w.attrs {
		if a.Key == "subsys" {
			subsys = a.Value.String()
		} else {
			attrs[a.Key] = a.Value.String()
		}
	}
	w.record.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value.String(); return true })
	var attrsJSON *string
	if len(attrs) > 0 {
		b, _ := json.Marshal(attrs)
		v := string(b)
		attrsJSON = &v
	}
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
	defer cancel()
	_, _ = s.db.ExecContext(ctx, `INSERT INTO logs (timestamp, level, subsystem, message, attrs_json) VALUES (?, ?, ?, ?, ?)`, w.record.Time.UnixMilli(), w.record.Level.String(), nullStr(subsys), w.record.Message, attrsJSON)
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// RetentionCleanup deletes log entries older than maxAge.
func RetentionCleanup(db *sql.DB, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().Add(-maxAge).UnixMilli()
	res, err := db.Exec(`DELETE FROM logs WHERE timestamp < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// QueryLogs returns recent log entries, optionally filtered by level
// and subsystem. limit=0 defaults to 50.
func QueryLogs(db *sql.DB, level, subsystem string, limit int) ([]LogEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, timestamp, level, IFNULL(subsystem,''), message, IFNULL(attrs_json,'')
		  FROM logs WHERE 1=1`
	var args []any
	if level != "" {
		query += ` AND level = ?`
		args = append(args, strings.ToUpper(level))
	}
	if subsystem != "" {
		query += ` AND subsystem = ?`
		args = append(args, subsystem)
	}
	query += ` ORDER BY timestamp DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []LogEntry
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Level, &e.Subsystem, &e.Message, &e.AttrsJSON); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// LogEntry is one row from the logs table.
type LogEntry struct {
	ID        int64
	Timestamp int64
	Level     string
	Subsystem string
	Message   string
	AttrsJSON string
}
