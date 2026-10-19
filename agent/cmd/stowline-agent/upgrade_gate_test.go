package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func TestUpgradeDefersWhileBackupOrRestoreIsActive(t *testing.T) {
	for _, name := range []string{"backup", "restore", "qualification backup"} {
		t.Run(name, func(t *testing.T) {
			var gate upgradeGate
			if err := gate.beginOperation(); err != nil {
				t.Fatal(err)
			}
			if gate.beginUpgrade() {
				t.Fatal("must not stage or restart while the operation is active")
			}
			gate.endOperation()
			if !gate.beginUpgrade() {
				t.Fatal("must allow an upgrade after the operation finishes")
			}
			if err := gate.beginOperation(); !errors.Is(err, domain.ErrOperationInProgress) {
				t.Fatalf("must not admit a new operation during download/swap/restart: %v", err)
			}
			gate.cancelUpgrade()
			if err := gate.beginOperation(); err != nil {
				t.Fatalf("a failed/no-op upgrade must reopen admission: %v", err)
			}
			gate.endOperation()
		})
	}
}

func TestOperationAdmissionAndUpgradeCannotRace(t *testing.T) {
	for i := 0; i < 100; i++ {
		var gate upgradeGate
		var running, upgrading atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			if err := gate.beginOperation(); err == nil {
				running.Store(1)
				if upgrading.Load() != 0 {
					t.Error("operation overlapped an upgrade")
				}
				running.Store(0)
				gate.endOperation()
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			if gate.beginUpgrade() {
				upgrading.Store(1)
				if running.Load() != 0 {
					t.Error("upgrade overlapped an operation")
				}
				upgrading.Store(0)
				gate.cancelUpgrade()
			}
		}()
		close(start)
		wg.Wait()
	}
}
