package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/adapters/process"
	"github.com/canblmz1/stowline/agent/internal/adapters/restic"
	"github.com/canblmz1/stowline/agent/internal/adapters/secrets"
	"github.com/canblmz1/stowline/agent/internal/adapters/storage"
	"github.com/canblmz1/stowline/agent/internal/config"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/pilot/preflight"
)

// Reproduces, in full isolation (a fresh temp PilotRoot -- never touches the
// real C:\Stowline on this machine), the exact failure found on a real
// external Windows PC: control enroll --mode=service only machine-scopes
// the control-plane/gateway-REST credentials (enroll.Bind/writeEnvelopes);
// PasswordRef -- the restic repository password -- is left exactly as
// bootstrap.py's initial `config write` set it (pilot/dpapi-user). A
// LocalSystem-run service's own preflight then permanently refuses to
// start: "service mode requires dpapi-machine, not dpapi-user". Proves
// `secrets provision --mode=service` (the agent's own existing
// ReWrapToMachine) fixes it without generating a replacement password --
// it re-authenticates the current one against a real repository first.
func TestServiceProvisionUpgradesPasswordRefToMachineScope(t *testing.T) {
	cfg := config.DefaultPilot()
	// config.DefaultPilot()'s Binaries.Restic/Rclone paths are not
	// overridden below (only PilotRoot-derived paths are) -- they stay
	// pinned to C:\Stowline\bin\*.exe, which this test genuinely needs
	// (it runs the real binary against a real local repository, not a
	// fake). Confirmed live: a fresh CI checkout has no C:\Stowline at
	// all, so this must skip there rather than fail.
	if _, err := os.Stat(cfg.Binaries.Restic.Path); err != nil {
		t.Skipf("pinned restic binary not available: %v", err)
	}
	root := t.TempDir()
	cfg.PilotRoot = root
	cfg.Repository.Location = filepath.Join(root, "Repos", "local")
	cfg.PasswordRef.Locator = filepath.Join(root, "secrets", "restic-password.user.dpapi")
	cfg.StagingRoot = filepath.Join(root, "Restore")
	cfg.JournalPath = filepath.Join(root, "state", "journal.sqlite")
	cfg.CacheDir = filepath.Join(root, "cache")
	cfg.TempDir = filepath.Join(root, "cache", "tmp")
	cfg.SourceRoots = []string{filepath.Join(root, "TestCorpus")}
	for _, dir := range []string{cfg.Repository.Location, cfg.TempDir, cfg.CacheDir, filepath.Dir(cfg.JournalPath)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	// Step 1: exactly what `stowline-agent.exe config write` does on first
	// bootstrap (bootstrap.py's layout()) -- pilot-mode provisioning,
	// generating and dpapi-user-protecting a fresh password since no
	// repository exists yet.
	if err := provisionSecrets(&cfg, "pilot"); err != nil {
		t.Fatalf("pilot provision: %v", err)
	}
	if cfg.PasswordRef.Provider != domain.SecretProviderDPAPIUser {
		t.Fatalf("expected dpapi-user after pilot provision, got %s", cfg.PasswordRef.Provider)
	}

	// Reproduce: exactly what a service-mode preflight sees right after
	// `control enroll --mode=service`, which never touches PasswordRef --
	// the same scope-mismatch error reported on the real external PC.
	if err := secrets.RequireServiceProvider(cfg.PasswordRef); err == nil {
		t.Fatal("expected scope-mismatch error before service provisioning")
	} else if !strings.Contains(err.Error(), "dpapi-machine") || !strings.Contains(err.Error(), "dpapi-user") {
		t.Fatalf("unexpected error shape: %v", err)
	} else {
		t.Logf("reproduced: %v", err)
	}

	// Actually initialize a real local repository with that password so
	// ReWrapToMachine's own re-authentication step (never generates a
	// replacement once a repository exists) has something real to verify.
	prov, _, err := secrets.ForRef(cfg.PasswordRef)
	if err != nil {
		t.Fatalf("provider for pilot ref: %v", err)
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
	if _, err := eng.Init(context.Background(), cfg.Repository, cfg.PasswordRef); err != nil {
		t.Fatalf("init local test repo: %v", err)
	}

	// Fix: the exact re-wrap provisionSecrets(cfg, "service") runs for
	// PasswordRef specifically (agent/cmd/stowline-agent/ops.go). Called
	// directly here, not through provisionSecrets/`secrets provision`,
	// because that wrapper's later acl.Apply(dir, acl.ServiceSecret) locks
	// the directory to SYSTEM/Administrators only -- correct in
	// production, but it would then also deny this non-elevated test
	// process's own read-back below. ACL enforcement has its own test
	// coverage (agent/internal/windows/acl); this test isolates the
	// crypto/scope-promotion behavior the wizard fix actually depends on.
	authCalls := 0
	ref, err := secrets.ReWrapToMachine(context.Background(), secrets.MigrateRequest{
		Dir:          filepath.Join(root, "secrets"),
		RepoLocation: cfg.Repository.Location,
		Authenticate: func(ctx context.Context, r domain.SecretRef) (string, error) {
			authCalls++
			p, _, err := secrets.ForRef(r)
			if err != nil {
				return "", err
			}
			e := &restic.Adapter{
				Runner:     process.NewRunner(),
				Secrets:    p,
				Connector:  storage.Connector{RclonePin: cfg.Binaries.Rclone},
				Pin:        cfg.Binaries.Restic,
				TempDir:    cfg.TempDir,
				CacheDir:   cfg.CacheDir,
				SystemRoot: os.Getenv("SystemRoot"),
				ExtraPath:  []string{filepath.Dir(cfg.Binaries.Rclone.Path)},
			}
			return e.CatConfig(ctx, cfg.Repository, r)
		},
	})
	if err != nil {
		t.Fatalf("ReWrapToMachine: %v", err)
	}
	if authCalls == 0 {
		t.Fatal("ReWrapToMachine must re-authenticate the password against the real repository, not trust it blindly")
	}
	cfg.PasswordRef = ref
	if cfg.PasswordRef.Provider != domain.SecretProviderDPAPIMachine {
		t.Fatalf("expected dpapi-machine after service provision, got %s", cfg.PasswordRef.Provider)
	}
	if err := secrets.RequireServiceProvider(cfg.PasswordRef); err != nil {
		t.Fatalf("still rejected after service provision: %v", err)
	}

	rep := preflight.Run(&cfg)
	for _, c := range rep.Checks {
		if c.Name == "service_secret_provider" && c.Status != preflight.PASS {
			t.Fatalf("service_secret_provider: %s %s", c.Status, c.Detail)
		}
		if c.Name == "repository_auth" && c.Status != preflight.PASS {
			t.Fatalf("repository_auth: %s %s", c.Status, c.Detail)
		}
	}
}

// A dpapi-user blob must be rejected outright in service mode -- the exact
// gate that caught this bug in the first place. No temp files: pure
// metadata check, no secret bytes ever touched.
func TestRequireServiceProviderRejectsDPAPIUser(t *testing.T) {
	ref := domain.SecretRef{
		Purpose:  domain.SecretResticPassword,
		Provider: domain.SecretProviderDPAPIUser,
		Locator:  `C:\Stowline\secrets\restic-password.user.dpapi`,
	}
	err := secrets.RequireServiceProvider(ref)
	if err == nil {
		t.Fatal("dpapi-user must be rejected in service mode")
	}
	if !strings.Contains(err.Error(), domain.SecretProviderDPAPIMachine) {
		t.Fatalf("error must name the required provider: %v", err)
	}
}

// Regression: dispatch()'s switch had no "secrets" case at all, even
// though cmdSecrets() was fully implemented and its own usage() text
// advertised "secrets provision --mode=pilot|service". Every invocation
// -- from the wizard's do_install(), from this test file's own earlier
// direct-CLI qualification runs, from a plain shell -- fell through to
// the default case and failed with "unknown command secrets", regardless
// of mode or filesystem state. Confirmed identically on both the
// pre-existing qualified binary and the newly built one before fixing,
// so this was never a regression from the bind()/DPAPI work -- it had
// simply never been exercised through the real CLI before.
func TestDispatchRoutesSecretsCommand(t *testing.T) {
	err := dispatch("secrets", []string{})
	if err == nil {
		t.Fatal("secrets with no subcommand must still reach cmdSecrets' own usage error")
	}
	if strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("dispatch() has no case for \"secrets\": %v", err)
	}
	if !strings.Contains(err.Error(), "secrets provision") {
		t.Fatalf("expected cmdSecrets' own usage error, got: %v", err)
	}
}
