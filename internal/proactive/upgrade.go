package proactive

import (
	"context"
	"fmt"
)

// UpgradeOutcome is the result of a template upgrade for one preset.
type UpgradeOutcome string

const (
	// UpgradeApplied: the untouched task was rewritten from the new template.
	UpgradeApplied UpgradeOutcome = "applied"
	// UpgradeNeedsApply: the owner edited the task; it is left alone and the
	// preset screen offers "Apply" (ResetToTemplate) after a confirmation.
	UpgradeNeedsApply UpgradeOutcome = "needs-apply"
	// UpgradeBlocked: the new template cannot be rendered now (quiet hours).
	UpgradeBlocked UpgradeOutcome = "blocked"
)

// UpgradeResult names one preset touched by UpgradeTemplates.
type UpgradeResult struct {
	PresetID string
	Outcome  UpgradeOutcome
}

// UpgradeTemplates brings every live preset task whose stored template version
// is behind the catalog up to date, unless the owner edited it (section 6.6).
// Run at startup and when a preset screen opens.
func (s *Service) UpgradeTemplates(ctx context.Context) ([]UpgradeResult, error) {
	var out []UpgradeResult
	err := s.inLockedTx(ctx, func(ctx context.Context) error {
		out = out[:0]
		env, err := s.env(ctx)
		if err != nil {
			return err
		}
		for _, p := range Catalog() {
			if _, off := p.Unavailable(); off {
				continue // left exactly as it is until the preset becomes available
			}
			row, err := s.loadRow(ctx, p.ID)
			if err != nil {
				return err
			}
			task, ts, err := s.lookupTask(ctx, p)
			if err != nil {
				return err
			}
			if ts != taskLive || row.TemplateVersion >= p.TemplateVersion {
				continue
			}
			if modified(row, task) {
				out = append(out, UpgradeResult{p.ID, UpgradeNeedsApply})
				continue
			}
			set, err := decodeSettings(row, p)
			if err != nil {
				return err
			}
			prompt, sched, err := s.renderTask(p, set.Settings, env)
			if blocked(err) {
				out = append(out, UpgradeResult{p.ID, UpgradeBlocked})
				continue
			}
			if err != nil {
				return fmt.Errorf("upgrading %s: %w", p.ID, err)
			}
			if err = s.rewrite(ctx, &task, prompt, sched); err != nil {
				return err
			}
			if err = s.putRow(ctx, row, set, p.TemplateVersion, ContentHash(prompt, "cron", sched)); err != nil {
				return err
			}
			out = append(out, UpgradeResult{p.ID, UpgradeApplied})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return append([]UpgradeResult(nil), out...), nil
}
