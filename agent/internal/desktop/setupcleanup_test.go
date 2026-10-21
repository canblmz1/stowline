package desktop

import "testing"

func TestFreshInstallerPreservesEveryExistingInstallation(t *testing.T) {
	for name, old := range map[string]OldInstall{
		"same server":           {PilotRootExists: true, ServiceExists: true, PilotJSON: []byte(`{"device_id":"existing","control_plane_url":"https://backup.example.com"}`)},
		"other server":          {PilotRootExists: true, ServiceExists: true, PilotJSON: []byte(`{"control_plane_url":"https://old.example.com"}`)},
		"service only":          {ServiceExists: true},
		"folder without config": {PilotRootExists: true},
		"corrupt config":        {PilotRootExists: true, PilotJSON: []byte("{not json")},
		"legacy config":         {PilotRootExists: true, PilotJSON: []byte(`{"device_id":"existing"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if !BlocksFreshInstall(old) {
				t.Fatal("fresh setup must preserve existing identity and recovery material")
			}
		})
	}
	if BlocksFreshInstall(OldInstall{}) {
		t.Fatal("a fresh device must remain installable")
	}
}
