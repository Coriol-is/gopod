// Package taskreport implements the strict task-results v1 side channel
// (CP-021, plan PR 3): a host-issued, attempt-bound capability and a bounded
// parser that turns an agent's report into a provisional, model-reported record.
//
// A report is the model's claim. Nothing here marks execution complete, host
// verified, delivered or accepted, fetches an evidence path, or runs anything:
// evidence strings are stored as untrusted text.
package taskreport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Bounds from the CP-021 implementation contract.
const (
	MaxEnvelopeBytes   = 32 << 10
	MaxListItems       = 8
	MaxItemBytes       = 1024
	MaxCheckpointBytes = 2048
	MaxPlanVersion     = 1_000_000
	maxTokenBytes      = 128
	maxIDBytes         = 64
)

// Code is a sanitized rejection reason. It never carries content.
type Code string

const (
	CodeInvalid      Code = "invalid_report"
	CodeTooLarge     Code = "report_too_large"
	CodeStale        Code = "stale_capability"
	CodeIdentity     Code = "identity_mismatch"
	CodeScopeChanged Code = "scope_changed"
)

// Error is a rejected report.
type Error struct{ Code Code }

func (e *Error) Error() string { return "taskreport: " + string(e.Code) }

func reject(c Code) error { return &Error{Code: c} }

// CodeOf extracts the rejection code of err.
func CodeOf(err error) (Code, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, true
	}
	return "", false
}

// Outcome statuses an agent may claim.
var statuses = map[string]bool{"achieved": true, "partial": true, "blocked": true, "unknown": true}

// Outcome is the model's claim about the goal.
type Outcome struct {
	Status      string   `json:"status"`
	Checks      []string `json:"checks"`
	Evidence    []string `json:"evidence"`
	Limitations []string `json:"limitations"`
}

// Checkpoint is where a continuation could safely resume. Text only.
type Checkpoint struct {
	PlanVersion            int    `json:"planVersion"`
	LastSupportedMilestone string `json:"lastSupportedMilestone"`
	NextSafeStep           string `json:"nextSafeStep"`
	UnresolvedEffects      string `json:"unresolvedEffects"`
}

// Envelope is one report file.
type Envelope struct {
	Version    int         `json:"version"`
	RunID      string      `json:"runId"`
	AttemptID  string      `json:"attemptId"`
	RunToken   string      `json:"runToken"`
	Outcome    *Outcome    `json:"outcome"`
	Checkpoint *Checkpoint `json:"checkpoint"`
}

// Parse decodes and validates one envelope. Unknown fields, trailing data,
// wrong types, oversize or malformed text reject the whole report.
func Parse(raw []byte) (Envelope, error) {
	var env Envelope
	if len(raw) > MaxEnvelopeBytes {
		return env, reject(CodeTooLarge)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return env, reject(CodeInvalid)
	}
	if _, err := dec.Token(); err != io.EOF {
		return env, reject(CodeInvalid) // trailing data
	}
	if env.Version != 1 || !validID(env.RunID) || !validID(env.AttemptID) ||
		env.RunToken == "" || len(env.RunToken) > maxTokenBytes || !cleanText(env.RunToken) {
		return env, reject(CodeInvalid)
	}
	if o := env.Outcome; o != nil {
		if !statuses[o.Status] {
			return env, reject(CodeInvalid)
		}
		for _, list := range []*[]string{&o.Checks, &o.Evidence, &o.Limitations} {
			if len(*list) > MaxListItems {
				return env, reject(CodeInvalid)
			}
			for _, s := range *list {
				if len(s) > MaxItemBytes || !cleanText(s) {
					return env, reject(CodeInvalid)
				}
			}
			if *list == nil {
				*list = []string{}
			}
		}
	}
	if c := env.Checkpoint; c != nil {
		if c.PlanVersion < 1 || c.PlanVersion > MaxPlanVersion {
			return env, reject(CodeInvalid)
		}
		for _, s := range []string{c.LastSupportedMilestone, c.NextSafeStep, c.UnresolvedEffects} {
			if len(s) > MaxCheckpointBytes || !cleanText(s) {
				return env, reject(CodeInvalid)
			}
		}
	}
	return env, nil
}

func validID(s string) bool {
	if s == "" || len(s) > maxIDBytes {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// cleanText: valid UTF-8 without NUL.
func cleanText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// Provisional is the host's canonical, model-reported record of a report. It is
// re-marshalled from the parsed fields, never copied from the raw bytes.
type Provisional struct {
	RunID          string
	AttemptID      string
	Status         string // empty when the report has no outcome
	OutcomeJSON    string
	CheckpointJSON string
	PlanVersion    int
}

// Canonical builds the provisional record of a validated envelope.
func Canonical(env Envelope) (Provisional, error) {
	p := Provisional{RunID: env.RunID, AttemptID: env.AttemptID}
	if env.Outcome != nil {
		b, err := json.Marshal(env.Outcome)
		if err != nil {
			return p, fmt.Errorf("taskreport: outcome: %w", err)
		}
		p.Status, p.OutcomeJSON = env.Outcome.Status, string(b)
	}
	if env.Checkpoint != nil {
		b, err := json.Marshal(env.Checkpoint)
		if err != nil {
			return p, fmt.Errorf("taskreport: checkpoint: %w", err)
		}
		p.CheckpointJSON, p.PlanVersion = string(b), env.Checkpoint.PlanVersion
	}
	if p.OutcomeJSON == "" && p.CheckpointJSON == "" {
		return p, reject(CodeInvalid) // nothing to record
	}
	return p, nil
}
