package ipc

import "errors"

// ErrPermanent is the sentinel sinks wrap into their dispatch errors
// to tell the watcher "do not retry this file." Use it like:
//
//	return fmt.Errorf("validate cron %q: %w: %w", expr, err, ipc.ErrPermanent)
//
// The watcher checks via errors.Is.
var ErrPermanent = errors.New("ipc: permanent dispatch error")
