package securityscan

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func TestNoExecCommandOutsideProcessAdapter(t *testing.T) {
	root := filepath.Join(repoRoot(t), "agent")
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := string(b)
		if strings.Contains(s, "exec.Command") || strings.Contains(s, "exec.CommandContext") {
			if !strings.Contains(filepath.ToSlash(p), "/adapters/process/") {
				t.Errorf("exec.Command outside process adapter: %s", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestForgetPruneNotAllowlisted(t *testing.T) {
	p := filepath.Join(repoRoot(t), "agent", "internal", "adapters", "restic", "args.go")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, `"forget": {}`) || strings.Contains(s, `"prune": {}`) || strings.Contains(s, `"unlock": {}`) {
		t.Fatal("forget/prune/unlock must not be in allowedCommands")
	}
}

func TestNoPasswordFlagsInResticArgs(t *testing.T) {
	p := filepath.Join(repoRoot(t), "agent", "internal", "adapters", "restic", "args.go")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `HasPrefix(al, "--password")`) {
		t.Fatal("restic args must reject --password*")
	}
	if strings.Contains(s, `args = append(args, "--password"`) {
		t.Fatal("must not pass --password on argv")
	}
}

func TestCmdDoesNotDefaultWriteGitEvidence(t *testing.T) {
	root := filepath.Join(repoRoot(t), "agent", "cmd")
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := filepath.ToSlash(string(b))
		if strings.Contains(s, "docs/pilot-evidence") || strings.Contains(s, `docs\pilot-evidence`) {
			t.Errorf("command defaults must not write git evidence: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPartialNeverFreshnessSQL(t *testing.T) {
	p := filepath.Join(repoRoot(t), "agent", "internal", "adapters", "journal", "sqlite.go")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "state='SUCCEEDED'") {
		t.Fatal("LatestSucceededBackupSlot must require SUCCEEDED")
	}
	if strings.Contains(string(b), "state IN ('SUCCEEDED','PARTIAL')") {
		t.Fatal("PARTIAL must not count as freshness success")
	}
}
