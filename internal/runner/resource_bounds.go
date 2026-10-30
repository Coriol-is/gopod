package runner

import (
	"context"
	"sync"
	"time"
)

const maxAgentOutputBytes = 4 << 20
const groupTurnTimeout = 15 * time.Minute
const outputValidationInterval = 3 * time.Second

// boundedBuffer retains a diagnostic tail while continuing to drain the pipe.
// Returning short writes would strand the exec and its stderr demultiplexer.
type boundedBuffer struct{ data []byte }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if n >= maxAgentOutputBytes {
		b.data = append(b.data[:0], p[n-maxAgentOutputBytes:]...)
		return n, nil
	}
	if overflow := len(b.data) + n - maxAgentOutputBytes; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *boundedBuffer) WriteString(s string) (int, error) { return b.Write([]byte(s)) }
func (b *boundedBuffer) String() string                    { return string(b.data) }
func (b *boundedBuffer) Reset()                            { b.data = b.data[:0] }

func groupTurnContext(ctx context.Context, group bool) (context.Context, context.CancelFunc) {
	if group {
		return context.WithTimeout(ctx, groupTurnTimeout)
	}
	return ctx, func() {}
}

// Output checks are scoped to one running turn. A denial latches; start and
// completion checks are uncached. Do not cache admission across separate turns.
func outputValidator(ctx context.Context, prov AgentProvider, now func() time.Time) func() error {
	var mu sync.Mutex
	var checked time.Time
	var denied error
	return func() error {
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		if denied != nil {
			return denied
		}
		current := now()
		if checked.IsZero() || current.Sub(checked) >= outputValidationInterval {
			denied = validateExecution(ctx, prov)
			checked = current
		}
		return denied
	}
}
