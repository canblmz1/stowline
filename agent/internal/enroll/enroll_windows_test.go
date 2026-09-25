//go:build windows

package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
	"github.com/canblmz1/stowline/agent/internal/config"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/windows/acl"
	"github.com/canblmz1/stowline/agent/internal/windows/identity"
)

func persistJSON(t *testing.T, path string) func(config.File) error {
	t.Helper()
	return func(cfg config.File) error {
		b, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		return os.WriteFile(path, b, 0600)
	}
}

func TestPreflightServiceRoundTripRemovesProbe(t *testing.T) {
	dir := t.TempDir()
	if err := Preflight(ModeService, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, probeMachineName)); err == nil {
		t.Fatal("preflight probe must be removed")
	}
	if err := Preflight(ModePilot, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, probeUserName)); err == nil {
		t.Fatal("user preflight probe must be removed")
	}
}

func openAndCheck(t *testing.T, p secrets.DPAPIProvider, locator, want string) {
	t.Helper()
	h, err := p.Open(context.Background(), domain.SecretRef{Locator: locator})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if string(h.Bytes()) != want {
		t.Fatal("decrypt mismatch")
	}
}

func TestPilotEnrollmentPersistsUserScope(t *testing.T) {
	cfg := testCfg(t)
	cfgPath := filepath.Join(cfg.PilotRoot, "config", "pilot.json")
	fc := &fakeClient{enroll: sampleEnroll()}
	res, err := Run(context.Background(), Request{
		Mode:           ModePilot,
		URL:            "http://127.0.0.1:8080",
		Token:          "one-time-token",
		Cfg:            cfg,
		Client:         fc,
		Preflight:      skipPreflight,
		ApplySecretACL: noopACL,
		PersistConfig:  persistJSON(t, cfgPath),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fc.abortCalls != 0 {
		t.Fatal("successful persist must not abort")
	}
	dir := filepath.Join(cfg.PilotRoot, "secrets")
	ctrl := filepath.Join(dir, controlUserName)
	gw := filepath.Join(dir, gatewayUserName)
	scope, err := secrets.EnvelopeScope(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	if scope != domain.SecretScopeUser {
		t.Fatalf("pilot control scope %s", scope)
	}
	gwScope, err := secrets.EnvelopeScope(gw)
	if err != nil {
		t.Fatal(err)
	}
	if gwScope != domain.SecretScopeUser {
		t.Fatalf("pilot gateway scope %s", gwScope)
	}
	raw, err := os.ReadFile(ctrl)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(domain.CanarySecret)) || bytes.Contains(raw, []byte(canaryGateway)) {
		t.Fatal("plaintext control credential on disk")
	}
	openAndCheck(t, secrets.NewPilot(dir), ctrl, domain.CanarySecret)
	openAndCheck(t, secrets.NewPilot(dir), gw, canaryGateway)
	if _, err := secrets.NewService(dir).Open(context.Background(), domain.SecretRef{Locator: ctrl}); err == nil {
		t.Fatal("machine provider must not open user-scope control credential")
	}
	if cfg.ControlSecretRef.Provider != domain.SecretProviderDPAPIUser {
		t.Fatal(cfg.ControlSecretRef.Provider)
	}
	assertNoSecrets(t, DiagnosticString(res))
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, string(saved))
}

func TestServiceEnrollmentPersistsMachineScopeAndDecrypts(t *testing.T) {
	cfg := testCfg(t)
	roots := append([]string(nil), cfg.SourceRoots...)
	cfgPath := filepath.Join(cfg.PilotRoot, "config", "pilot.json")
	fc := &fakeClient{enroll: sampleEnroll()}
	res, err := Run(context.Background(), Request{
		Mode:           ModeService,
		URL:            "http://127.0.0.1:8080",
		Token:          "one-time-token",
		Cfg:            cfg,
		Client:         fc,
		Preflight:      skipPreflight,
		ApplySecretACL: noopACL,
		PersistConfig:  persistJSON(t, cfgPath),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fc.abortCalls != 0 || res.DeviceID != "dev-enroll-1" || res.SiteID != "hq" {
		t.Fatalf("result %+v abort=%d", res, fc.abortCalls)
	}
	dir := filepath.Join(cfg.PilotRoot, "secrets")
	ctrl := filepath.Join(dir, controlMachineName)
	gw := filepath.Join(dir, gatewayMachineName)
	for _, p := range []string{ctrl, gw} {
		scope, err := secrets.EnvelopeScope(p)
		if err != nil {
			t.Fatal(err)
		}
		if scope != domain.SecretScopeMachine {
			t.Fatalf("%s scope %s", p, scope)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(domain.CanarySecret)) || bytes.Contains(raw, []byte(canaryGateway)) {
			t.Fatal("plaintext persisted")
		}
		if bytes.Contains(raw, []byte("scope=user")) {
			t.Fatal("service envelope must not be user-scope")
		}
	}
	// Machine-scope DPAPI is decryptable by LocalSystem (S-1-5-18) and by any
	// local process that can read the file. Isolation is the ServiceSecret ACL.
	svc := secrets.NewService(dir)
	openAndCheck(t, svc, ctrl, domain.CanarySecret)
	openAndCheck(t, svc, gw, canaryGateway)
	if _, err := secrets.NewPilot(dir).Open(context.Background(), domain.SecretRef{Locator: ctrl}); err == nil {
		t.Fatal("user provider must not open machine-scope control credential")
	}
	if _, err := secrets.NewPilot(dir).Open(context.Background(), domain.SecretRef{Locator: gw}); err == nil {
		t.Fatal("user provider must not open machine-scope gateway credential")
	}
	if cfg.ControlPlaneURL != "http://127.0.0.1:8080" || cfg.SiteID != "hq" || cfg.DeviceID != "dev-enroll-1" {
		t.Fatalf("runtime binding: %+v", cfg)
	}
	if cfg.ControlSecretRef.Provider != domain.SecretProviderDPAPIMachine {
		t.Fatal(cfg.ControlSecretRef.Provider)
	}
	if cfg.Repository.CredentialRefs[domain.SecretRESTPassword].Provider != domain.SecretProviderDPAPIMachine {
		t.Fatal("gateway password ref must be machine-scope")
	}
	if len(cfg.SourceRoots) != len(roots) || cfg.SourceRoots[0] != roots[0] {
		t.Fatalf("source_roots changed: %v", cfg.SourceRoots)
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk config.File
	if err := json.Unmarshal(saved, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.DeviceID != "dev-enroll-1" || onDisk.SiteID != "hq" || onDisk.ControlPlaneURL != "http://127.0.0.1:8080" {
		t.Fatalf("persisted binding: %+v", onDisk)
	}
	assertNoSecrets(t, string(saved))
	assertNoSecrets(t, DiagnosticString(res))
}

func TestServiceEnrollmentAppliesServiceSecretACL(t *testing.T) {
	cfg := testCfg(t)
	dir := filepath.Join(cfg.PilotRoot, "secrets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := acl.Apply(dir, acl.PilotWorking); err != nil {
		t.Skipf("ACL change not permitted: %v", err)
	}
	t.Cleanup(func() { _ = acl.Apply(dir, acl.PilotWorking) })
	fc := &fakeClient{enroll: sampleEnroll()}
	_, err := Run(context.Background(), Request{
		Mode:          ModeService,
		URL:           "http://127.0.0.1:8080",
		Token:         "one-time-token",
		Cfg:           cfg,
		Client:        fc,
		Preflight:     skipPreflight,
		PersistConfig: persistJSON(t, filepath.Join(cfg.PilotRoot, "config", "pilot.json")),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := acl.Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	ok, reason := acl.EvaluateServiceSecret(got.ACEs)
	if !ok {
		t.Fatalf("service-secret ACL: %s (%+v)", reason, got)
	}
	if !got.HasSystem || !got.HasAdministrators {
		t.Fatalf("SYSTEM+Administrators required: %+v", got)
	}
	sys, err := identity.IsLocalSystem()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, controlMachineName)
	if !sys && got.HasCurrentUser {
		t.Fatalf("ordinary current user must not have an ACE: %+v", got)
	}
	// See acl_windows_test.go's TestServiceSecretOmitsInteractiveUser: a
	// successful read only means something if the account running this
	// test is neither SYSTEM nor a member of Administrators, which the
	// ACL correctly grants access to (got.HasAdministrators above).
	admin, err := identity.IsAdministrator()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(marker); err == nil && !sys && !admin {
		t.Fatal("interactive user must not read service-secret enrollment files")
	}
}
