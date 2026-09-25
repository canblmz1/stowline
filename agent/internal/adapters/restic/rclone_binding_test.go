package restic

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode"

	"github.com/canblmz1/stowline/agent/internal/adapters/process"
	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
	"github.com/canblmz1/stowline/agent/internal/adapters/storage"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

// Regression: the storage connector's rclone.program value must survive the
// restic argument builder. A mismatch here silently disables every rclone
// backend (PILOT_RCLONE_LOCAL / PILOT_RCLONE_DRIVE) at runtime while unit tests
// on each side keep passing.
func TestRcloneConnectorOutputSurvivesBuildArgs(t *testing.T) {
	pinPath := `C:\Stowline\bin\rclone.exe`
	pin := domain.BinaryPin{Path: pinPath, SHA256: strings.Repeat("a", 64), Version: "1.75.0"}
	bound, err := storage.Connector{RclonePin: pin}.Bind(
		context.Background(),
		storage.RcloneLocalDescriptor("rclone:stowlinelocal:Repos/x", "", "g", "dev"),
	)
	if err != nil {
		t.Fatalf("connector.Bind(%q): %v", pinPath, err)
	}
	args, err := buildArgs(cmdBackup, bound, "--tag", "stowline", "--", `C:\corpus`)
	if err != nil {
		t.Fatalf("buildArgs rejected connector output: %v (options=%+v)", err, bound.Options)
	}
	var progVal string
	for i, a := range args {
		if a == "-o" && i+1 < len(args) && strings.HasPrefix(args[i+1], "rclone.program=") {
			progVal = strings.TrimPrefix(args[i+1], "rclone.program=")
		}
	}
	if progVal != `C:/Stowline/bin/rclone.exe` {
		t.Fatalf("rclone.program = %q, want the unquoted forward-slash pinned path", progVal)
	}
	// restic 0.19.1: -o is CSV-parsed (no ") then SplitShellStrings-split (on \
	// and whitespace). A slash path with no spaces yields exactly one arg.
	if got := splitShellLike(progVal); len(got) != 1 || got[0] != progVal {
		t.Fatalf("rclone.program %q does not survive as one literal arg: %v", progVal, got)
	}

	// A pinned path with a space cannot be expressed safely and must fail closed.
	_, err = storage.Connector{RclonePin: domain.BinaryPin{Path: `C:\Program Files\Stowline\rclone.exe`, SHA256: strings.Repeat("a", 64)}}.Bind(
		context.Background(), storage.RcloneLocalDescriptor("rclone:stowlinelocal:Repos/x", "", "g", "dev"))
	if err == nil {
		t.Fatal("rclone pin path with a space must be rejected by the connector")
	}
}

// TestRcloneEngineBackupReachesRunner proves the full engine path binds an
// rclone descriptor without rejecting its own generated option.
func TestRcloneEngineBackupReachesRunner(t *testing.T) {
	pin := domain.BinaryPin{Path: `C:\Stowline\bin\rclone.exe`, SHA256: strings.Repeat("a", 64), Version: "1.75.0"}
	fake := &process.Fake{Result: &ports.ProcessResult{
		ExitCode: 0,
		Stdout:   []byte(`{"message_type":"summary","snapshot_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","files_new":1}`),
	}}
	a := &Adapter{
		Runner:    fake,
		Secrets:   secrets.StaticProvider{Values: map[string]string{"pw": "x"}},
		Connector: storage.Connector{RclonePin: pin},
		Pin:       domain.BinaryPin{Path: `C:\Stowline\bin\restic.exe`, SHA256: "abc", Version: "0.19.1"},
	}
	_, err := a.Backup(context.Background(), domain.BackupRequest{
		DeviceID:    "dev",
		Repository:  storage.RcloneLocalDescriptor("rclone:stowlinelocal:Repos/x", "", "g", "dev"),
		SourceRoots: []string{`C:\corpus`},
		VSSMode:     domain.VSSDisabled,
		PasswordRef: domain.SecretRef{Locator: "pw"},
	})
	if errors.Is(err, domain.ErrArbitraryFlags) {
		t.Fatalf("engine rejected its own rclone binding: %v", err)
	}
	if len(fake.Calls) == 0 {
		t.Fatalf("engine never reached the runner: %v", err)
	}
	if got := fake.Calls[0].Dependencies; len(got) != 1 || got[0].Path != pin.Path {
		t.Fatalf("pinned rclone dependency not forwarded for digest verification: %+v", got)
	}
}

// splitShellLike mirrors restic's SplitShellStrings for the shapes this codebase
// can produce: outside quotes, a backslash or any unicode space is a separator.
func splitShellLike(s string) []string {
	var out []string
	start := -1
	for i, r := range s {
		sep := r == '\\' || r == '"' || r == '\'' || unicode.IsSpace(r)
		if sep {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		} else if start == -1 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}
