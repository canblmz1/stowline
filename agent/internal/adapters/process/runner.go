package process

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

// Runner launches executables with an argument vector. Windows uses Job Objects;
// other platforms are for unit tests only and still never invoke a shell.
type Runner struct {
	LookPath func(string) (string, error)
}

func NewRunner() *Runner { return &Runner{} }

func (r *Runner) Run(ctx context.Context, spec ports.Spec) (*ports.ProcessResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.Executable == "" {
		return nil, domain.ErrBinaryDigestMismatch
	}
	if err := rejectShell(spec); err != nil {
		return nil, err
	}
	if err := rejectSecretArgs(spec, secretValues(spec.Env)); err != nil {
		return nil, err
	}
	if err := verifyExecutable(spec.Executable, spec.ExpectedSHA256); err != nil {
		return nil, err
	}
	for _, pin := range spec.Dependencies {
		if err := verifyExecutable(pin.Path, pin.SHA256); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, err := runOS(ctx, spec)
	if res != nil {
		// Exact known values are removed before process output leaves this boundary.
		for _, secret := range secretValues(spec.Env) {
			res.Stdout = bytes.ReplaceAll(res.Stdout, []byte(secret), []byte("[REDACTED]"))
			res.Stderr = bytes.ReplaceAll(res.Stderr, []byte(secret), []byte("[REDACTED]"))
		}
	}
	return res, err
}

func runUnixTest(ctx context.Context, spec ports.Spec) (*ports.ProcessResult, error) {
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, spec.Executable, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = envBlock(spec.Env)
	if len(spec.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	var stdout, stderr limitedBuffer
	cmd.Stdout = observedWriter{capture: &stdout, observe: spec.OnStdout}
	cmd.Stderr = observedWriter{capture: &stderr, observe: spec.OnStderr}
	start := time.Now()
	err := cmd.Run()
	res := &ports.ProcessResult{
		Stdout:          stdout.Bytes(),
		Stderr:          stderr.Bytes(),
		StdoutTruncated: stdout.Truncated,
		StderrTruncated: stderr.Truncated,
		Duration:        time.Since(start),
		ArgvRedacted:    domain.RedactArgs(append([]string{spec.Executable}, spec.Args...)),
		EnvNames:        envNames(spec.Env),
	}
	if cmd.Process != nil {
		res.Pid = uint32(cmd.Process.Pid)
	}
	if ctx.Err() != nil {
		res.Cancelled = errors.Is(ctx.Err(), context.Canceled)
		res.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	}
	if err == nil {
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, err
}

type limitedBuffer struct {
	buf       bytes.Buffer
	Truncated bool
	mu        sync.Mutex
}

type observedWriter struct {
	capture *limitedBuffer
	observe func([]byte)
}

func (w observedWriter) Write(p []byte) (int, error) {
	n, err := w.capture.Write(p)
	if n > 0 && w.observe != nil {
		// The reader buffer is reused by the Windows runner; hand the observer
		// an owned copy so it can never observe later mutations.
		w.observe(append([]byte(nil), p[:n]...))
	}
	return n, err
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remain := maxCapture - b.buf.Len()
	if remain <= 0 {
		b.Truncated = true
		return len(p), nil
	}
	if len(p) > remain {
		b.buf.Write(p[:remain])
		b.Truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func drain(r io.ReadCloser, w *limitedBuffer, wg *sync.WaitGroup) {
	defer wg.Done()
	defer r.Close()
	_, _ = io.Copy(w, r)
}

func isCanceled(ctx context.Context) bool {
	return ctx.Err() != nil
}

// Ensure os is referenced on non-windows compile of helpers used by tests.
var _ = os.Stderr
var _ = syscall.SIGINT
