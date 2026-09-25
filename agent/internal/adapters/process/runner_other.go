//go:build !windows

package process

import (
	"context"

	"github.com/canblmz1/stowline/agent/internal/ports"
)

func runOS(ctx context.Context, spec ports.Spec) (*ports.ProcessResult, error) {
	return runUnixTest(ctx, spec)
}
