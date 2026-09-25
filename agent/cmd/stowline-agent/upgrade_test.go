package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/canblmz1/stowline/agent/internal/ports"
)

type fakeUpgradeClient struct {
	data  []byte
	err   error
	calls int
}

func (f *fakeUpgradeClient) DownloadAgentBinary(ctx context.Context) ([]byte, error) {
	f.calls++
	return f.data, f.err
}

// fakeProcessRunner stands in for internal/adapters/process.Runner in
// tests -- runAgentUpgrade must never call exec.Command directly (enforced
// by internal/securityscan), so its self-check goes through this
// interface instead of a real subprocess.
type fakeProcessRunner struct {
	exitCode    int
	err         error
	sawExpected string // the ExpectedSHA256 the caller passed
	calls       int
}

func (f *fakeProcessRunner) Run(ctx context.Context, spec ports.Spec) (*ports.ProcessResult, error) {
	f.calls++
	f.sawExpected = spec.ExpectedSHA256
	if f.err != nil {
		return nil, f.err
	}
	return &ports.ProcessResult{ExitCode: f.exitCode}, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestStageVerifiedUpgradeRefusesAHashMismatchWithoutTouchingAnything(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "stowline-agent.exe")
	if err := os.WriteFile(exe, []byte("old-binary"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := stageVerifiedUpgrade(exe, []byte("new-bytes"), sha256Hex([]byte("something-else")), nil, time.Now())
	if err == nil {
		t.Fatal("expected a hash mismatch error")
	}
	got, err := os.ReadFile(exe)
	if err != nil || string(got) != "old-binary" {
		t.Fatalf("the original binary must be untouched on a hash mismatch: %v %s", err, got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected no staged/rollback files left behind, got %+v", entries)
	}
}

func TestStageVerifiedUpgradeRefusesWhenTheStagedBinaryFailsItsOwnSelfCheck(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "stowline-agent.exe")
	if err := os.WriteFile(exe, []byte("old-binary"), 0700); err != nil {
		t.Fatal(err)
	}
	newBytes := []byte("new-binary-that-does-not-actually-run")
	failingSelfCheck := func(path string) error { return errors.New("exit status 1") }
	_, err := stageVerifiedUpgrade(exe, newBytes, sha256Hex(newBytes), failingSelfCheck, time.Now())
	if err == nil {
		t.Fatal("expected the self-check failure to propagate")
	}
	got, err := os.ReadFile(exe)
	if err != nil || string(got) != "old-binary" {
		t.Fatalf("the original binary must still be live after a failed self-check: %v %s", err, got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("the failed staging file must be cleaned up, got %+v", entries)
	}
}

func TestStageVerifiedUpgradeSwapsAtomicallyAndKeepsARollbackCopy(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "stowline-agent.exe")
	if err := os.WriteFile(exe, []byte("old-binary"), 0700); err != nil {
		t.Fatal(err)
	}
	newBytes := []byte("new-binary-that-passes-its-self-check")
	var checkedPath string
	passingSelfCheck := func(path string) error { checkedPath = path; return nil }
	rollback, err := stageVerifiedUpgrade(exe, newBytes, sha256Hex(newBytes), passingSelfCheck, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if checkedPath == "" || checkedPath == exe {
		t.Fatalf("self-check must run against the staged copy, not the live path yet: %s", checkedPath)
	}
	live, err := os.ReadFile(exe)
	if err != nil || string(live) != string(newBytes) {
		t.Fatalf("the live path must now hold the new binary: %v %s", err, live)
	}
	rolledBack, err := os.ReadFile(rollback)
	if err != nil || string(rolledBack) != "old-binary" {
		t.Fatalf("the rollback file must hold the exact previous binary: %v %s", err, rolledBack)
	}
}

func TestRestoreRollbackPutsThePreviousBinaryBackInPlace(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "stowline-agent.exe")
	rollback := filepath.Join(dir, "stowline-agent.exe.rollback-1700000000")
	if err := os.WriteFile(exe, []byte("bad-new-binary"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollback, []byte("known-good-binary"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := restoreRollback(exe, rollback); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(exe)
	if err != nil || string(got) != "known-good-binary" {
		t.Fatalf("expected the rollback binary restored to the live path: %v %s", err, got)
	}
	if _, err := os.Stat(rollback); !os.IsNotExist(err) {
		t.Fatalf("rollback file should have been consumed by the restore, err=%v", err)
	}
}

func TestRunAgentUpgradeIsANoOpWhenUnqualified(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "stowline-agent.exe")
	os.WriteFile(exe, []byte("current"), 0700)
	client := &fakeUpgradeClient{data: []byte("new")}
	runner := &fakeProcessRunner{}
	_, err := runAgentUpgrade(context.Background(), client, runner, exe, "aaa", "bbb", false)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 0 {
		t.Fatalf("must never download an unqualified pin, got %d calls", client.calls)
	}
}

func TestRunAgentUpgradeIsANoOpWhenAlreadyOnThePinnedSHA(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "stowline-agent.exe")
	os.WriteFile(exe, []byte("current"), 0700)
	client := &fakeUpgradeClient{data: []byte("new")}
	runner := &fakeProcessRunner{}
	_, err := runAgentUpgrade(context.Background(), client, runner, exe, "same-sha", "same-sha", true)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 0 {
		t.Fatalf("must not download when already running the pinned build, got %d calls", client.calls)
	}
}

func TestRunAgentUpgradeDownloadsVerifiesAndSwapsOnAMismatch(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "stowline-agent.exe")
	os.WriteFile(exe, []byte("current"), 0700)
	newBytes := []byte("new-qualified-binary")
	client := &fakeUpgradeClient{data: newBytes}
	runner := &fakeProcessRunner{exitCode: 0}
	wantSHA := sha256Hex(newBytes)
	rollback, err := runAgentUpgrade(context.Background(), client, runner, exe, "old-sha", wantSHA, true)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 1 {
		t.Fatalf("expected exactly one download, got %d", client.calls)
	}
	if runner.calls != 1 || runner.sawExpected != wantSHA {
		t.Fatalf("expected the self-check to run once with ExpectedSHA256 set, got calls=%d expected=%s", runner.calls, runner.sawExpected)
	}
	live, _ := os.ReadFile(exe)
	if string(live) != string(newBytes) {
		t.Fatalf("expected the new binary installed, got %q", live)
	}
	rolledBack, _ := os.ReadFile(rollback)
	if string(rolledBack) != "current" {
		t.Fatalf("expected the previous binary preserved as rollback, got %q", rolledBack)
	}
}

func TestRunAgentUpgradeRefusesWhenTheSelfCheckExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "stowline-agent.exe")
	os.WriteFile(exe, []byte("current"), 0700)
	newBytes := []byte("new-binary-that-crashes-immediately")
	client := &fakeUpgradeClient{data: newBytes}
	runner := &fakeProcessRunner{exitCode: 1}
	_, err := runAgentUpgrade(context.Background(), client, runner, exe, "old-sha", sha256Hex(newBytes), true)
	if err == nil {
		t.Fatal("expected a non-zero self-check exit code to refuse the install")
	}
	live, _ := os.ReadFile(exe)
	if string(live) != "current" {
		t.Fatalf("current binary must still be live after a failed self-check, got %q", live)
	}
}

func TestRunAgentUpgradePropagatesADownloadFailureWithoutTouchingTheCurrentBinary(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "stowline-agent.exe")
	os.WriteFile(exe, []byte("current"), 0700)
	client := &fakeUpgradeClient{err: errors.New("network blip")}
	runner := &fakeProcessRunner{}
	_, err := runAgentUpgrade(context.Background(), client, runner, exe, "old-sha", "new-sha", true)
	if err == nil {
		t.Fatal("expected the download failure to propagate")
	}
	live, _ := os.ReadFile(exe)
	if string(live) != "current" {
		t.Fatalf("current binary must be untouched on a download failure, got %q", live)
	}
}

func setupSwapped(t *testing.T) (dir, exe, rollback string) {
	t.Helper()
	dir = t.TempDir()
	exe = filepath.Join(dir, "stowline-agent.exe")
	rollback = filepath.Join(dir, "stowline-agent.exe.rollback-1700000000")
	os.WriteFile(exe, []byte("new-binary"), 0700)
	os.WriteFile(rollback, []byte("known-good"), 0700)
	if err := writeUpgradeMarker(exe, rollback); err != nil {
		t.Fatal(err)
	}
	return dir, exe, rollback
}

func TestGuardLetsANewBinaryBootUpToTheLimit(t *testing.T) {
	_, exe, _ := setupSwapped(t)
	for i := 1; i <= maxUnconfirmedBoots; i++ {
		rolled, err := guardUnconfirmedUpgrade(exe)
		if err != nil || rolled {
			t.Fatalf("boot %d: rolled=%v err=%v", i, rolled, err)
		}
	}
	if got, _ := os.ReadFile(exe); string(got) != "new-binary" {
		t.Fatalf("new binary must still be live within the limit, got %q", got)
	}
}

func TestGuardRollsBackABinaryThatNeverConfirms(t *testing.T) {
	_, exe, _ := setupSwapped(t)
	var rolled bool
	for i := 0; i <= maxUnconfirmedBoots; i++ {
		var err error
		rolled, err = guardUnconfirmedUpgrade(exe)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !rolled {
		t.Fatal("expected a rollback after too many unconfirmed boots")
	}
	if got, _ := os.ReadFile(exe); string(got) != "known-good" {
		t.Fatalf("expected the known-good binary restored, got %q", got)
	}
	if _, err := os.Stat(markerPath(exe)); !os.IsNotExist(err) {
		t.Fatal("marker must be cleared after a rollback")
	}
}

func TestConfirmedUpgradeIsNeverRolledBack(t *testing.T) {
	_, exe, _ := setupSwapped(t)
	guardUnconfirmedUpgrade(exe)
	confirmUpgradeHealthy(exe)
	for i := 0; i <= maxUnconfirmedBoots+2; i++ {
		if rolled, err := guardUnconfirmedUpgrade(exe); rolled || err != nil {
			t.Fatalf("a confirmed upgrade must never roll back: rolled=%v err=%v", rolled, err)
		}
	}
	if got, _ := os.ReadFile(exe); string(got) != "new-binary" {
		t.Fatalf("got %q", got)
	}
}

func TestGuardIsANoOpWithoutAPendingUpgrade(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "stowline-agent.exe")
	os.WriteFile(exe, []byte("current"), 0700)
	if rolled, err := guardUnconfirmedUpgrade(exe); rolled || err != nil {
		t.Fatalf("rolled=%v err=%v", rolled, err)
	}
}

func TestPruneKeepsTheNewestRollbacksAndTheOneStillPending(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "stowline-agent.exe")
	os.WriteFile(exe, []byte("current"), 0700)
	names := []string{"1700000001", "1700000002", "1700000003", "1700000004"}
	for _, n := range names {
		os.WriteFile(filepath.Join(dir, "stowline-agent.exe.rollback-"+n), []byte(n), 0700)
	}
	os.WriteFile(filepath.Join(dir, "stowline-agent.exe.upgrade-staging"), []byte("x"), 0700)
	os.WriteFile(filepath.Join(dir, "stowline-agent.exe.failed-123"), []byte("x"), 0700)
	writeUpgradeMarker(exe, filepath.Join(dir, "stowline-agent.exe.rollback-1700000001"))

	pruneUpgradeLeftovers(exe, 2)

	exists := func(n string) bool { _, err := os.Stat(filepath.Join(dir, n)); return err == nil }
	if !exists("stowline-agent.exe.rollback-1700000001") {
		t.Fatal("the rollback a pending upgrade still depends on must survive")
	}
	if exists("stowline-agent.exe.rollback-1700000002") {
		t.Fatal("an old, unreferenced rollback should be pruned")
	}
	if !exists("stowline-agent.exe.rollback-1700000003") || !exists("stowline-agent.exe.rollback-1700000004") {
		t.Fatal("the newest rollbacks must be kept")
	}
	if exists("stowline-agent.exe.upgrade-staging") || exists("stowline-agent.exe.failed-123") {
		t.Fatal("staging and failed leftovers should be removed")
	}
	if !exists("stowline-agent.exe") {
		t.Fatal("the live binary must never be touched")
	}
}
