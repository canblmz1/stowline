package ports

import (
	"context"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"time"
)

// EnvVar is a process-local environment entry. Secrets may appear here, never in Args.
type EnvVar struct {
	Name   string
	Value  string
	Secret bool
}

// Spec is a direct executable invocation. Args is an argument vector, not a shell string.
type Spec struct {
	Name        string
	Executable  string
	Args        []string
	Env         []EnvVar
	Dir         string
	Stdin       []byte
	Timeout     time.Duration
	GracePeriod time.Duration
	// ForceTerminate skips console Ctrl-Break (a Windows Service has no console)
	// and uses a bounded wait followed by TerminateJobObject. That is forced
	// termination, not a graceful child shutdown.
	ForceTerminate  bool
	ExpectedSHA256  string
	ExpectedVersion string
	Dependencies    []domain.BinaryPin
	// Output observers run inside the agent and must reduce raw child output
	// to non-sensitive structured data. They must never log or transmit chunks.
	OnStdout func([]byte)
	OnStderr func([]byte)
}

// ProcessResult is the structured child outcome. Stringers must not dump secrets.
type ProcessResult struct {
	ExitCode        int
	Signaled        bool
	TimedOut        bool
	Cancelled       bool
	Pid             uint32
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
	Duration        time.Duration
	ArgvRedacted    []string
	EnvNames        []string
}

// Runner is the ONLY component allowed to launch external binaries.
type Runner interface {
	Run(ctx context.Context, spec Spec) (*ProcessResult, error)
}
