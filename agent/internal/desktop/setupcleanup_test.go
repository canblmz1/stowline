package desktop

import "testing"

const current = "https://backup.example.com"

func TestAFreshPCNeedsNoCleanup(t *testing.T) {
	if NeedsCleanup(OldInstall{}, current) {
		t.Fatal("nothing installed, nothing to remove")
	}
}

func TestAnOldRailwayInstallIsRemoved(t *testing.T) {
	old := OldInstall{PilotRootExists: true, ServiceExists: true, PilotJSON: []byte(`{"control_plane_url":"https://old-control.example.net"}`)}
	if !NeedsCleanup(old, current) {
		t.Fatal("an install pointing at another server must be removed")
	}
}

func TestAnInstallOnThisSameServerIsKeptForRepair(t *testing.T) {
	old := OldInstall{PilotRootExists: true, ServiceExists: true, PilotJSON: []byte("\xEF\xBB\xBF" + `{"control_plane_url":"https://backup.example.com/"}`)}
	if NeedsCleanup(old, current) {
		t.Fatal("re-running the installer on an already-migrated PC is a repair, not a wipe")
	}
}

func TestBrokenLeftoversAreRemoved(t *testing.T) {
	for name, old := range map[string]OldInstall{
		"service only":      {ServiceExists: true},
		"folder, no config": {PilotRootExists: true},
		"corrupt config":    {PilotRootExists: true, PilotJSON: []byte("{not json")},
		"config, no server": {PilotRootExists: true, PilotJSON: []byte(`{"device_id":"x"}`)},
	} {
		if !NeedsCleanup(old, current) {
			t.Fatalf("%s: must be cleaned", name)
		}
	}
}

func TestAGenericPackageKeepsAWorkingInstallAndAsks(t *testing.T) {
	old := OldInstall{PilotRootExists: true, ServiceExists: true, PilotJSON: []byte(`{"control_plane_url":"https://backup.example.org"}`)}
	if NeedsCleanup(old, "") {
		t.Fatal("a generic package must not silently remove a working install")
	}
	if !NeedsCleanup(OldInstall{PilotRootExists: true, PilotJSON: []byte(`{}`)}, "") {
		t.Fatal("an install with no server in its config is broken leftovers: remove it")
	}
}
