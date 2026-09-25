package process

import (
	"context"
	"sync"

	"github.com/canblmz1/stowline/agent/internal/ports"
)

// Fake records invocations for unit tests. It never launches a process.
type Fake struct {
	mu     sync.Mutex
	Calls  []ports.Spec
	Result *ports.ProcessResult
	Err    error
	Hook   func(spec ports.Spec) (*ports.ProcessResult, error)
	// HookCtx, when set, wins over Hook and also sees the run's context
	// (for tests of cancellation, e.g. a stalled child).
	HookCtx func(ctx context.Context, spec ports.Spec) (*ports.ProcessResult, error)
}

func (f *Fake) Run(ctx context.Context, spec ports.Spec) (*ports.ProcessResult, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, spec)
	f.mu.Unlock()
	if f.HookCtx != nil {
		return f.HookCtx(ctx, spec)
	}
	if f.Hook != nil {
		return f.Hook(spec)
	}
	if f.Err != nil {
		return f.Result, f.Err
	}
	if f.Result != nil {
		return f.Result, nil
	}
	return &ports.ProcessResult{ExitCode: 0}, nil
}

func (f *Fake) Last() ports.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Calls) == 0 {
		return ports.Spec{}
	}
	return f.Calls[len(f.Calls)-1]
}
