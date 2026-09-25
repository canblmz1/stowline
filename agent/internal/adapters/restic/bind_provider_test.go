package restic

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/adapters/process"
	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
	"github.com/canblmz1/stowline/agent/internal/adapters/storage"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

// Real end-to-end failure, found on a real Windows PC after the DPAPI
// scope-mismatch bug was fixed: bind() opened the repository password, REST
// username, REST password, and rclone-config-pass all through ONE provider
// selected from PasswordRef alone. SecretRESTUsername is always
// Provider=file (plain text) in both pilot and service mode
// (enroll.go's Bind()), so opening it through a machine-scope DPAPI
// provider failed: "missing DPAPI envelope (refusing raw/plaintext)". These
// tests use real DPAPI machine-scope envelopes and real files -- no
// StaticProvider -- because the bug is specifically about provider
// SELECTION, which a StaticProvider papers over by construction.

func catConfigResult(id string) *ports.ProcessResult {
	return &ports.ProcessResult{ExitCode: 0, Stdout: []byte(`{"id":"` + id + `"}`)}
}

// restGatewayRepo builds the exact mixed-provider shape enroll.go's Bind()
// produces for a service-mode REST_GATEWAY enrollment: a machine-scope
// repository password, a plain-file REST username, and a machine-scope
// REST password, each a real, independently-protected secret under dir.
func restGatewayRepo(t *testing.T, dir string) (domain.RepositoryDescriptor, domain.SecretRef) {
	t.Helper()
	passwordRef := domain.SecretRef{
		Purpose:  domain.SecretResticPassword,
		Provider: domain.SecretProviderDPAPIMachine,
		Locator:  filepath.Join(dir, "restic-password.machine.dpapi"),
	}
	if err := secrets.NewService(dir).Protect(filepath.Base(passwordRef.Locator), []byte("repo-password-value")); err != nil {
		t.Fatalf("protect repo password: %v", err)
	}

	usernameRef := domain.SecretRef{
		Purpose:  domain.SecretRESTUsername,
		Provider: domain.SecretProviderFile,
		Locator:  filepath.Join(dir, "gateway-username.txt"),
	}
	if err := os.WriteFile(usernameRef.Locator, []byte("device-123"), 0600); err != nil {
		t.Fatalf("write username file: %v", err)
	}

	restPasswordRef := domain.SecretRef{
		Purpose:  domain.SecretRESTPassword,
		Provider: domain.SecretProviderDPAPIMachine,
		Locator:  filepath.Join(dir, "gateway-credential.machine.dpapi"),
	}
	if err := secrets.NewService(dir).Protect(filepath.Base(restPasswordRef.Locator), []byte("gateway-password-value")); err != nil {
		t.Fatalf("protect gateway password: %v", err)
	}

	repo := domain.RepositoryDescriptor{
		BackendKind:      domain.BackendRESTGateway,
		Location:         "rest:https://gateway.example.test/restic/dev/gen",
		GenerationID:     "gen",
		DeviceID:         "dev",
		TransportProfile: "qualification",
		Capabilities:     domain.DefaultCapabilities(domain.BackendRESTGateway),
		CredentialRefs: map[string]domain.SecretRef{
			domain.SecretRESTUsername: usernameRef,
			domain.SecretRESTPassword: restPasswordRef,
		},
	}
	return repo, passwordRef
}

func adapterFor(passwordRef domain.SecretRef, fake *process.Fake) (*Adapter, error) {
	prov, _, err := secrets.ForRef(passwordRef)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		Runner:    fake,
		Secrets:   prov,
		Connector: storage.Connector{},
		Pin:       domain.BinaryPin{Path: `C:\Stowline\bin\restic.exe`, SHA256: "abc"},
	}, nil
}

// 1. REST_GATEWAY + PasswordRef=dpapi-machine + REST username ref=file works.
// 2. REST password is opened using its own declared provider.
func TestBindOpensEachRefWithItsOwnDeclaredProvider(t *testing.T) {
	dir := t.TempDir()
	repo, passwordRef := restGatewayRepo(t, dir)
	fake := &process.Fake{Result: catConfigResult("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")}
	a, err := adapterFor(passwordRef, fake)
	if err != nil {
		t.Fatal(err)
	}

	id, err := a.CatConfig(context.Background(), repo, passwordRef)
	if err != nil {
		t.Fatalf("CatConfig with mixed providers: %v", err)
	}
	if id == "" {
		t.Fatal("expected a repo id")
	}

	env := map[string]string{}
	for _, e := range fake.Calls[0].Env {
		env[e.Name] = e.Value
	}
	if env["RESTIC_PASSWORD"] != "repo-password-value" {
		t.Fatalf("RESTIC_PASSWORD = %q", env["RESTIC_PASSWORD"])
	}
	if env["RESTIC_REST_USERNAME"] != "device-123" {
		t.Fatalf("RESTIC_REST_USERNAME = %q", env["RESTIC_REST_USERNAME"])
	}
	if env["RESTIC_REST_PASSWORD"] != "gateway-password-value" {
		t.Fatalf("RESTIC_REST_PASSWORD = %q", env["RESTIC_REST_PASSWORD"])
	}
}

// 3. A file secret passed to a DPAPI provider still fails -- proves the fix
// is correct SELECTION, not a silent fallback that would accept anything.
func TestOpenSecretRefRejectsPlaintextClaimingDPAPIMachine(t *testing.T) {
	dir := t.TempDir()
	loc := filepath.Join(dir, "not-really-dpapi.machine.dpapi")
	if err := os.WriteFile(loc, []byte("plaintext-not-a-dpapi-envelope"), 0600); err != nil {
		t.Fatal(err)
	}
	ref := domain.SecretRef{Provider: domain.SecretProviderDPAPIMachine, Locator: loc}
	if _, err := openSecretRef(context.Background(), ref); err == nil {
		t.Fatal("plaintext content must not be accepted by a DPAPI provider")
	}
}

// Converse of #3: a DPAPI envelope must not be accepted by the file
// provider either -- no raw/plaintext fallback in either direction.
func TestOpenSecretRefRejectsDPAPIEnvelopeClaimingFile(t *testing.T) {
	dir := t.TempDir()
	name := "looks-like-a-file.txt"
	if err := secrets.NewService(dir).Protect(name, []byte("real-secret")); err != nil {
		t.Fatal(err)
	}
	ref := domain.SecretRef{Provider: domain.SecretProviderFile, Locator: filepath.Join(dir, name)}
	if _, err := openSecretRef(context.Background(), ref); err == nil {
		t.Fatal("a DPAPI envelope must not be accepted by the file provider")
	}
}

// 4. dpapi-user repository password is still rejected in service mode.
func TestRequireServiceProviderStillRejectsDPAPIUserForMixedRepo(t *testing.T) {
	dir := t.TempDir()
	repo, _ := restGatewayRepo(t, dir)
	userPasswordRef := domain.SecretRef{
		Purpose:  domain.SecretResticPassword,
		Provider: domain.SecretProviderDPAPIUser,
		Locator:  filepath.Join(dir, "restic-password.user.dpapi"),
	}
	if err := secrets.NewPilot(dir).Protect(filepath.Base(userPasswordRef.Locator), []byte("repo-password-value")); err != nil {
		t.Fatal(err)
	}
	if err := secrets.RequireServiceProvider(userPasswordRef); err == nil {
		t.Fatal("dpapi-user repository password must be rejected in service mode")
	}
	_ = repo
}

// 5. REST_GATEWAY bind does not require, and must not open, unrelated
// rclone/direct-backend secrets -- even a broken one must be ignored.
func TestBindDoesNotOpenUnrelatedRcloneSecretForRESTGateway(t *testing.T) {
	dir := t.TempDir()
	repo, passwordRef := restGatewayRepo(t, dir)
	repo.CredentialRefs[domain.SecretRcloneConfigPass] = domain.SecretRef{
		Provider: domain.SecretProviderDPAPIMachine,
		Locator:  filepath.Join(dir, "does-not-exist.machine.dpapi"),
	}
	fake := &process.Fake{Result: catConfigResult("dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")}
	a, err := adapterFor(passwordRef, fake)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.CatConfig(context.Background(), repo, passwordRef); err != nil {
		t.Fatalf("a broken, backend-irrelevant credential must not be opened: %v", err)
	}
	for _, e := range fake.Calls[0].Env {
		if e.Name == "RCLONE_CONFIG_PASS" {
			t.Fatal("REST_GATEWAY must not set RCLONE_CONFIG_PASS")
		}
	}
}

// 6. The LocalSystem machine-DPAPI path remains valid: openSecretRef
// resolves a machine-scope ref the same way secrets.ForRef always has.
// Full LocalSystem-identity proof (CryptUnprotectData under the real
// LocalSystem account) is agent/cmd/stowline-agent/secrets_provision_test.go
// plus the live SYSTEM-scheduled-task run against the qualified binary.
func TestOpenSecretRefResolvesMachineScope(t *testing.T) {
	dir := t.TempDir()
	ref := domain.SecretRef{Provider: domain.SecretProviderDPAPIMachine, Locator: filepath.Join(dir, "x.machine.dpapi")}
	if err := secrets.NewService(dir).Protect(filepath.Base(ref.Locator), []byte("machine-secret")); err != nil {
		t.Fatal(err)
	}
	h, err := openSecretRef(context.Background(), ref)
	if err != nil {
		t.Fatalf("machine-scope open: %v", err)
	}
	defer h.Close()
	if string(h.Bytes()) != "machine-secret" {
		t.Fatal("machine-scope secret did not round-trip")
	}
}

// 7. The mixed-provider REST_GATEWAY shape succeeds end-to-end through
// CatConfig -- the same call repository_auth's preflight check makes.
func TestCatConfigSucceedsForMixedProviderRESTGateway(t *testing.T) {
	dir := t.TempDir()
	repo, passwordRef := restGatewayRepo(t, dir)
	repo.ExpectedRepoID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	fake := &process.Fake{Result: catConfigResult(repo.ExpectedRepoID)}
	a, err := adapterFor(passwordRef, fake)
	if err != nil {
		t.Fatal(err)
	}
	id, err := a.CatConfig(context.Background(), repo, passwordRef)
	if err != nil {
		t.Fatalf("repository_auth's CatConfig must succeed for a correctly mixed-provider REST_GATEWAY repo: %v", err)
	}
	if id != repo.ExpectedRepoID {
		t.Fatalf("id = %q want %q", id, repo.ExpectedRepoID)
	}
}

// 8. Existing LOCAL/direct-backend behavior (no CredentialRefs at all) does
// not regress: bind() must not require or look for REST/rclone secrets.
func TestBindLocalBackendUnaffectedByCredentialRefsGating(t *testing.T) {
	dir := t.TempDir()
	passwordRef := domain.SecretRef{Provider: domain.SecretProviderFile, Locator: filepath.Join(dir, "pw.txt")}
	if err := os.WriteFile(passwordRef.Locator, []byte("local-password"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := storage.LocalDescriptor(filepath.Join(dir, "repo"), "gen", "dev")
	fake := &process.Fake{Result: catConfigResult("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")}
	a, err := adapterFor(passwordRef, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CatConfig(context.Background(), repo, passwordRef); err != nil {
		t.Fatalf("LOCAL backend with no CredentialRefs must still work: %v", err)
	}
	for _, e := range fake.Calls[0].Env {
		if e.Name == "RESTIC_REST_USERNAME" || e.Name == "RESTIC_REST_PASSWORD" || e.Name == "RCLONE_CONFIG_PASS" {
			t.Fatalf("LOCAL backend must not set %s", e.Name)
		}
	}
}
