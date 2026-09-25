package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyIndependentAndRefusePilot(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "config"), []byte(`{"id":"lab"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmdCopy([]string{"--src", src, "--dst", filepath.Join(dst, "copy")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "copy", "stowline-recovery-manifest.json")); err != nil {
		t.Fatal(err)
	}
	if err := rejectPilotLive(`C:\Stowline\Repos\local`); err == nil {
		t.Fatal("must refuse live pilot repo")
	}
}

// Reproduces a live finding: copying a REST-gateway repository's flat
// data/<id> layout straight into a local directory produced a tree
// restic's own local backend could not read back -- restore-to-staging
// failed on a real, present blob purely because of the layout mismatch,
// and this restic version has no -o local.layout override to paper over
// it ("option local.layout is not known"). copyDir must reshard data/
// blobs into data/<id[:2]>/<id> during the copy so the result is
// directly usable, regardless of how flat the source was.
func TestCopyReshardsFlatDataBlobsForTheLocalBackend(t *testing.T) {
	src := t.TempDir()
	id := strings.Repeat("6", 64)
	if err := os.MkdirAll(filepath.Join(src, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "data", id), []byte("opaque-blob"), 0600); err != nil {
		t.Fatal(err)
	}
	// index/ and friends are already flat on the real gateway too, and
	// restic's local backend expects them flat -- must NOT be resharded.
	if err := os.WriteFile(filepath.Join(src, "config"), []byte(`{"id":"lab"}`), 0600); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := copyDir(src, filepath.Join(dst, "copy")); err != nil {
		t.Fatal(err)
	}
	sharded := filepath.Join(dst, "copy", "data", id[:2], id)
	if _, err := os.Stat(sharded); err != nil {
		t.Fatalf("blob must land at the sharded path restic's local backend expects: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "copy", "data", id)); err == nil {
		t.Fatal("blob must not also be left flat under data/")
	}
	if _, err := os.Stat(filepath.Join(dst, "copy", "config")); err != nil {
		t.Fatalf("non-data files must be copied unchanged: %v", err)
	}
}

func TestForgetApplyFriction(t *testing.T) {
	err := cmdForget([]string{"--repo", t.TempDir(), "--restic", os.Args[0], "--sha256", "00"}, false)
	if err == nil {
		t.Fatal("apply without friction flags must fail (pin or flags)")
	}
}

func makeFakeRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(`{"id":"fake"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"data", "index", "keys", "snapshots"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0700); err != nil {
			t.Fatal(err)
		}
	}
}

// Reproduces the real storage layout: repositories live several
// directories deep (IT-QUALIFICATION/pc01--<device>/<generation-id>/),
// and an unrelated directory that merely has a "data" subfolder (e.g. an
// ordinary Documents\data folder) must never be mistaken for one.
func TestListRepositoriesFindsNestedRepoAndSkipsLookalikes(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "IT-QUALIFICATION", "pc01--abc12345", "gen-1")
	makeFakeRepo(t, repoDir)
	lookalike := filepath.Join(root, "unrelated", "data")
	if err := os.MkdirAll(lookalike, 0700); err != nil {
		t.Fatal(err)
	}
	if err := cmdListRepositories([]string{"--root", root}); err != nil {
		t.Fatal(err)
	}
	if !looksLikeResticRepo(repoDir) {
		t.Fatal("a directory with config + data/index/keys/snapshots must be recognized")
	}
	if looksLikeResticRepo(filepath.Join(root, "unrelated")) {
		t.Fatal("a directory that merely contains a folder named data must not be recognized as a repo")
	}
}

func TestListRepositoriesDoesNotDescendIntoAFoundRepo(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	makeFakeRepo(t, repoDir)
	// A restic repo's own data/ directory is full of pack-file-looking
	// entries; if the walk descended into it, it could misfire on those
	// too and report the same repository multiple times.
	if err := os.WriteFile(filepath.Join(repoDir, "data", "aa"), []byte("pack"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmdListRepositories([]string{"--root", root}); err != nil {
		t.Fatal(err)
	}
}

func TestRejectLiveStagingTargetBlocksDriveRootAndLiveNames(t *testing.T) {
	cases := []string{`C:\`, `C:\Users\op\Documents`, `C:\Users\op\Desktop\recovery`, `C:\ProgramData\x`}
	for _, c := range cases {
		if err := rejectLiveStagingTarget(c); err == nil {
			t.Fatalf("must refuse staging target %q", c)
		}
	}
}

func TestRejectLiveStagingTargetAllowsFreshEmptyDir(t *testing.T) {
	staging := filepath.Join(t.TempDir(), "recovery-staging")
	if err := rejectLiveStagingTarget(staging); err != nil {
		t.Fatalf("a fresh, not-yet-created staging dir must be allowed: %v", err)
	}
}

func TestRejectLiveStagingTargetRefusesNonEmptyDir(t *testing.T) {
	staging := t.TempDir()
	if err := os.WriteFile(filepath.Join(staging, "pre-existing.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := rejectLiveStagingTarget(staging); err == nil {
		t.Fatal("must refuse a staging target that already has files in it")
	}
}

func TestRestoreToStagingRequiresAllFlags(t *testing.T) {
	err := cmdRestoreToStaging([]string{"--repo", t.TempDir()})
	if err == nil {
		t.Fatal("must require --restic --sha256 --snapshot --staging")
	}
}

func TestRestoreToStagingRefusesLiveStagingBeforeTouchingRepo(t *testing.T) {
	err := cmdRestoreToStaging([]string{
		"--repo", t.TempDir(), "--restic", os.Args[0], "--sha256", "00",
		"--snapshot", "deadbeef", "--staging", `C:\Users\op\Documents\recovered`,
	})
	if err == nil || !strings.Contains(err.Error(), "live data location") {
		t.Fatalf("must refuse the live-looking staging target: %v", err)
	}
}

func TestResticEnvPreservesSystemVariables(t *testing.T) {
	t.Setenv("RESTIC_PASSWORD", "test-pw-value")
	t.Setenv("SystemRoot", `C:\Windows`)
	t.Setenv("Path", `C:\some\path`)

	env := resticEnv()

	get := func(key string) (string, bool) {
		for _, kv := range env {
			if len(kv) > len(key) && strings.EqualFold(kv[:len(key)], key) && kv[len(key)] == '=' {
				return kv[len(key)+1:], true
			}
		}
		return "", false
	}

	if v, ok := get("RESTIC_PASSWORD"); !ok || v != "test-pw-value" {
		t.Fatalf("RESTIC_PASSWORD not set correctly: %q, ok=%v", v, ok)
	}
	if _, ok := get("SystemRoot"); !ok {
		t.Fatal("SystemRoot must be inherited: without it, restic's TLS/DNS resolution can fail unpredictably on Windows")
	}
	if _, ok := get("Path"); !ok {
		t.Fatal("Path must be inherited so the restic subprocess can resolve dependent DLLs")
	}
}
