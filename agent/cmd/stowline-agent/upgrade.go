package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/canblmz1/stowline/agent/internal/ports"
)

// upgradeSelfCheck runs a candidate binary's own self-check before it is
// trusted enough to become the live one. A hash match alone cannot catch a
// corrupted-but-hash-matching build pipeline bug; actually running the new
// binary once, in a throwaway subprocess, is what does.
type upgradeSelfCheck func(exePath string) error

// stageVerifiedUpgrade is the entire trust boundary of the automatic agent
// upgrade (see docs/superpowers/specs/2026-09-22-ops-and-desktop-plan.md,
// Phase 2): nothing here is installed as the live binary until its hash
// matches the server's pin AND it has passed its own self-check as a
// separate process. The previous binary is renamed aside, never deleted,
// so a bad build can always be rolled back to a binary that is known to
// have worked.
func stageVerifiedUpgrade(currentExePath string, downloaded []byte, wantSHA256 string, selfCheck upgradeSelfCheck, now time.Time) (rollbackPath string, err error) {
	got := sha256.Sum256(downloaded)
	if hex.EncodeToString(got[:]) != wantSHA256 {
		return "", fmt.Errorf("downloaded agent binary does not match the pinned SHA-256")
	}
	dir := filepath.Dir(currentExePath)
	tmp := filepath.Join(dir, "stowline-agent.exe.upgrade-staging")
	if err := os.WriteFile(tmp, downloaded, 0700); err != nil {
		return "", fmt.Errorf("write staged binary: %w", err)
	}
	if selfCheck != nil {
		if err := selfCheck(tmp); err != nil {
			_ = os.Remove(tmp)
			return "", fmt.Errorf("staged binary failed its own self-check, refusing to install: %w", err)
		}
	}
	rollbackPath = filepath.Join(dir, fmt.Sprintf("stowline-agent.exe.rollback-%d", now.Unix()))
	if err := os.Rename(currentExePath, rollbackPath); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("move current binary aside: %w", err)
	}
	if err := os.Rename(tmp, currentExePath); err != nil {
		// Put the original back before returning -- a half-completed swap
		// must never leave the machine with neither a current nor a
		// rollback binary at the live path.
		_ = os.Rename(rollbackPath, currentExePath)
		return "", fmt.Errorf("install staged binary: %w", err)
	}
	return rollbackPath, nil
}

// restoreRollback undoes a swap: the binary at rollbackPath (known to have
// worked before the upgrade) replaces whatever is at currentExePath now.
// The bad binary is renamed aside rather than deleted: Windows refuses to
// delete an executable while it is running (which it is, when the boot-loop
// guard below calls this from inside the failing binary itself), but it does
// allow renaming it.
func restoreRollback(currentExePath, rollbackPath string) error {
	failed := fmt.Sprintf("%s.failed-%d", currentExePath, time.Now().UnixNano())
	if err := os.Rename(currentExePath, failed); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("move failed binary aside: %w", err)
	}
	if err := os.Rename(rollbackPath, currentExePath); err != nil {
		_ = os.Rename(failed, currentExePath)
		return fmt.Errorf("restore rollback binary: %w", err)
	}
	return nil
}

// upgradeMarkerName records an upgrade that has been swapped in but has not
// yet proven itself with a successful heartbeat.
const upgradeMarkerName = "stowline-agent.exe.upgrade-pending"

// maxUnconfirmedBoots is how many times a freshly installed binary may start
// without ever reaching a successful heartbeat before it is presumed broken
// and the previous binary is put back. SCM's recovery policy restarts the
// service after every crash, so a binary that dies on startup would
// otherwise loop forever.
const maxUnconfirmedBoots = 3

type upgradeMarker struct {
	RollbackPath string `json:"rollback_path"`
	Boots        int    `json:"boots"`
}

func markerPath(exePath string) string {
	return filepath.Join(filepath.Dir(exePath), upgradeMarkerName)
}

// writeUpgradeMarker is called right after a successful swap, before the
// process exits to let SCM start the new binary.
func writeUpgradeMarker(exePath, rollbackPath string) error {
	b, _ := json.Marshal(upgradeMarker{RollbackPath: rollbackPath})
	return os.WriteFile(markerPath(exePath), b, 0600)
}

// guardUnconfirmedUpgrade runs once at every service start. It counts this
// boot against a pending upgrade; once the new binary has started too many
// times without confirming, it restores the previous binary and reports
// rolledBack=true so the caller exits and SCM starts the known-good one.
func guardUnconfirmedUpgrade(exePath string) (rolledBack bool, err error) {
	raw, err := os.ReadFile(markerPath(exePath))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var m upgradeMarker
	if err := json.Unmarshal(raw, &m); err != nil || m.RollbackPath == "" {
		_ = os.Remove(markerPath(exePath))
		return false, nil
	}
	m.Boots++
	if m.Boots > maxUnconfirmedBoots {
		if err := restoreRollback(exePath, m.RollbackPath); err != nil {
			return false, err
		}
		_ = os.Remove(markerPath(exePath))
		return true, nil
	}
	b, _ := json.Marshal(m)
	return false, os.WriteFile(markerPath(exePath), b, 0600)
}

// confirmUpgradeHealthy clears a pending upgrade once the new binary has
// completed a real heartbeat with the control plane.
func confirmUpgradeHealthy(exePath string) {
	_ = os.Remove(markerPath(exePath))
}

// pruneUpgradeLeftovers keeps the newest `keep` rollback copies (plus any
// the pending marker still points at) and removes stale staging and
// failed-binary files, so repeated upgrades don't fill the disk.
func pruneUpgradeLeftovers(exePath string, keep int) {
	dir := filepath.Dir(exePath)
	base := filepath.Base(exePath)
	protected := ""
	if raw, err := os.ReadFile(markerPath(exePath)); err == nil {
		var m upgradeMarker
		if json.Unmarshal(raw, &m) == nil {
			protected = filepath.Clean(m.RollbackPath)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var rollbacks []string
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(dir, name)
		switch {
		case strings.HasPrefix(name, base+".rollback-"):
			rollbacks = append(rollbacks, full)
		case strings.HasPrefix(name, base+".failed-"), name == base+".upgrade-staging":
			_ = os.Remove(full)
		}
	}
	// Names end in a unix timestamp of the same width, so a plain sort is
	// chronological.
	sort.Strings(rollbacks)
	for i := 0; i < len(rollbacks)-keep; i++ {
		if filepath.Clean(rollbacks[i]) == protected {
			continue
		}
		_ = os.Remove(rollbacks[i])
	}
}

// hashFile is the same comparison the server's own qualification pin is
// built from (SHA-256 of the whole file) -- used at startup to know this
// process's own running binary's identity, so heartbeat's agent_sha256
// can be compared against something real rather than a version string.
func hashFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// selfSHA256 is this running binary's own hash, or "" if it can't be read.
// Computed once at service start (the file can't change underneath a
// running process on Windows without a restart following it).
func selfSHA256() string {
	exePath, err := os.Executable()
	if err != nil {
		return ""
	}
	sha, err := hashFile(exePath)
	if err != nil {
		return ""
	}
	return sha
}

type upgradeClient interface {
	DownloadAgentBinary(ctx context.Context) ([]byte, error)
}

// processRunner is the same internal/adapters/process.Runner interface
// every other subprocess launch in this agent already goes through --
// no direct child-process launch call may appear outside that one
// package (enforced by internal/securityscan). Running the staged
// binary's own `version` subcommand this way, rather than with a raw
// exec call here, keeps that boundary and gets ExpectedSHA256 verification
// (the same mechanism already used for the pinned restic/rclone binaries)
// for free.
type processRunner interface {
	Run(ctx context.Context, spec ports.Spec) (*ports.ProcessResult, error)
}

// runAgentUpgrade is the one check-and-act cycle wired to Executor.OnAgentPin
// (see agent/internal/control/execute.go). It does nothing unless the
// server's pin is both qualified and different from this process's own
// running binary hash. On a successful swap it returns the rollback path;
// the caller's job is then to hand control back to SCM (see EnsureRestartOnFailure
// and the os.Exit call site in main.go) so the already-swapped binary on
// disk becomes the one actually running. Any error here is the caller's
// to log -- the current binary is untouched and keeps running; the next
// heartbeat cycle tries again.
func runAgentUpgrade(ctx context.Context, client upgradeClient, runner processRunner, currentExePath, currentSHA256, wantSHA256 string, qualified bool) (rollbackPath string, err error) {
	if !qualified || wantSHA256 == "" || wantSHA256 == currentSHA256 {
		return "", nil
	}
	data, err := client.DownloadAgentBinary(ctx)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	selfCheck := func(stagedPath string) error {
		res, err := runner.Run(ctx, ports.Spec{
			Name:           "stowline-agent-self-check",
			Executable:     stagedPath,
			Args:           []string{"version"},
			ExpectedSHA256: wantSHA256,
			Timeout:        15 * time.Second,
			ForceTerminate: true,
		})
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("self-check exited %d: %s", res.ExitCode, res.Stderr)
		}
		return nil
	}
	return stageVerifiedUpgrade(currentExePath, data, wantSHA256, selfCheck, time.Now())
}
