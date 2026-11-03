package main

import (
	"context"
	"github.com/Coriol-is/gopod/internal/runner"
)

// elevatedRestart joins host sessions into the existing restart/drain contract.
type elevatedRestart struct {
	ordinary restartRuntime
	host     *runner.Elevated
}

func (r elevatedRestart) BeginDrain()   { r.host.BeginDrain(); r.ordinary.BeginDrain() }
func (r elevatedRestart) CancelDrain()  { r.host.CancelDrain(); r.ordinary.CancelDrain() }
func (r elevatedRestart) CancelActive() { r.host.CancelActive(); r.ordinary.CancelActive() }
func (r elevatedRestart) WaitDrained(ctx context.Context) error {
	if err := r.host.WaitDrained(ctx); err != nil {
		return err
	}
	return r.ordinary.WaitDrained(ctx)
}
func (r elevatedRestart) ForceDrain(ctx context.Context) error {
	if err := r.host.ForceDrain(ctx); err != nil {
		return err
	}
	return r.ordinary.ForceDrain(ctx)
}
