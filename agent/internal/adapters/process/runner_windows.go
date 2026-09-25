//go:build windows

package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

func runOS(ctx context.Context, spec ports.Spec) (*ports.ProcessResult, error) {
	return runWindows(ctx, spec)
}

func runWindows(ctx context.Context, spec ports.Spec) (*ports.ProcessResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	argv := append([]string{spec.Executable}, spec.Args...)
	cmdline := windows.ComposeCommandLine(argv)

	stdinR, stdinW, err := makePipe(true)
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdoutR, stdoutW, err := makePipe(false)
	if err != nil {
		closeHandles(stdinR, stdinW)
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrR, stderrW, err := makePipe(false)
	if err != nil {
		closeHandles(stdinR, stdinW, stdoutR, stdoutW)
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput = stdinR
	si.StdOutput = stdoutW
	si.StdErr = stderrW

	envSlice, envPtr, err := createEnvBlock(spec.Env)
	if err != nil {
		closeHandles(stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW)
		return nil, err
	}
	_ = envSlice

	exePtr, err := windows.UTF16PtrFromString(spec.Executable)
	if err != nil {
		closeHandles(stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW)
		return nil, err
	}
	cmdPtr, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		closeHandles(stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW)
		return nil, err
	}
	var dirPtr *uint16
	if spec.Dir != "" {
		dirPtr, err = windows.UTF16PtrFromString(spec.Dir)
		if err != nil {
			closeHandles(stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW)
			return nil, err
		}
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		closeHandles(stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW)
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		windows.CloseHandle(job)
		closeHandles(stdinR, stdinW, stdoutR, stdoutW, stderrR, stderrW)
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}

	var pi windows.ProcessInformation
	creation := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_NO_WINDOW)
	err = windows.CreateProcess(
		exePtr,
		cmdPtr,
		nil,
		nil,
		true,
		creation,
		envPtr,
		dirPtr,
		&si,
		&pi,
	)
	runtime.KeepAlive(envSlice)
	closeHandles(stdinR, stdoutW, stderrW)
	if err != nil {
		windows.CloseHandle(job)
		closeHandles(stdinW, stdoutR, stderrR)
		return nil, fmt.Errorf("CreateProcess: %w", err)
	}

	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		windows.CloseHandle(pi.Thread)
		windows.CloseHandle(pi.Process)
		windows.CloseHandle(job)
		closeHandles(stdinW, stdoutR, stderrR)
		return nil, fmt.Errorf("AssignProcessToJobObject: %w", err)
	}

	start := time.Now()
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		windows.CloseHandle(pi.Thread)
		windows.CloseHandle(pi.Process)
		windows.CloseHandle(job)
		closeHandles(stdinW, stdoutR, stderrR)
		return nil, fmt.Errorf("ResumeThread: %w", err)
	}
	windows.CloseHandle(pi.Thread)

	var stdoutBuf, stderrBuf limitedBuffer
	var wg sync.WaitGroup
	wg.Add(2)
	go drainHandle(stdoutR, &stdoutBuf, spec.OnStdout, &wg)
	go drainHandle(stderrR, &stderrBuf, spec.OnStderr, &wg)

	if len(spec.Stdin) > 0 {
		go func() {
			defer windows.CloseHandle(stdinW)
			data := spec.Stdin
			for len(data) > 0 {
				var n uint32
				err := windows.WriteFile(stdinW, data, &n, nil)
				if err != nil || n == 0 {
					return
				}
				data = data[n:]
			}
		}()
	} else {
		windows.CloseHandle(stdinW)
	}

	done := make(chan waitOutcome, 1)
	go func() {
		ev, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		var code uint32
		if err == nil {
			err = windows.GetExitCodeProcess(pi.Process, &code)
		}
		done <- waitOutcome{event: ev, err: err, code: code}
	}()

	grace := spec.GracePeriod
	if grace <= 0 {
		grace = 2 * time.Second
	}

	res := &ports.ProcessResult{
		Pid:          pi.ProcessId,
		ArgvRedacted: domain.RedactArgs(argv),
		EnvNames:     envNames(spec.Env),
	}

	var outcome waitOutcome
	select {
	case outcome = <-done:
	case <-ctx.Done():
		res.Cancelled = errors.Is(ctx.Err(), context.Canceled)
		res.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		// Interactive mode may try Ctrl-Break. A Windows Service has no console,
		// so ForceTerminate skips that and uses Job Object kill after a bounded wait.
		if !spec.ForceTerminate {
			_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, pi.ProcessId)
		}
		select {
		case outcome = <-done:
		case <-time.After(grace):
			_ = windows.TerminateJobObject(job, 1)
			outcome = <-done
		}
	}

	// Descendants can retain inherited pipe writers after the root exits.
	// Closing our sole Job handle kills them before waiting for pipe EOF.
	windows.CloseHandle(job)
	wg.Wait()
	var exit uint32
	_ = windows.GetExitCodeProcess(pi.Process, &exit)
	if outcome.code != 0 {
		exit = outcome.code
	}
	windows.CloseHandle(pi.Process)
	if ctx.Err() != nil {
		res.Cancelled = errors.Is(ctx.Err(), context.Canceled)
		res.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	}

	res.ExitCode = int(exit)
	if res.ExitCode == 259 { // STILL_ACTIVE — treat as crash after terminate
		res.ExitCode = 1
	}
	res.Stdout = stdoutBuf.Bytes()
	res.Stderr = stderrBuf.Bytes()
	res.StdoutTruncated = stdoutBuf.Truncated
	res.StderrTruncated = stderrBuf.Truncated
	res.Duration = time.Since(start)
	if outcome.err != nil {
		return res, fmt.Errorf("process wait failed: %w", outcome.err)
	}
	return res, nil
}

type waitOutcome struct {
	event uint32
	err   error
	code  uint32
}

func makePipe(stdin bool) (read, write windows.Handle, err error) {
	var sa windows.SecurityAttributes
	sa.Length = uint32(unsafe.Sizeof(sa))
	sa.InheritHandle = 1
	if err = windows.CreatePipe(&read, &write, &sa, 0); err != nil {
		return 0, 0, err
	}
	parent := write
	if !stdin {
		parent = read
	}
	if err = windows.SetHandleInformation(parent, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		windows.CloseHandle(read)
		windows.CloseHandle(write)
		return 0, 0, err
	}
	return read, write, nil
}

func closeHandles(hs ...windows.Handle) {
	for _, h := range hs {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
	}
}

func drainHandle(h windows.Handle, buf *limitedBuffer, observe func([]byte), wg *sync.WaitGroup) {
	defer wg.Done()
	defer windows.CloseHandle(h)
	tmp := make([]byte, 32*1024)
	for {
		var n uint32
		err := windows.ReadFile(h, tmp, &n, nil)
		if n > 0 {
			_, _ = buf.Write(tmp[:n])
			if observe != nil {
				observe(append([]byte(nil), tmp[:n]...))
			}
		}
		if err != nil {
			if errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, io.EOF) {
				return
			}
			if err == syscall.ERROR_BROKEN_PIPE {
				return
			}
			return
		}
		if n == 0 {
			return
		}
	}
}

func createEnvBlock(vars []ports.EnvVar) ([]uint16, *uint16, error) {
	lines := envBlock(vars)
	sort.Slice(lines, func(i, j int) bool {
		return strings.ToUpper(lines[i]) < strings.ToUpper(lines[j])
	})
	var u []uint16
	for _, l := range lines {
		p, err := windows.UTF16FromString(l)
		if err != nil {
			return nil, nil, err
		}
		u = append(u, p...)
	}
	u = append(u, 0)
	if len(u) == 1 {
		u = []uint16{0, 0}
	}
	return u, &u[0], nil
}

var _ = os.ErrClosed
