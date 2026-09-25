package process

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

func TestRejectShellAndSecretArgs(t *testing.T) {
	r := NewRunner()
	_, err := r.Run(context.Background(), ports.Spec{
		Executable:     `C:\Windows\System32\cmd.exe`,
		Args:           []string{"/c", "echo hi"},
		ExpectedSHA256: "00",
	})
	if err == nil {
		t.Fatal("cmd.exe must be rejected")
	}
	_, err = r.Run(context.Background(), ports.Spec{
		Executable:     os.Args[0],
		Args:           []string{"--password", domain.CanarySecret},
		ExpectedSHA256: "00",
	})
	if err == nil {
		t.Fatal("secret args must be rejected before launch")
	}
}

func TestRestrictedEnvOmitsPasswordCommand(t *testing.T) {
	_, err := RestrictedEnv([]ports.EnvVar{{Name: "RESTIC_PASSWORD_COMMAND", Value: "whoami"}}, "", "", "", nil)
	if err == nil {
		t.Fatal("password command must be rejected")
	}
	env, err := RestrictedEnv([]ports.EnvVar{{Name: "RESTIC_PASSWORD", Value: domain.CanarySecret, Secret: true}}, `C:\cache`, `C:\tmp`, `C:\Windows`, []string{`C:\Stowline\bin`})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range env {
		names[e.Name] = true
		if e.Name == "ComSpec" {
			t.Fatal("ComSpec must not be set")
		}
	}
	if !names["RESTIC_PASSWORD"] || !names["RESTIC_CACHE_DIR"] {
		t.Fatalf("%v", names)
	}
}

func TestLaunchSelfHelper(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip()
	}
	exe := os.Args[0]
	sum, err := hashFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := r.Run(ctx, ports.Spec{
		Executable:     exe,
		Args:           []string{"-test.run", "TestDoesNotExistZZZ"},
		ExpectedSHA256: sum,
		Timeout:        10 * time.Second,
		Dir:            filepath.Dir(exe),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
	for _, a := range res.ArgvRedacted {
		if a == domain.CanarySecret {
			t.Fatal("canary in argv")
		}
	}
}
