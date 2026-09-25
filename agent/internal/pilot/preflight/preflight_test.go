package preflight

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/config"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/windows/identity"
)

func TestSanitizeWinErrRedactsCanary(t *testing.T) {
	s := sanitizeWinErr(fmt.Errorf("CryptUnprotectData: %s", domain.CanarySecret))
	if strings.Contains(s, domain.CanarySecret) {
		t.Fatalf("secret leaked in preflight detail: %q", s)
	}
	if !strings.Contains(s, "[REDACTED]") {
		t.Fatalf("expected redaction: %q", s)
	}
}

func TestProcessIdentityCheck(t *testing.T) {
	c := processIdentityCheck()
	if c.Name != "process_identity" {
		t.Fatalf("%+v", c)
	}
	if c.Status != PASS && c.Status != FAIL {
		t.Fatalf("identity status %s", c.Status)
	}
	sys, err := identity.IsLocalSystem()
	if err != nil {
		t.Fatal(err)
	}
	if sys {
		if c.Detail != "LocalSystem (S-1-5-18)" {
			t.Fatalf("SYSTEM describe: %q", c.Detail)
		}
		return
	}
	if strings.Contains(c.Detail, "LocalSystem") {
		t.Fatalf("non-SYSTEM must not report LocalSystem: %q", c.Detail)
	}
}

func TestLocalSystemDecryptNotAttemptedUnlessSystem(t *testing.T) {
	sys, err := identity.IsLocalSystem()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.File{PasswordRef: domain.SecretRef{
		Provider: domain.SecretProviderDPAPIMachine,
		Locator:  `C:\Stowline\secrets\restic-password.machine.dpapi`,
	}}
	c := localSystemDecryptCheck(cfg)
	if c.Name != "localsystem_decrypt" {
		t.Fatalf("%+v", c)
	}
	if sys {
		return
	}
	if c.Status != BLOCKED {
		t.Fatalf("interactive decrypt must stay BLOCKED_EXTERNAL, got %s %s", c.Status, c.Detail)
	}
	if strings.Contains(strings.ToLower(c.Detail), "scm") && !strings.Contains(c.Detail, "S-1-5-18") {
		t.Fatal("must not infer LocalSystem from SCM")
	}
}

func TestFormatDoesNotTreatElevationAsSystem(t *testing.T) {
	got := identity.Format("S-1-5-21-9-9-9-1001", false, true)
	if strings.Contains(got, "LocalSystem") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "Interactive administrator") {
		t.Fatal(got)
	}
}

func TestRepositoryAuthSameSecretPass(t *testing.T) {
	id := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	c := ClassifyRepositoryAuth("", id, nil)
	if c.Status != PASS || !strings.Contains(c.Detail, "authenticated repository=") {
		t.Fatalf("%+v", c)
	}
	if strings.Contains(c.Detail, id) && len(id) == 64 {
		t.Fatal("full repository id must not be copied into evidence")
	}
	if domain.ContainsSecret(c.Detail) {
		t.Fatal(c.Detail)
	}
}

func TestRepositoryAuthWrongPasswordIsAuthFail(t *testing.T) {
	c := ClassifyRepositoryAuth("", "", fmt.Errorf("%w: cat config", domain.ErrAuth))
	if c.Status != FAIL || c.Detail != "AUTH" {
		t.Fatalf("%+v", c)
	}
}

func TestRepositoryAuthUnknownIsFailNotPass(t *testing.T) {
	c := ClassifyRepositoryAuth("", "id", fmt.Errorf("restic exploded %s", domain.CanarySecret))
	if c.Status == PASS {
		t.Fatal("unknown must not PASS")
	}
	if c.Detail == "AUTH" {
		t.Fatal("unknown must not be classified AUTH")
	}
	if domain.ContainsSecret(c.Detail) || strings.Contains(c.Detail, domain.CanarySecret) {
		t.Fatalf("secret in evidence: %q", c.Detail)
	}
}

func TestRepositoryAuthIdentityMismatch(t *testing.T) {
	c := ClassifyRepositoryAuth("want", "got", nil)
	if c.Status != FAIL || c.Detail != "repository identity mismatch" {
		t.Fatalf("%+v", c)
	}
}

func TestHasFailAndUnsuccessful(t *testing.T) {
	ok := Report{Checks: []Check{{Name: "a", Status: PASS}, {Name: "b", Status: WARNING}}}
	if ok.HasFail() || ok.Unsuccessful() {
		t.Fatal("PASS+WARNING must be successful")
	}
	fail := Report{Checks: []Check{{Name: "a", Status: FAIL}}}
	if !fail.HasFail() || !fail.Unsuccessful() {
		t.Fatal("FAIL must be unsuccessful")
	}
	blocked := Report{Checks: []Check{{Name: "a", Status: BLOCKED}}}
	if blocked.HasFail() {
		t.Fatal("BLOCKED_EXTERNAL is not FAIL")
	}
	if !blocked.Unsuccessful() {
		t.Fatal("BLOCKED_EXTERNAL must be unsuccessful for qualification")
	}
}

func TestDefaultReportPathNotGitEvidence(t *testing.T) {
	p := DefaultReportPath(`C:\Stowline`)
	n := strings.ToLower(filepath.ToSlash(p))
	if strings.Contains(n, "/docs/pilot-evidence/") {
		t.Fatalf("must not write evidence tree by default: %s", p)
	}
	if !strings.HasSuffix(n, "/cache/preflight-last.json") {
		t.Fatalf("got %s", p)
	}
}

func TestRefuseServiceStart(t *testing.T) {
	if err := RefuseServiceStart(Report{}); err == nil {
		t.Fatal("missing check must refuse")
	}
	if err := RefuseServiceStart(Report{Checks: []Check{{Name: "repository_auth", Status: FAIL, Detail: "AUTH"}}}); err == nil {
		t.Fatal("FAIL must refuse")
	}
	if err := RefuseServiceStart(Report{Checks: []Check{{Name: "repository_auth", Status: PASS, Detail: "authenticated repository=abc"}}}); err != nil {
		t.Fatal(err)
	}
}
