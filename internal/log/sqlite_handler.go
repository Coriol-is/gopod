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
	"time"
)

// secretPattern matches attribute keys that should be redacted per D012.
var secretPattern = regexp.MustCompile(`(?i)token|key|secret|password|cookie|auth`)

// SQLiteHandler is a slog.Handler that writes to the logs SQLite table.
// It also forwards to a wrapped handler (typically stderr) so logs
// appear in both places.
type SQLiteHandler struct {
	db      *sql.DB
	wrapped slog.Handler
	attrs   []slog.Attr
	group   string
}

// NewSQLiteHandler creates a handler that writes to both the SQLite
// logs table and the wrapped handler (for stderr).
func NewSQLiteHandler(db *sql.DB, wrapped slog.Handler) *SQLiteHandler {
	return &SQLiteHandler{db: db, wrapped: wrapped}
}

func (h *SQLiteHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.wrapped.Enabled(ctx, level)
}

func (h *SQLiteHandler) Handle(ctx context.Context, r slog.Record) error {
	// Forward to wrapped handler (stderr) first.
	if err := h.wrapped.Handle(ctx, r); err != nil {
		return err
	}

	// Write to SQLite. Best-effort: never fail the log call.
	go h.writeToSQLite(r)
	return nil
}

func (h *SQLiteHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &SQLiteHandler{
		db:      h.db,
		wrapped: h.wrapped.WithAttrs(attrs),
		attrs:   append(h.attrs, attrs...),
		group:   h.group,
	}
}

func (h *SQLiteHandler) WithGroup(name string) slog.Handler {
	return &SQLiteHandler{
		db:      h.db,
		wrapped: h.wrapped.WithGroup(name),
		attrs:   h.attrs,
		group:   name,
	}
}

func (h *SQLiteHandler) writeToSQLite(r slog.Record) {
	if h.db == nil {
		return
	}

	ts := r.Time.UnixMilli()
	level := r.Level.String()
	msg := r.Message

	// Extract subsystem from pre-set attrs.
	subsys := h.group
	for _, a := range h.attrs {
		if a.Key == "subsys" {
			subsys = a.Value.String()
		}
	}

	// Collect all attributes into a JSON map, redacting secrets.
	attrs := make(map[string]any)
	for _, a := range h.attrs {
		if a.Key != "subsys" {
			attrs[redactKey(a.Key)] = redactValue(a.Key, a.Value.String())
		}
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[redactKey(a.Key)] = redactValue(a.Key, a.Value.String())
		return true
	})

	var attrsJSON *string
	if len(attrs) > 0 {
		b, _ := json.Marshal(attrs)
		s := string(b)
		attrsJSON = &s
	}

	h.db.Exec(`INSERT INTO logs (timestamp, level, subsystem, message, attrs_json) VALUES (?, ?, ?, ?, ?)`,
		ts, level, nullStr(subsys), msg, attrsJSON)
}

func redactKey(key string) string {
	return key
}

func redactValue(key, value string) string {
	if secretPattern.MatchString(key) {
		return "<redacted>"
	}
	return value
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
