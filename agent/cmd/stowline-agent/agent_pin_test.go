package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func waitPinSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat decision did not reach the controlled barrier")
	}
}

func pendingPinHandler(t *testing.T, gate *upgradeGate) agentPinHandler {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "stowline-agent.exe")
	if err := writeUpgradeMarker(exe, "synthetic-rollback"); err != nil {
		t.Fatal(err)
	}
	return agentPinHandler{activity: gate, exePath: exe}
}

func assertPinHealthy(t *testing.T, handler agentPinHandler) {
	t.Helper()
	if _, err := os.Stat(markerPath(handler.exePath)); !os.IsNotExist(err) {
		t.Errorf("successful heartbeat must confirm the running build before any update decision: %v", err)
	}
}

// The real local restore runner is used here: a rejected admission would become
// an asynchronous failed restore, despite its Start call initially succeeding.
func startPinTestRestore(t *testing.T, gate *upgradeGate) (error, func()) {
	t.Helper()
	admitted := make(chan error, 1)
	release := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	jobs := &restoreJobs{Run: func(context.Context, string, string, []string, func(domain.RestoreProgress)) (string, error) {
		defer close(finished)
		err := gate.beginOperation()
		admitted <- err
		if err != nil {
			return "", err
		}
		defer gate.endOperation()
		<-release
		return "", nil
	}}
	if _, err := jobs.Start("synthetic-snapshot", nil); err != nil {
		t.Fatal(err)
	}
	finish := func() {
		once.Do(func() { close(release) })
		waitPinSignal(t, finished)
	}
	t.Cleanup(finish)
	select {
	case err := <-admitted:
		return err, finish
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not reach admission")
		return nil, finish
	}
}

func TestHeartbeatNoOpPinNeverBlocksRestore(t *testing.T) {
	for _, tc := range []struct {
		name              string
		want              string
		qualified, canary bool
		expectHash        bool
	}{
		{"not permitted", "candidate", false, false, false},
		{"empty qualified pin", "", true, false, false},
		{"empty canary pin", "", false, true, false},
		{"identical qualified pin", "running", true, false, true},
		{"identical canary pin", "running", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gate upgradeGate
			handler := pendingPinHandler(t, &gate)
			hashEntered, releaseHash, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var hashRelease sync.Once
			release := func() { hashRelease.Do(func() { close(releaseHash) }) }
			t.Cleanup(release)
			var stages atomic.Int32
			handler.currentSHA = func(string) (string, error) {
				close(hashEntered)
				<-releaseHash
				return "running", nil
			}
			handler.stage = func(string, string, string, bool) bool { stages.Add(1); return false }
			go func() {
				defer close(done)
				handler.onPin(tc.want, tc.qualified, tc.canary)
			}()
			select {
			case <-hashEntered:
				if !tc.expectHash {
					t.Error("a pin without permission or a target must return before reading the agent binary")
				}
			case <-done:
				if tc.expectHash {
					t.Error("a permitted pin must compare the running hash")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("heartbeat did not reach hash comparison or return")
			}
			assertPinHealthy(t, handler)
			err, finishRestore := startPinTestRestore(t, &gate)
			if err != nil {
				t.Errorf("a no-op heartbeat must not fail a local restore while hashing: %v", err)
			}
			release()
			waitPinSignal(t, done)
			if stages.Load() != 0 {
				t.Error("a no-op heartbeat must return before update admission/staging")
			}
			finishRestore()
			if err := gate.beginOperation(); err != nil {
				t.Fatalf("no-op heartbeat left operation admission closed: %v", err)
			}
			gate.endOperation()
		})
	}
}

func TestHeartbeatPermittedUpdateRechecksWorkAfterHash(t *testing.T) {
	var gate upgradeGate
	handler := pendingPinHandler(t, &gate)
	hashEntered, releaseHash, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var hashRelease sync.Once
	release := func() { hashRelease.Do(func() { close(releaseHash) }) }
	t.Cleanup(release)
	var stages atomic.Int32
	handler.currentSHA = func(string) (string, error) {
		close(hashEntered)
		<-releaseHash
		return "running", nil
	}
	handler.stage = func(string, string, string, bool) bool { stages.Add(1); return false }
	go func() { defer close(done); handler.onPin("candidate", true, false) }()
	waitPinSignal(t, hashEntered)
	err, finishRestore := startPinTestRestore(t, &gate)
	if err != nil {
		t.Errorf("hash comparison must not reserve an upgrade before the pin differs: %v", err)
	}
	release()
	waitPinSignal(t, done)
	assertPinHealthy(t, handler)
	if stages.Load() != 0 {
		t.Error("an operation admitted during hash comparison must defer the update")
	}
	finishRestore()
}

func TestHeartbeatConfirmsHealthWhenActiveWorkDefersUpdate(t *testing.T) {
	var gate upgradeGate
	handler := pendingPinHandler(t, &gate)
	handler.currentSHA = func(string) (string, error) { return "running", nil }
	handler.stage = func(string, string, string, bool) bool {
		t.Error("active restore must defer staging")
		return false
	}
	if err, _ := startPinTestRestore(t, &gate); err != nil {
		t.Fatal(err)
	}
	handler.onPin("candidate", true, false)
	assertPinHealthy(t, handler)
}

func TestHeartbeatPermittedDifferentPinExcludesWorkOnlyWhileStaging(t *testing.T) {
	for _, canary := range []bool{false, true} {
		for _, staged := range []bool{false, true} {
			t.Run(map[bool]string{false: "qualified", true: "canary"}[canary]+map[bool]string{false: "/failed stage", true: "/restart pending"}[staged], func(t *testing.T) {
				var gate upgradeGate
				handler := pendingPinHandler(t, &gate)
				stageEntered, releaseStage, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(releaseStage) }) }
				t.Cleanup(release)
				handler.currentSHA = func(string) (string, error) { return "running", nil }
				handler.stage = func(exe, current, want string, allowed bool) bool {
					if exe != handler.exePath || current != "running" || want != "candidate" || !allowed {
						t.Error("permitted differing pin was not forwarded to staging")
					}
					close(stageEntered)
					<-releaseStage
					return staged
				}
				go func() { defer close(done); handler.onPin("candidate", !canary, canary) }()
				waitPinSignal(t, stageEntered)
				assertPinHealthy(t, handler)
				if err := gate.beginOperation(); !errors.Is(err, domain.ErrOperationInProgress) {
					t.Errorf("backup admission must stay closed across a real update: %v", err)
				}
				if err, _ := startPinTestRestore(t, &gate); !errors.Is(err, domain.ErrOperationInProgress) {
					t.Errorf("restore admission must stay closed across a real update: %v", err)
				}
				release()
				waitPinSignal(t, done)
				err := gate.beginOperation()
				if staged {
					if !errors.Is(err, domain.ErrOperationInProgress) {
						t.Errorf("successful swap must keep admission closed until restart: %v", err)
					}
				} else {
					if err != nil {
						t.Fatalf("failed stage must reopen admission: %v", err)
					}
					gate.endOperation()
				}
			})
		}
	}
}
