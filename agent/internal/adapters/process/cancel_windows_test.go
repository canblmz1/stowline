//go:build windows

package process

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/canblmz1/stowline/agent/internal/ports"
	"github.com/canblmz1/stowline/agent/internal/windows/service"
)

const (
	helperChild  = "child"
	helperParent = "parent"
	helperSleep  = "sleep"
	readyEnv     = "STOWLINE_READY_DIR"
	helperEnv    = "STOWLINE_HELPER"
)

func TestCancelBeforeChildStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sum, err := hashFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRunner().Run(ctx, ports.Spec{
		Executable:     os.Args[0],
		Args:           []string{"-test.run", "TestDoesNotExistZZZ"},
		ExpectedSHA256: sum,
		ForceTerminate: true,
		GracePeriod:    50 * time.Millisecond,
		Dir:            filepath.Dir(os.Args[0]),
	})
	if err == nil {
		t.Fatal("cancelled context must not start a child")
	}
}

func TestForceTerminateWhileChildRuns(t *testing.T) {
	if os.Getenv(helperEnv) == helperSleep {
		writeReadyPID(mustReadyDir())
		parkUntilKilled()
	}
	readyDir := t.TempDir()
	sum, err := hashFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan runDone, 1)
	start := time.Now()
	go func() {
		res, err := NewRunner().Run(ctx, ports.Spec{
			Executable: os.Args[0],
			Args:       []string{"-test.run", "^TestForceTerminateWhileChildRuns$"},
			Env: []ports.EnvVar{
				{Name: helperEnv, Value: helperSleep},
				{Name: readyEnv, Value: readyDir},
			},
			ExpectedSHA256: sum,
			ForceTerminate: true,
			GracePeriod:    150 * time.Millisecond,
			Timeout:        30 * time.Second,
			Dir:            filepath.Dir(os.Args[0]),
		})
		done <- runDone{res: res, err: err}
	}()
	pid := waitReadyPID(t, readyDir, 45*time.Second)
	h := openPID(t, pid)
	defer windows.CloseHandle(h)
	cancel()
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if time.Since(start) > 20*time.Second {
		t.Fatalf("forced termination exceeded shutdown-like bound: %s", time.Since(start))
	}
	if got.res == nil || !got.res.Cancelled {
		t.Fatalf("expected cancelled result: %+v", got.res)
	}
	waitDead(t, h, 5*time.Second)
}

func TestForceTerminateKillsProcessTree(t *testing.T) {
	switch os.Getenv(helperEnv) {
	case helperChild:
		writeReadyPID(mustReadyDir())
		parkUntilKilled()
	case helperParent:
		dir := mustReadyDir()
		cmd := exec.Command(os.Args[0], "-test.run", "^TestForceTerminateKillsProcessTree$")
		cmd.Env = append(os.Environ(), helperEnv+"="+helperChild, readyEnv+"="+dir)
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		// Parent stays alive so the grandchild remains in the Job Object tree.
		parkUntilKilled()
	}

	readyDir := t.TempDir()
	sum, err := hashFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan runDone, 1)
	go func() {
		res, err := NewRunner().Run(ctx, ports.Spec{
			Executable: os.Args[0],
			Args:       []string{"-test.run", "^TestForceTerminateKillsProcessTree$"},
			Env: []ports.EnvVar{
				{Name: helperEnv, Value: helperParent},
				{Name: readyEnv, Value: readyDir},
			},
			ExpectedSHA256: sum,
			ForceTerminate: true,
			GracePeriod:    200 * time.Millisecond,
			Timeout:        45 * time.Second,
			Dir:            filepath.Dir(os.Args[0]),
		})
		done <- runDone{res: res, err: err}
	}()

	pid := waitReadyPID(t, readyDir, 45*time.Second)
	h := openPID(t, pid)
	defer windows.CloseHandle(h)
	cancel()
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.res == nil || !got.res.Cancelled {
		t.Fatalf("expected cancelled result: %+v", got.res)
	}
	waitDead(t, h, 5*time.Second)
}

func TestServiceShutdownBudgetIsBounded(t *testing.T) {
	if service.ShutdownBudget > 60*time.Second || service.ForcedKillAfter >= service.ShutdownBudget {
		t.Fatalf("shutdown=%s kill=%s", service.ShutdownBudget, service.ForcedKillAfter)
	}
}

type runDone struct {
	res *ports.ProcessResult
	err error
}

func mustReadyDir() string {
	dir := os.Getenv(readyEnv)
	if dir == "" {
		os.Exit(3)
	}
	return dir
}

func readyPath(dir string) string {
	return filepath.Join(dir, "grandchild.pid")
}

func writeReadyPID(dir string) {
	pid := strconv.Itoa(os.Getpid())
	tmp := readyPath(dir) + ".tmp"
	if err := os.WriteFile(tmp, []byte(pid), 0600); err != nil {
		os.Exit(4)
	}
	if err := os.Rename(tmp, readyPath(dir)); err != nil {
		os.Exit(5)
	}
}

func waitReadyPID(t *testing.T, dir string, timeout time.Duration) uint32 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	path := readyPath(dir)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			n, conv := strconv.Atoi(string(bytes.TrimSpace(b)))
			if conv == nil && n > 0 {
				return uint32(n)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild did not publish ready pid file %s", path)
	return 0
}

func openPID(t *testing.T, pid uint32) windows.Handle {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		t.Fatalf("OpenProcess pid %d: %v", pid, err)
	}
	return h
}

func waitDead(t *testing.T, h windows.Handle, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var code uint32
		if err := windows.GetExitCodeProcess(h, &code); err != nil {
			return
		}
		if code != 259 { // STILL_ACTIVE
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("process still running after TerminateJobObject")
}

func parkUntilKilled() {
	time.Sleep(60 * time.Second)
	os.Exit(0)
}
