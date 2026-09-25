package preflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/canblmz1/stowline/agent/internal/adapters/process"
	"github.com/canblmz1/stowline/agent/internal/adapters/restic"
	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
	"github.com/canblmz1/stowline/agent/internal/adapters/storage"
	"github.com/canblmz1/stowline/agent/internal/config"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/windows/acl"
	"github.com/canblmz1/stowline/agent/internal/windows/identity"
	"github.com/canblmz1/stowline/agent/internal/windows/paths"
	winsvc "github.com/canblmz1/stowline/agent/internal/windows/service"
)

const (
	PASS    = "PASS"
	FAIL    = "FAIL"
	BLOCKED = "BLOCKED_EXTERNAL"
	WARNING = "WARNING"
)

type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type Report struct {
	GeneratedAt string  `json:"generated_at"`
	Checks      []Check `json:"checks"`
	Note        string  `json:"note"`
}

func Run(cfg *config.File) Report {
	rep := Report{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Note:        "Non-destructive. Does not install the Windows Service. Does not print secrets.",
	}
	add := func(c Check) { rep.Checks = append(rep.Checks, c) }

	if identity.Elevated() {
		add(Check{"elevated", PASS, "process token is elevated"})
	} else {
		add(Check{"elevated", WARNING, "not elevated; LocalSystem/VSS/service-ACL proofs remain external"})
	}
	add(processIdentityCheck())

	exe, err := os.Executable()
	if err != nil {
		add(Check{"binary_path", FAIL, err.Error()})
	} else {
		add(Check{"binary_path", PASS, exe})
		if sum, err := fileSHA(exe); err != nil {
			add(Check{"binary_digest", FAIL, err.Error()})
		} else {
			add(Check{"binary_digest", PASS, sum})
		}
	}

	checkPin := func(name string, pin domain.BinaryPin) {
		if pin.Path == "" || pin.SHA256 == "" {
			add(Check{name, FAIL, "pin missing"})
			return
		}
		sum, err := fileSHA(pin.Path)
		if err != nil {
			add(Check{name, FAIL, err.Error()})
			return
		}
		if !strings.EqualFold(sum, pin.SHA256) {
			add(Check{name, FAIL, "digest mismatch"})
			return
		}
		add(Check{name, PASS, pin.Version + " " + sum})
	}
	checkPin("restic_digest", cfg.Binaries.Restic)
	checkPin("rclone_digest", cfg.Binaries.Rclone)

	if err := secrets.RequireServiceProvider(cfg.PasswordRef); err != nil {
		add(Check{"service_secret_provider", FAIL, domain.Redact(err.Error())})
	} else {
		add(Check{"service_secret_provider", PASS, cfg.PasswordRef.Provider})
		raw, err := os.ReadFile(cfg.PasswordRef.Locator)
		if err != nil {
			add(Check{"service_secret_envelope", FAIL, "secret file unreadable by this identity (expected under service ACL if you are not SYSTEM)"})
			add(decryptSkippedBecauseEnvelopeUnreadable())
		} else if !strings.Contains(string(raw), "scope=machine") {
			add(Check{"service_secret_envelope", FAIL, "envelope is not machine-scope"})
		} else {
			add(Check{"service_secret_envelope", PASS, "machine-scope envelope present"})
			add(localSystemDecryptCheck(cfg))
		}
	}

	secretDir := cfg.PasswordRef.Locator
	if i := strings.LastIndex(secretDir, `\`); i >= 0 {
		secretDir = secretDir[:i]
	}
	if insp, err := acl.Inspect(secretDir); err != nil {
		add(Check{"service_secret_acl", BLOCKED, "ACL inspect failed: " + domain.Redact(err.Error())})
	} else if ok, reason := acl.EvaluateServiceSecret(insp.ACEs); !ok {
		add(Check{"service_secret_acl", FAIL, reason})
	} else {
		add(Check{"service_secret_acl", PASS, reason})
	}

	add(repositoryAuthCheck(cfg))

	if f, err := os.OpenFile(cfg.JournalPath, os.O_RDWR|os.O_CREATE, 0600); err != nil {
		add(Check{"journal_writable", FAIL, err.Error()})
	} else {
		_ = f.Close()
		add(Check{"journal_writable", PASS, "journal path writable by current identity"})
	}

	for _, root := range cfg.SourceRoots {
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			add(Check{"source_volume", FAIL, root})
		} else {
			add(Check{"source_volume", PASS, root})
		}
	}

	add(vssCheck())

	pol := domain.DefaultPilotPolicy(cfg.SourceRoots)
	if err := pol.Schedule.Validate(); err != nil {
		add(Check{"schedule_timezone", FAIL, err.Error()})
	} else {
		add(Check{"schedule_timezone", PASS, pol.Schedule.Timezone + " " + pol.Schedule.WindowStartHHMM + "-" + pol.Schedule.WindowEndHHMM})
	}

	if err := paths.ValidateStagingRoot(cfg.StagingRoot); err != nil {
		add(Check{"restore_staging", FAIL, err.Error()})
	} else {
		add(Check{"restore_staging", PASS, cfg.StagingRoot})
	}

	installed, bin, err := winsvc.QueryInstalled()
	if err != nil {
		add(Check{"scm_conflict", BLOCKED, "SCM query failed: " + err.Error()})
	} else if installed {
		add(Check{"scm_conflict", PASS, "StowlineBackup registered (single instance): " + bin})
	} else {
		add(Check{"scm_conflict", PASS, "StowlineBackup is not installed"})
	}

	free, err := volumeFree(cfg.Repository.Location)
	if err != nil {
		add(Check{"space_threshold", WARNING, err.Error()})
	} else if int64(free) < pol.FreeSpaceFloorBytes {
		add(Check{"space_threshold", FAIL, fmt.Sprintf("free=%d floor=%d", free, pol.FreeSpaceFloorBytes)})
	} else {
		add(Check{"space_threshold", PASS, fmt.Sprintf("free_bytes=%d", free)})
	}

	return rep
}

func (r Report) WriteJSON(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}

// HasFail reports an explicit FAIL check. WARNING is not FAIL.
func (r Report) HasFail() bool {
	for _, c := range r.Checks {
		if c.Status == FAIL {
			return true
		}
	}
	return false
}

// Unsuccessful is true when any check is FAIL or BLOCKED_EXTERNAL.
// Interactive diagnostics are expected to be unsuccessful. LocalSystem
// qualification requires every check PASS.
func (r Report) Unsuccessful() bool {
	for _, c := range r.Checks {
		if c.Status == FAIL || c.Status == BLOCKED {
			return true
		}
	}
	return false
}

// DefaultReportPath is under the pilot cache, never the git evidence tree.
func DefaultReportPath(pilotRoot string) string {
	if strings.TrimSpace(pilotRoot) == "" {
		return filepath.Join(os.TempDir(), "stowline-preflight-last.json")
	}
	return filepath.Join(pilotRoot, "cache", "preflight-last.json")
}

func decryptSkippedBecauseEnvelopeUnreadable() Check {
	sys, err := identity.IsLocalSystem()
	if err != nil {
		return Check{"localsystem_decrypt", FAIL, "process token inspect failed: " + domain.Redact(err.Error())}
	}
	if sys {
		return Check{"localsystem_decrypt", FAIL, "envelope unreadable under LocalSystem"}
	}
	return Check{"localsystem_decrypt", BLOCKED, "not LocalSystem (S-1-5-18); envelope unreadable by this identity"}
}

func fileSHA(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func processIdentityCheck() Check {
	detail, err := identity.Describe()
	if err != nil {
		return Check{"process_identity", FAIL, domain.Redact(err.Error())}
	}
	return Check{"process_identity", PASS, detail}
}

func localSystemDecryptCheck(cfg *config.File) Check {
	sys, err := identity.IsLocalSystem()
	if err != nil {
		return Check{"localsystem_decrypt", FAIL, "process token inspect failed: " + domain.Redact(err.Error())}
	}
	if !sys {
		return Check{"localsystem_decrypt", BLOCKED, "not LocalSystem (S-1-5-18); CryptUnprotectData not attempted"}
	}
	if err := secrets.RequireServiceProvider(cfg.PasswordRef); err != nil {
		return Check{"localsystem_decrypt", FAIL, domain.Redact(err.Error())}
	}
	prov, _, err := secrets.ForRef(cfg.PasswordRef)
	if err != nil {
		return Check{"localsystem_decrypt", FAIL, domain.Redact(err.Error())}
	}
	h, err := prov.Open(context.Background(), cfg.PasswordRef)
	if err != nil {
		return Check{"localsystem_decrypt", FAIL, sanitizeWinErr(err)}
	}
	n := 0
	if h != nil {
		n = len(h.Bytes())
		_ = h.Close()
	}
	if n == 0 {
		return Check{"localsystem_decrypt", FAIL, "CryptUnprotectData returned empty secret"}
	}
	return Check{"localsystem_decrypt", PASS, "CryptUnprotectData succeeded for machine-scope envelope"}
}

func repositoryAuthCheck(cfg *config.File) Check {
	if cfg == nil {
		return Check{"repository_auth", FAIL, "missing config"}
	}
	if err := secrets.RequireServiceProvider(cfg.PasswordRef); err != nil {
		return Check{"repository_auth", FAIL, domain.Redact(err.Error())}
	}
	id, err := liveCatConfig(cfg)
	return ClassifyRepositoryAuth(cfg.Repository.ExpectedRepoID, id, err)
}

func liveCatConfig(cfg *config.File) (string, error) {
	prov, _, err := secrets.ForRef(cfg.PasswordRef)
	if err != nil {
		return "", err
	}
	if cfg.TempDir != "" {
		_ = os.MkdirAll(cfg.TempDir, 0700)
	}
	eng := &restic.Adapter{
		Runner:     process.NewRunner(),
		Secrets:    prov,
		Connector:  storage.Connector{RclonePin: cfg.Binaries.Rclone},
		Pin:        cfg.Binaries.Restic,
		TempDir:    cfg.TempDir,
		CacheDir:   cfg.CacheDir,
		SystemRoot: os.Getenv("SystemRoot"),
		ExtraPath:  []string{filepath.Dir(cfg.Binaries.Rclone.Path)},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return eng.CatConfig(ctx, cfg.Repository, cfg.PasswordRef)
}

// ClassifyRepositoryAuth maps a read-only restic cat-config result into a preflight check.
// Wrong password is AUTH. Unknown failures are FAIL, never PASS. Detail never includes secrets.
func ClassifyRepositoryAuth(expectedID, gotID string, err error) Check {
	if err != nil {
		if errors.Is(err, domain.ErrAuth) {
			return Check{"repository_auth", FAIL, "AUTH"}
		}
		if errors.Is(err, domain.ErrRepositoryBinding) {
			return Check{"repository_auth", FAIL, "repository identity mismatch"}
		}
		detail := sanitizeWinErr(err)
		if domain.ContainsSecret(detail) {
			detail = "repository authentication failed"
		}
		if detail == "" {
			detail = "repository authentication failed"
		}
		return Check{"repository_auth", FAIL, detail}
	}
	if strings.TrimSpace(gotID) == "" {
		return Check{"repository_auth", FAIL, "empty repository identity"}
	}
	if expectedID != "" && !strings.EqualFold(gotID, expectedID) {
		return Check{"repository_auth", FAIL, "repository identity mismatch"}
	}
	return Check{"repository_auth", PASS, "authenticated repository=" + safeRepoID(gotID)}
}

func safeRepoID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 12 {
		if id == "" {
			return "unknown"
		}
		return id
	}
	return id[:12] + "…"
}

// RefuseServiceStart is the operator `service start` gate. It does not run inside SCM.
func RefuseServiceStart(rep Report) error {
	for _, c := range rep.Checks {
		if c.Name != "repository_auth" {
			continue
		}
		if c.Status != PASS {
			return fmt.Errorf("refusing service start: repository_auth=%s %s", c.Status, domain.Redact(c.Detail))
		}
		return nil
	}
	return fmt.Errorf("refusing service start: repository_auth check missing")
}

func sanitizeWinErr(err error) string {
	if err == nil {
		return ""
	}
	s := domain.Redact(err.Error())
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 240 {
		s = s[:240] + "...[truncated]"
	}
	return strings.TrimSpace(s)
}
