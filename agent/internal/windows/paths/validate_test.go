package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func TestRejectTraversalAndUNC(t *testing.T) {
	root := t.TempDir()
	cases := []string{
		filepath.Join(root, "..", "escape"),
		`\\server\share\x`,
		`C:Windows`,
		`\\?\C:\Windows`,
		`foo:bar.txt`,
		`CON`,
		filepath.Join(root, "ends."),
	}
	for _, c := range cases {
		if err := ValidateRestoreDestination(root, c); err == nil {
			t.Fatalf("expected reject %q", c)
		}
	}
}

func TestValidStagingChildAccepted(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "job-1")
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRestoreDestination(root, dest); err != nil {
		t.Fatal(err)
	}
}

func TestForbiddenWindowsDirs(t *testing.T) {
	if !IsForbiddenTarget(`C:\Windows\System32`) {
		t.Fatal("windows dir")
	}
	if err := ValidateRestoreDestination(`C:\Stowline\Restore`, `C:\Windows`); err == nil && IsForbiddenTarget(`C:\Windows`) {
		_ = domain.ErrRestorePathRejected
	}
}
