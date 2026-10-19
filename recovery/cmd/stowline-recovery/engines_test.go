package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This exercises a fresh recovery workstation, rather than a still-enrolled
// agent. It needs no control plane, original machine state or storage secrets:
// only an independent repository copy and its separately retained password.
// The required CI job sets STOWLINE_REQUIRE_REAL_ENGINES=1 so missing binaries
// cannot silently turn this verification into a passing skip.
func TestIndependentRecoveryWithRealEngines(t *testing.T) {
	engine := func(name string) string {
		t.Helper()
		path, err := exec.LookPath(name)
		if err != nil {
			if os.Getenv("STOWLINE_REQUIRE_REAL_ENGINES") == "1" {
				t.Fatalf("required real engine %s is missing: %v", name, err)
			}
			t.Skipf("real %s is unavailable; required CI must set STOWLINE_REQUIRE_REAL_ENGINES=1", name)
		}
		return path
	}
	restic, rclone := engine("restic"), engine("rclone")
	root := t.TempDir()
	endpoint := filepath.Join(root, "endpoint")
	originalRepo := filepath.Join(root, "original-repository")
	storageCopy := filepath.Join(root, "storage", "custom-root", "Finance", "pc--persistent-id", "generation-2")
	download := filepath.Join(root, "workstation", "download")
	independentRepo := filepath.Join(root, "workstation", "local-repository")
	staging := filepath.Join(root, "workstation", "staging")
	retainedPassword := "synthetic-independent-recovery-password"
	t.Setenv("RESTIC_PASSWORD", retainedPassword)
	t.Setenv("RESTIC_PASSWORD_FILE", "")
	t.Setenv("RESTIC_PASSWORD_COMMAND", "")
	t.Setenv("RESTIC_CACHE_DIR", filepath.Join(root, "cache"))
	t.Setenv("RCLONE_CONFIG", os.DevNull)

	want := map[string][]byte{
		"invoice.txt": []byte("synthetic invoice\r\namount=42\r\n"),
		"binary.bin":  {0, 1, 2, 0xff, 0, 13, 10},
		"empty.txt":   {},
	}
	for name, content := range want {
		path := filepath.Join(endpoint, "nested", name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(binary string, args ...string) {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Env = resticEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", filepath.Base(binary), args, err, out)
		}
	}
	run(restic, "--repo", originalRepo, "init")
	run(restic, "--repo", originalRepo, "backup", endpoint, "--host", "synthetic-endpoint")
	run(restic, "--repo", originalRepo, "check", "--read-data")

	// A gateway repository has flat data/<id> packs. Emulate that storage
	// layout from a genuine restic repository, then actually download it
	// through rclone's local backend into a separate workstation directory.
	if err := copyDir(originalRepo, storageCopy); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(storageCopy, "data")
	err := filepath.WalkDir(data, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if resticBlobID.MatchString(entry.Name()) && filepath.Dir(path) != data {
			return os.Rename(path, filepath.Join(data, entry.Name()))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	run(rclone, "--config", os.DevNull, "copy", storageCopy, download)
	if err := cmdCopy([]string{"--src", download, "--dst", independentRepo}); err != nil {
		t.Fatal(err)
	}
	// Simulate loss of the endpoint and its original local repository before
	// opening the downloaded copy. All paths belong to this test's TempDir.
	for _, path := range []string{endpoint, originalRepo, storageCopy, download} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	engineBytes, err := os.ReadFile(restic)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(engineBytes)
	base := []string{"--repo", independentRepo, "--restic", restic, "--sha256", hex.EncodeToString(digest[:])}
	if err := cmdVerify(base); err != nil {
		t.Fatalf("independent repository check: %v", err)
	}
	if err := cmdListSnapshots(base); err != nil {
		t.Fatalf("independent snapshot listing: %v", err)
	}
	// Restore the selected data subtree, without applying original machine
	// ancestor ACLs (e.g. C:\Users) to the fresh workstation's staging parents.
	// restic's own snapshotID:subfolder selector is supported by the CLI.
	snapshotPath := filepath.ToSlash(endpoint)
	if volume := filepath.VolumeName(endpoint); volume != "" {
		snapshotPath = "/" + strings.TrimSuffix(volume, ":") + filepath.ToSlash(endpoint[len(volume):])
	}
	if err := cmdRestoreToStaging(append(base, "--snapshot", "latest:"+snapshotPath, "--staging", staging)); err != nil {
		t.Fatalf("independent restore: %v", err)
	}
	seen := map[string]bool{}
	err = filepath.WalkDir(staging, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() == "stowline-recovery-restore-manifest.json" {
			return err
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		expected, ok := want[entry.Name()]
		if !ok || seen[entry.Name()] || !bytes.Equal(got, expected) {
			t.Errorf("unexpected, duplicate or changed restored file: %s", path)
		}
		seen[entry.Name()] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(want) {
		t.Fatalf("restored %d files, want %d", len(seen), len(want))
	}
	badPin := append([]string(nil), base...)
	badPin[len(badPin)-1] = strings.Repeat("0", 64)
	if err := cmdVerify(badPin); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("wrong restic executable hash must be rejected: %v", err)
	}
	t.Setenv("RESTIC_PASSWORD", "wrong-password")
	if err := cmdVerify(base); err == nil {
		t.Fatal("a repository copy without the correct off-device key must not pass verification")
	}
}
