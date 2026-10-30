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
	// UpgradePausedUnavailable: a live task of a preset that is no longer
	// available (an earlier build allowed it) was paused, once.
	UpgradePausedUnavailable UpgradeOutcome = "paused-unavailable"
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
			row, err := s.loadRow(ctx, p.ID)
			if err != nil {
				return err
			}
			task, ts, err := s.lookupTask(ctx, p)
			if err != nil {
				return err
			}
			if reason, off := p.Unavailable(); off {
				// Never rewritten. A live task from an earlier build would keep
				// running a template that cannot work, so it is paused once.
				if ts == taskLive && task.Status == "active" {
					if err = s.st.UpdateTaskStatus(ctx, task.ID, "paused", 0, task.LastRun); err != nil {
						return err
					}
					set, derr := decodeSettings(row, p)
					if derr != nil {
						return derr
					}
					set.Reason = "not available yet (" + reason + ")"
					row.PresetID = p.ID
					row.Settings, row.UpdatedAt = encodeSettings(set), s.now().UnixMilli()
					if err = s.st.PutProactivePreset(ctx, row); err != nil {
						return err
					}
					out = append(out, UpgradeResult{p.ID, UpgradePausedUnavailable})
				}
				continue
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
