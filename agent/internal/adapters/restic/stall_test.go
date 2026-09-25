package restic

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/canblmz1/stowline/agent/internal/adapters/process"
	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
	"github.com/canblmz1/stowline/agent/internal/adapters/storage"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

// stallFake plays a restic backup that prints status lines every 5ms; the
// numbers move for the first `moving` lines and then freeze, like restic
// waiting on a dead connection. It exits only when its context is cancelled
// or after `lines` lines.
func stallFake(moving, lines int) *process.Fake {
	return &process.Fake{HookCtx: func(ctx context.Context, spec ports.Spec) (*ports.ProcessResult, error) {
		args := strings.Join(spec.Args, " ")
		if !strings.Contains(args, "backup") {
			return &ports.ProcessResult{ExitCode: 0}, nil
		}
		for i := 0; i < lines; i++ {
			n := i
			if n > moving {
				n = moving
			}
			if spec.OnStdout != nil {
				spec.OnStdout([]byte(fmt.Sprintf("[0:%02d] %d.00%%  %d files %d MiB, total 100 files 100 MiB\n", i%60, n, n, n)))
			}
			select {
			case <-ctx.Done():
				return &ports.ProcessResult{ExitCode: 1, Cancelled: true}, nil
			case <-time.After(5 * time.Millisecond):
			}
		}
		return &ports.ProcessResult{ExitCode: 0}, nil
	}}
}

func stallAdapter(f *process.Fake, stall time.Duration) *Adapter {
	return &Adapter{
		Runner:       f,
		Secrets:      secrets.StaticProvider{Values: map[string]string{"pw": "x"}},
		Connector:    storage.Connector{},
		Pin:          domain.BinaryPin{Path: `C:\Stowline\bin\restic.exe`, SHA256: "abc"},
		StallTimeout: stall,
	}
}

func stallRequest() domain.BackupRequest {
	return domain.BackupRequest{
		DeviceID:    "dev",
		Repository:  storage.LocalDescriptor(`C:\Stowline\Repos\local`, "g1", "dev"),
		SourceRoots: []string{`C:\Stowline\TestCorpus`},
		VSSMode:     domain.VSSDisabled,
		PasswordRef: domain.SecretRef{Locator: "pw"},
		Host:        "dev",
	}
}

func TestBackupWithFrozenProgressIsStoppedAsANetworkFailure(t *testing.T) {
	a := stallAdapter(stallFake(10, 100000), 200*time.Millisecond)
	start := time.Now()
	out, err := a.Backup(context.Background(), stallRequest())
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != domain.PhaseFailed || out.ErrorClass != domain.ErrorNetwork {
		t.Fatalf("want FAILED/NETWORK, got %s/%s (%s)", out.Outcome, out.ErrorClass, out.ErrorMessage)
	}
	if !strings.Contains(out.ErrorMessage, "no backup progress") {
		t.Fatalf("message: %q", out.ErrorMessage)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("watchdog too slow: %s", time.Since(start))
	}
}

func TestBackupThatKeepsProgressingIsNotStopped(t *testing.T) {
	// 150 moving lines at 5ms = 750ms, far longer than the 200ms limit.
	a := stallAdapter(stallFake(150, 150), 200*time.Millisecond)
	out, _ := a.Backup(context.Background(), stallRequest())
	if out.ErrorClass == domain.ErrorNetwork {
		t.Fatalf("a progressing backup was stopped: %s", out.ErrorMessage)
	}
}
