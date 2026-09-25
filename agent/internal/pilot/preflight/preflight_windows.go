//go:build windows

package preflight

import (
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func vssCheck() Check {
	m, err := mgr.Connect()
	if err != nil {
		return Check{"vss_service", BLOCKED, "SCM unavailable: " + err.Error()}
	}
	defer m.Disconnect()
	s, err := m.OpenService("VSS")
	if err != nil {
		return Check{"vss_service", BLOCKED, err.Error()}
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return Check{"vss_service", BLOCKED, err.Error()}
	}
	switch st.State {
	case svc.Stopped:
		return Check{"vss_service", PASS, "Stopped (manual start is normal)"}
	case svc.Running:
		return Check{"vss_service", PASS, "Running"}
	default:
		return Check{"vss_service", WARNING, "unexpected state"}
	}
}

func volumeFree(path string) (uint64, error) {
	vol := filepath.VolumeName(path)
	if vol == "" {
		vol = `C:`
	}
	if vol[len(vol)-1] != '\\' {
		vol += `\`
	}
	p, err := windows.UTF16PtrFromString(vol)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	err = windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree)
	return free, err
}
