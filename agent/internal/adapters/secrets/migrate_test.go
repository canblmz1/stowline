package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

const testGoodSecret = "test-repo-secret-bytes-not-production"

func testOpen(_ context.Context, ref domain.SecretRef) (ports.SecretHandle, error) {
	b, err := os.ReadFile(ref.Locator)
	if err != nil {
		return nil, err
	}
	return &memoryHandle{data: append([]byte(nil), b...)}, nil
}

func testProtect(dir string) ProtectFunc {
	return func(name string, plaintext []byte) error {
		return os.WriteFile(filepath.Join(dir, name), append([]byte(nil), plaintext...), 0600)
	}
}

func testAuthFromDisk(good []byte) AuthenticateFunc {
	return func(ctx context.Context, ref domain.SecretRef) (string, error) {
		h, err := testOpen(ctx, ref)
		if err != nil {
			return "", err
		}
		defer h.Close()
		if !bytes.Equal(h.Bytes(), good) {
			return "", fmt.Errorf("%w", domain.ErrAuth)
		}
		return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
	}
}

func writeRepo(t *testing.T, root string) string {
	t.Helper()
	loc := filepath.Join(root, "repo")
	if err := os.MkdirAll(loc, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loc, "config"), []byte("restic-config-placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestReWrapPreservesExactSecretBytes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "secrets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UserRef(dir).Locator, []byte(testGoodSecret), 0600); err != nil {
		t.Fatal(err)
	}
	generated := false
	ref, err := ReWrapToMachine(context.Background(), MigrateRequest{
		Dir:            dir,
		RepoLocation:   writeRepo(t, root),
		Authenticate:   testAuthFromDisk([]byte(testGoodSecret)),
		Open:           testOpen,
		ProtectMachine: testProtect(dir),
		Generate: func() ([]byte, error) {
			generated = true
			return []byte("replacement-must-not-be-used"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if generated {
		t.Fatal("existing repository must not generate a replacement secret")
	}
	if ref.Provider != domain.SecretProviderDPAPIMachine {
		t.Fatalf("provider %s", ref.Provider)
	}
	got, err := os.ReadFile(ref.Locator)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(testGoodSecret)) {
		t.Fatal("machine envelope must contain the same secret bytes as the source")
	}
	user, err := os.ReadFile(UserRef(dir).Locator)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(user, []byte(testGoodSecret)) {
		t.Fatal("user envelope must remain intact")
	}
	if _, err := os.Stat(filepath.Join(dir, machineTmpName)); err == nil {
		t.Fatal("temporary envelope must be promoted away")
	}
}

func TestReWrapNeverGeneratesForExistingRepo(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "secrets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	generated := false
	_, err := ReWrapToMachine(context.Background(), MigrateRequest{
		Dir:          dir,
		RepoLocation: writeRepo(t, root),
		Authenticate: func(context.Context, domain.SecretRef) (string, error) {
			return "", fmt.Errorf("%w", domain.ErrAuth)
		},
		Open:           testOpen,
		ProtectMachine: testProtect(dir),
		Generate: func() ([]byte, error) {
			generated = true
			return []byte("replacement-must-not-be-used"), nil
		},
	})
	if err == nil {
		t.Fatal("expected fail-closed")
	}
	if generated {
		t.Fatal("must not generate a replacement password for an existing repository")
	}
	if _, err := os.Stat(MachineRef(dir).Locator); err == nil {
		t.Fatal("must not write a machine envelope from a generated secret")
	}
	if !errors.Is(err, domain.ErrConfig) && !strings.Contains(err.Error(), "refusing to generate") {
		t.Fatalf("want refuse-to-generate, got %v", err)
	}
}

func TestFailedMigrationLeavesPreviousSecretIntact(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "secrets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UserRef(dir).Locator, []byte(testGoodSecret), 0600); err != nil {
		t.Fatal(err)
	}
	wrong := []byte("wrong-machine-secret")
	if err := os.WriteFile(MachineRef(dir).Locator, wrong, 0600); err != nil {
		t.Fatal(err)
	}
	auth := func(ctx context.Context, ref domain.SecretRef) (string, error) {
		if strings.HasSuffix(ref.Locator, machineTmpName) {
			return "", fmt.Errorf("%w", domain.ErrAuth)
		}
		return testAuthFromDisk([]byte(testGoodSecret))(ctx, ref)
	}
	_, err := ReWrapToMachine(context.Background(), MigrateRequest{
		Dir:            dir,
		RepoLocation:   writeRepo(t, root),
		Authenticate:   auth,
		Open:           testOpen,
		ProtectMachine: testProtect(dir),
		Generate: func() ([]byte, error) {
			t.Fatal("generate")
			return nil, fmt.Errorf("generate")
		},
	})
	if err == nil {
		t.Fatal("expected tmp auth failure")
	}
	user, err := os.ReadFile(UserRef(dir).Locator)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(user, []byte(testGoodSecret)) {
		t.Fatal("user source must be unchanged")
	}
	got, err := os.ReadFile(MachineRef(dir).Locator)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wrong) {
		t.Fatal("previous machine envelope must be left in place after a failed promote")
	}
	if _, err := os.Stat(filepath.Join(dir, machineTmpName)); err == nil {
		t.Fatal("failed tmp envelope must be removed")
	}
}

func TestIdempotentWhenMachineAlreadyAuthenticates(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "secrets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(MachineRef(dir).Locator, []byte(testGoodSecret), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UserRef(dir).Locator, []byte(testGoodSecret), 0600); err != nil {
		t.Fatal(err)
	}
	protectCalls := 0
	_, err := ReWrapToMachine(context.Background(), MigrateRequest{
		Dir:          dir,
		RepoLocation: writeRepo(t, root),
		Authenticate: testAuthFromDisk([]byte(testGoodSecret)),
		Open:         testOpen,
		ProtectMachine: func(name string, plaintext []byte) error {
			protectCalls++
			return testProtect(dir)(name, plaintext)
		},
		Generate: func() ([]byte, error) {
			t.Fatal("generate")
			return nil, fmt.Errorf("generate")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if protectCalls != 0 {
		t.Fatal("must not rewrite a machine envelope that already authenticates")
	}
}

func TestReWrapNamedMissingSourceNoop(t *testing.T) {
	dir := t.TempDir()
	if err := ReWrapNamedToMachine(context.Background(), dir, "control-credential.user.dpapi", "control-credential.machine.dpapi"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "control-credential.machine.dpapi")); err == nil {
		t.Fatal("missing source must not create a machine envelope")
	}
}

func TestRepoConfigExistsTreatsMissingAsUninitialized(t *testing.T) {
	dir := t.TempDir()
	if RepoConfigExists(filepath.Join(dir, "no-repo")) {
		t.Fatal("missing config must be uninitialized")
	}
	loc := writeRepo(t, dir)
	if !RepoConfigExists(loc) {
		t.Fatal("config file means initialized")
	}
	if !RepoConfigExists("") {
		t.Fatal("unknown location must fail closed (assume exists)")
	}
}

func TestReWrapRepairsWrongMachineEnvelope(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "secrets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UserRef(dir).Locator, []byte(testGoodSecret), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(MachineRef(dir).Locator, []byte("wrong-machine-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	generated := false
	_, err := ReWrapToMachine(context.Background(), MigrateRequest{
		Dir:            dir,
		RepoLocation:   writeRepo(t, root),
		Authenticate:   testAuthFromDisk([]byte(testGoodSecret)),
		Open:           testOpen,
		ProtectMachine: testProtect(dir),
		Generate: func() ([]byte, error) {
			generated = true
			return []byte("replacement-must-not-be-used"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if generated {
		t.Fatal("repair must re-wrap the existing secret, not generate")
	}
	got, err := os.ReadFile(MachineRef(dir).Locator)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(testGoodSecret)) {
		t.Fatal("wrong machine envelope must be replaced with the source secret bytes")
	}
}
