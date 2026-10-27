package groupruntime

import (
	"context"
	"strings"

	"github.com/Coriol-is/gopod/internal/runner"
)

type executionProvider struct {
	runner.AgentProvider
	guide string
	env   []string
	check func(context.Context) error
}

func (p *executionProvider) RunCmd(prompt, system string, s runner.SessionRef) []string {
	return p.AgentProvider.RunCmd(prompt, p.guide+"\n\n"+system, s)
}
func (p *executionProvider) RunFreshCmd(prompt string) []string {
	return p.AgentProvider.RunFreshCmd(p.guide + "\n\n" + prompt)
}
func (p *executionProvider) GroupExecEnv() []string                      { return append([]string(nil), p.env...) }
func (p *executionProvider) ValidateExecution(ctx context.Context) error { return p.check(ctx) }

type streamedExecutionProvider struct {
	*executionProvider
	runner.StructuredStreamer
}

func leasedProvider(base runner.AgentProvider, jid string, check func(context.Context) error) runner.AgentProvider {
	policy := base.(interface{ GroupExecEnv() []string })
	p := &executionProvider{AgentProvider: base, guide: strings.ReplaceAll(GroupGuide, "CURRENT_GROUP_JID", jid), env: policy.GroupExecEnv(), check: check}
	if ss, ok := base.(runner.StructuredStreamer); ok {
		return &streamedExecutionProvider{p, ss}
	}
	return p
}
