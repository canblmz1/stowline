package enroll

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/config"
	ctrlclient "github.com/canblmz1/stowline/agent/internal/control"
	"github.com/canblmz1/stowline/agent/internal/domain"
)

const canaryGateway = "STOWLINE_CANARY_GATEWAY_DO_NOT_LEAK_7e1d"

type fakeClient struct {
	enrollCalls int
	abortCalls  int
	enroll      *ctrlclient.EnrollResult
	enrollErr   error
	abortErr    error
}

func (f *fakeClient) Enroll(context.Context, string, string, string, string) (*ctrlclient.EnrollResult, error) {
	f.enrollCalls++
	if f.enrollErr != nil {
		return nil, f.enrollErr
	}
	out := *f.enroll
	return &out, nil
}

func (f *fakeClient) AbortEnrollment(context.Context, string) error {
	f.abortCalls++
	return f.abortErr
}

func sampleEnroll() *ctrlclient.EnrollResult {
	return &ctrlclient.EnrollResult{
		DeviceID:          "dev-enroll-1",
		InstallationID:    "inst-enroll-1",
		GenerationID:      "gen-enroll-1",
		ControlCredential: domain.CanarySecret,
		GatewayUsername:   "dev-enroll-1",
		GatewayCredential: canaryGateway,
		GatewayLocation:   "rest:http://127.0.0.1:8081/restic/dev-enroll-1/gen-enroll-1",
		SiteID:            "hq",
	}
}

func testCfg(t *testing.T) *config.File {
	t.Helper()
	d := config.DefaultPilot()
	root := t.TempDir()
	d.PilotRoot = root
	d.SourceRoots = []string{filepath.Join(root, "TestCorpus")}
	d.Repository.Location = filepath.Join(root, "Repos", "local")
	d.PasswordRef.Locator = filepath.Join(root, "secrets", "restic-password.user.dpapi")
	d.StagingRoot = filepath.Join(root, "Restore")
	d.JournalPath = filepath.Join(root, "state", "journal.sqlite")
	d.CacheDir = filepath.Join(root, "cache")
	d.TempDir = filepath.Join(root, "cache", "tmp")
	return &d
}

func skipPreflight(string, string) error { return nil }

func noopACL(string, string) error { return nil }

func assertNoSecrets(t *testing.T, s string) {
	t.Helper()
	if domain.ContainsSecret(s, canaryGateway) {
		t.Fatalf("diagnostic leaked secret material: %q", s)
	}
}

func TestParseModeDefaultIsPilotNotService(t *testing.T) {
	for _, in := range []string{"", "pilot", "PILOT", " Pilot "} {
		m, err := ParseMode(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if m != ModePilot {
			t.Fatalf("%q: got %s", in, m)
		}
	}
	m, err := ParseMode("service")
	if err != nil || m != ModeService {
		t.Fatalf("service: %s %v", m, err)
	}
	for _, bad := range []string{"system", "machine", "localsystem", "user", "auto"} {
		if _, err := ParseMode(bad); err == nil {
			t.Fatalf("must reject %q", bad)
		}
	}
}

func TestPreflightFailureDoesNotConsumeToken(t *testing.T) {
	cfg := testCfg(t)
	fc := &fakeClient{enroll: sampleEnroll()}
	_, err := Run(context.Background(), Request{
		Mode:           ModeService,
		URL:            "http://127.0.0.1:8080",
		Token:          "one-time-token",
		Cfg:            cfg,
		Client:         fc,
		Preflight:      func(string, string) error { return fmt.Errorf("%w: secrets path not writable", domain.ErrConfig) },
		ApplySecretACL: noopACL,
		PersistConfig:  func(config.File) error { return nil },
	})
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	if fc.enrollCalls != 0 {
		t.Fatalf("token must not be consumed before preflight: enrollCalls=%d", fc.enrollCalls)
	}
	if fc.abortCalls != 0 {
		t.Fatalf("abort must not run when enroll never happened: %d", fc.abortCalls)
	}
	assertNoSecrets(t, err.Error())
}

func TestServicePreflightUnwritableDoesNotConsumeToken(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "secrets")
	if err := os.WriteFile(blocked, []byte("not-a-directory"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := testCfg(t)
	cfg.PilotRoot = root
	fc := &fakeClient{enroll: sampleEnroll()}
	_, err := Run(context.Background(), Request{
		Mode:           ModeService,
		URL:            "http://127.0.0.1:8080",
		Token:          "one-time-token",
		Cfg:            cfg,
		Client:         fc,
		PersistConfig:  func(config.File) error { t.Fatal("persist"); return nil },
		ApplySecretACL: noopACL,
	})
	if err == nil {
		t.Fatal("expected unwritable secrets preflight failure")
	}
	if fc.enrollCalls != 0 {
		t.Fatalf("remote enroll must not run: %d", fc.enrollCalls)
	}
	if fc.abortCalls != 0 {
		t.Fatalf("abort must not run: %d", fc.abortCalls)
	}
}

func TestPersistFailureAbortsRemoteDevice(t *testing.T) {
	cfg := testCfg(t)
	dir := filepath.Join(cfg.PilotRoot, "secrets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	restic := filepath.Join(dir, "restic-password.user.dpapi")
	if err := os.WriteFile(restic, []byte("existing-restic-envelope-placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	roots := append([]string(nil), cfg.SourceRoots...)
	backend := cfg.Repository.BackendKind
	fc := &fakeClient{enroll: sampleEnroll()}
	_, err := Run(context.Background(), Request{
		Mode:           ModeService,
		URL:            "http://127.0.0.1:8080",
		Token:          "one-time-token",
		Cfg:            cfg,
		Client:         fc,
		Preflight:      skipPreflight,
		ApplySecretACL: noopACL,
		PersistConfig:  func(config.File) error { return fmt.Errorf("disk full") },
	})
	if err == nil {
		t.Fatal("expected persist failure")
	}
	if fc.enrollCalls != 1 {
		t.Fatalf("enrollCalls=%d", fc.enrollCalls)
	}
	if fc.abortCalls != 1 {
		t.Fatalf("persist failure must abort remote device, abortCalls=%d", fc.abortCalls)
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("operator error must report abort: %v", err)
	}
	assertNoSecrets(t, err.Error())
	if _, statErr := os.Stat(restic); statErr != nil {
		t.Fatal("existing restic envelope must not be scrubbed")
	}
	for _, n := range []string{controlMachineName, gatewayMachineName, gatewayUserFile} {
		if _, statErr := os.Stat(filepath.Join(dir, n)); statErr == nil {
			t.Fatalf("failed persist must scrub %s", n)
		}
	}
	if cfg.DeviceID != "pilot-device" || cfg.ControlPlaneURL != "" || cfg.SiteID != "" {
		t.Fatalf("config identity must roll back: %+v", cfg)
	}
	if cfg.Repository.BackendKind != backend {
		t.Fatal("LOCAL backend must remain after failed persist")
	}
	if len(cfg.SourceRoots) != len(roots) || cfg.SourceRoots[0] != roots[0] {
		t.Fatalf("source_roots changed: %v", cfg.SourceRoots)
	}
}

func TestBindPilotUserRefsKeepsLocalAndSourceRoots(t *testing.T) {
	cfg := testCfg(t)
	roots := append([]string(nil), cfg.SourceRoots...)
	out := &ctrlclient.EnrollResult{
		DeviceID:          "dev-local",
		InstallationID:    "inst-local",
		GenerationID:      "gen-local",
		ControlCredential: domain.CanarySecret,
		SiteID:            "branch",
	}
	if err := Bind(cfg, out, "http://127.0.0.1:8080", ModePilot); err != nil {
		t.Fatal(err)
	}
	if cfg.DeviceID != "dev-local" || cfg.SiteID != "branch" || cfg.ControlPlaneURL != "http://127.0.0.1:8080" {
		t.Fatalf("binding: device=%s site=%s url=%s", cfg.DeviceID, cfg.SiteID, cfg.ControlPlaneURL)
	}
	if cfg.ControlSecretRef.Provider != domain.SecretProviderDPAPIUser {
		t.Fatalf("pilot control ref %s", cfg.ControlSecretRef.Provider)
	}
	if !strings.HasSuffix(cfg.ControlSecretRef.Locator, controlUserName) {
		t.Fatalf("pilot control locator %s", cfg.ControlSecretRef.Locator)
	}
	if cfg.Repository.BackendKind != domain.BackendLocal {
		t.Fatal("empty gateway location must leave LOCAL backend")
	}
	if cfg.PasswordRef.Provider != domain.SecretProviderDPAPIUser {
		t.Fatal("restic password ref must be unchanged")
	}
	if len(cfg.SourceRoots) != len(roots) || cfg.SourceRoots[0] != roots[0] {
		t.Fatalf("source_roots: %v", cfg.SourceRoots)
	}
}

func TestBindServiceModeMachineRefs(t *testing.T) {
	cfg := testCfg(t)
	roots := append([]string(nil), cfg.SourceRoots...)
	out := sampleEnroll()
	if err := Bind(cfg, out, "https://control.example.internal", ModeService); err != nil {
		t.Fatal(err)
	}
	if cfg.DeviceID != out.DeviceID || cfg.SiteID != "hq" || cfg.ControlPlaneURL != "https://control.example.internal" {
		t.Fatalf("service bind: %+v", cfg)
	}
	if cfg.ControlSecretRef.Provider != domain.SecretProviderDPAPIMachine {
		t.Fatalf("service control ref %s", cfg.ControlSecretRef.Provider)
	}
	if !strings.HasSuffix(cfg.ControlSecretRef.Locator, controlMachineName) {
		t.Fatalf("service control locator %s", cfg.ControlSecretRef.Locator)
	}
	pw := cfg.Repository.CredentialRefs[domain.SecretRESTPassword]
	if pw.Provider != domain.SecretProviderDPAPIMachine || !strings.HasSuffix(pw.Locator, gatewayMachineName) {
		t.Fatalf("service gateway ref %+v", pw)
	}
	if cfg.Repository.BackendKind != domain.BackendRESTGateway {
		t.Fatal("gateway location must bind REST")
	}
	if !cfg.Repository.Capabilities.QualificationOnly {
		t.Fatal("existing qualification-only REST bind must remain")
	}
	if len(cfg.SourceRoots) != len(roots) || cfg.SourceRoots[0] != roots[0] {
		t.Fatalf("source_roots: %v", cfg.SourceRoots)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, string(raw))
}

// Reproduces a live finding: re-enrolling this physical endpoint carried a
// PRIOR repository's ExpectedRepoID into a brand-new one's config (Bind
// only ever set BackendKind/Location/GenerationID/DeviceID/Capabilities,
// never touching whatever ExpectedRepoID a previous enrollment had left
// behind). The new, still-uninitialized repository's first real backup then
// failed closed with ErrRepositoryBinding before ever running restic,
// because the restic adapter compares the repo's real id against this
// stale expectation. A fresh enrollment must start with no expectation at
// all -- ops.go already documents that this field auto-populates itself
// from the first genuine successful read of the (now truly new) repository.
func TestBindClearsStaleExpectedRepoIDFromAPriorEnrollment(t *testing.T) {
	cfg := testCfg(t)
	cfg.Repository.ExpectedRepoID = "381f24fd695a51ed589281bd744a3f5fe894c2198cac38aff6832d700d3d927d"
	out := sampleEnroll()
	if err := Bind(cfg, out, "https://control.example.internal", ModeService); err != nil {
		t.Fatal(err)
	}
	if cfg.Repository.ExpectedRepoID != "" {
		t.Fatalf("a new enrollment must not inherit a previous repository's ExpectedRepoID, got %q", cfg.Repository.ExpectedRepoID)
	}
}

func TestDiagnosticOmitsSecrets(t *testing.T) {
	r := &Result{
		DeviceID:        "dev-enroll-1",
		InstallationID:  "inst-enroll-1",
		GenerationID:    "gen-enroll-1",
		SiteID:          "hq",
		ControlPlaneURL: "http://127.0.0.1:8080",
		Mode:            ModeService,
	}
	s := DiagnosticString(r)
	assertNoSecrets(t, s)
	if !strings.Contains(s, "dev-enroll-1") || !strings.Contains(s, "hq") || !strings.Contains(s, ModeService) {
		t.Fatalf("diagnostic missing identity: %s", s)
	}
	if strings.Contains(s, domain.CanarySecret) {
		t.Fatal("canary in diagnostic")
	}
}
