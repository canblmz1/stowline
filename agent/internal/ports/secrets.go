package ports

import (
	"context"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

// SecretHandle is short-lived plaintext. Close must zero.
type SecretHandle interface {
	Bytes() []byte
	Close() error
}

// SecretProvider returns purpose-bound secrets. It must not log values.
type SecretProvider interface {
	Open(ctx context.Context, ref domain.SecretRef) (SecretHandle, error)
}

// Clock is injected so schedule tests can use a fake clock.
type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }
func (RealClock) Since(t time.Time) time.Duration {
	return time.Since(t)
}

type FrozenClock struct {
	T time.Time
}

func (c FrozenClock) Now() time.Time { return c.T }
func (c FrozenClock) Since(t time.Time) time.Duration {
	return c.T.Sub(t)
}
