package desktop

import (
	"encoding/json"
	"strings"
)

// OldInstall describes what an earlier Stowline install left on this PC.
type OldInstall struct {
	PilotRootExists bool   // C:\Stowline exists
	ServiceExists   bool   // the StowlineBackup Windows service is registered
	PilotJSON       []byte // C:\Stowline\config\pilot.json, nil if absent
}

// NeedsCleanup decides whether an earlier install must be removed before
// setup runs. It is removed when it belongs to another server (the old
// install pointing at a previous server) or is broken/unreadable. An install
// already enrolled against this package's own server is kept: running the
// installer again there is a repair, and the wizard reuses that enrollment.
func NeedsCleanup(old OldInstall, packagedControlURL string) bool {
	if !old.PilotRootExists && !old.ServiceExists {
		return false
	}
	if old.PilotJSON == nil {
		return true // leftovers without a readable config: start clean
	}
	var cfg struct {
		ControlPlaneURL string `json:"control_plane_url"`
	}
	raw := old.PilotJSON
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		raw = raw[3:]
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return true
	}
	have := strings.TrimRight(strings.TrimSpace(cfg.ControlPlaneURL), "/")
	want := strings.TrimRight(strings.TrimSpace(packagedControlURL), "/")
	if have == "" {
		return true
	}
	if want == "" {
		// A generic package (the server is typed in the wizard) cannot tell
		// whose install this is: keep a working one and let setup ask.
		return false
	}
	return !strings.EqualFold(have, want)
}
