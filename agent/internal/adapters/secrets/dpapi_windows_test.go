//go:build windows

package secrets

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func TestDPAPIUserRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := NewPilot(dir)
	plain := []byte("dpapi-pilot-roundtrip-not-a-real-secret")
	name := "restic-password.user.dpapi"
	if err := p.Protect(name, plain); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, plain) {
		t.Fatal("plaintext persisted on disk")
	}
	if !bytes.Contains(raw, []byte("STOWLINE-DPAPI-1")) || !bytes.Contains(raw, []byte("scope=user")) {
		t.Fatalf("envelope missing: %q", raw[:min(len(raw), 80)])
	}
	h, err := p.Open(context.Background(), domain.SecretRef{
		Purpose:  domain.SecretResticPassword,
		Locator:  filepath.Join(dir, name),
		Provider: domain.SecretProviderDPAPIUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if !bytes.Equal(h.Bytes(), plain) {
		t.Fatal("roundtrip mismatch")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if len(h.Bytes()) != 0 {
		t.Fatal("secret handle must wipe on close")
	}
}

func TestDPAPIMachineRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := NewService(dir)
	plain := []byte("dpapi-machine-roundtrip-not-a-real-secret")
	name := "restic-password.machine.dpapi"
	if err := p.Protect(name, plain); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, plain) {
		t.Fatal("plaintext persisted on disk")
	}
	if !bytes.Contains(raw, []byte("scope=machine")) {
		t.Fatal("machine envelope required")
	}
	h, err := p.Open(context.Background(), domain.SecretRef{
		Locator:  filepath.Join(dir, name),
		Provider: domain.SecretProviderDPAPIMachine,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if !bytes.Equal(h.Bytes(), plain) {
		t.Fatal("machine roundtrip mismatch")
	}
}

func TestDPAPIIncompatibleScopeFailsClosed(t *testing.T) {
	dir := t.TempDir()
	plain := []byte("scope-mismatch-not-a-real-secret")
	if err := NewPilot(dir).Protect("blob.user.dpapi", plain); err != nil {
		t.Fatal(err)
	}
	_, err := NewService(dir).Open(context.Background(), domain.SecretRef{
		Locator: filepath.Join(dir, "blob.user.dpapi"),
	})
	if err == nil {
		t.Fatal("machine provider must refuse user-scope blob")
	}
	if !strings.Contains(err.Error(), "scope") && err != domain.ErrSecretScopeMismatch {
		// wrapped
		if !bytes.Contains([]byte(err.Error()), []byte("scope")) {
			t.Fatalf("want scope mismatch, got %v", err)
		}
	}

	if err := NewService(dir).Protect("blob.machine.dpapi", plain); err != nil {
		t.Fatal(err)
	}
	_, err = NewPilot(dir).Open(context.Background(), domain.SecretRef{
		Locator: filepath.Join(dir, "blob.machine.dpapi"),
	})
	if err == nil {
		t.Fatal("user provider must refuse machine-scope blob")
	}
}

func TestDPAPIRefusesPlaintextAndRawBlob(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "restic-password")
	if err := os.WriteFile(p, []byte("deadbeefcafebabe"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := NewPilot(dir).Open(context.Background(), domain.SecretRef{Locator: p})
	if err == nil {
		t.Fatal("must refuse raw plaintext")
	}
}

func TestFileProviderRefusesDPAPIEnvelope(t *testing.T) {
	dir := t.TempDir()
	if err := NewPilot(dir).Protect("x.user.dpapi", []byte("not-a-real-secret")); err != nil {
		t.Fatal(err)
	}
	_, err := FileProvider{}.Open(context.Background(), domain.SecretRef{Locator: filepath.Join(dir, "x.user.dpapi")})
	if err == nil {
		t.Fatal("file provider must not open DPAPI locators")
	}
}

func TestRequireServiceProvider(t *testing.T) {
	if err := RequireServiceProvider(domain.SecretRef{Provider: domain.SecretProviderFile}); err == nil {
		t.Fatal("file provider not allowed in service mode")
	}
	if err := RequireServiceProvider(domain.SecretRef{Provider: domain.SecretProviderDPAPIUser}); err == nil {
		t.Fatal("user DPAPI not allowed in service mode")
	}
	if err := RequireServiceProvider(domain.SecretRef{Provider: domain.SecretProviderDPAPIMachine, Locator: `C:\Stowline\secrets\restic-password.machine.dpapi`}); err != nil {
		t.Fatal(err)
	}
	if err := RequireServiceProvider(domain.SecretRef{Provider: domain.SecretProviderDPAPIMachine, Locator: `C:\x\restic-password.user.dpapi`}); err == nil {
		t.Fatal("user locator must be refused")
	}
}

func TestForRefUnknownFailsClosed(t *testing.T) {
	_, _, err := ForRef(domain.SecretRef{Provider: "vault"})
	if err == nil {
		t.Fatal("unknown provider must fail closed")
	}
}
