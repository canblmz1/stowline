//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	winsvc "github.com/canblmz1/stowline/agent/internal/windows/service"
	"golang.org/x/sys/windows/svc"
)

// Exercise the same Startup callback and Execute handler used by cmdService;
// no second model of startup ordering and no real host service configuration.
func TestServiceLifecycleRollsBackBeforeFalliblePreparation(t *testing.T) {
	_, exe, rollback := setupSwapped(t)
	old, err := os.ReadFile(rollback)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxUnconfirmedBoots; i++ {
		if _, err := guardUnconfirmedUpgrade(exe); err != nil {
			t.Fatal(err)
		}
	}
	changes := make(chan svc.Status) // guard cannot run before handler registration/status
	prepared := false
	startup := serviceStartup(exe, true, func(bool) (winsvc.Runner, error) {
		prepared = true
		return nil, errors.New("config/engine unavailable")
	})
	host := &winsvc.Service{Interactive: true, Startup: startup}
	done := make(chan uint32, 1)
	go func() { _, code := host.Execute(nil, nil, changes); done <- code }()
	// Execute is blocked announcing StartPending; the live file is still B.
	if got, _ := os.ReadFile(exe); string(got) == string(old) {
		t.Fatal("rollback before SCM startup")
	}
	select {
	case status := <-changes:
		if status.State != svc.StartPending {
			t.Fatal(status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not connect")
	}
	select {
	case code := <-done:
		if code == 0 {
			t.Fatal("rollback was a normal service stop")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rollback did not finish")
	}
	if prepared {
		t.Fatal("config/engine or normal work prepared during rollback")
	}
	if got, _ := os.ReadFile(exe); string(got) != string(old) {
		t.Fatal("rollback did not restore A bytes")
	}
	if _, err := os.Stat(markerPath(exe)); !os.IsNotExist(err) {
		t.Fatal("marker not cleared")
	}
}

func TestServiceLifecycleCountsBootBeforePreparationFailure(t *testing.T) {
	_, exe, _ := setupSwapped(t)
	startup := serviceStartup(exe, true, func(mode bool) (winsvc.Runner, error) {
		if !mode {
			t.Error("lost service mode")
		}
		raw, err := os.ReadFile(markerPath(exe))
		if err != nil {
			t.Fatal(err)
		}
		var marker upgradeMarker
		if json.Unmarshal(raw, &marker) != nil || marker.Boots != 1 {
			t.Fatal("guard did not precede preparation")
		}
		return nil, errors.New("config/engine failure")
	})
	host := &winsvc.Service{Interactive: true, Startup: startup}
	if _, code := host.Execute(nil, nil, make(chan svc.Status, 8)); code == 0 {
		t.Fatal("initialization failure hidden")
	}
}
