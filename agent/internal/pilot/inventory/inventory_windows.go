//go:build windows

package inventory

import (
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/canblmz1/stowline/agent/internal/windows/identity"
)

func fillPlatform(r *Report) {
	r.Elevated = identity.Elevated()
	r.Volumes = []Volume{volumeOf(`C:\`)}
	r.VSSService = vssState()
}

func volumeOf(root string) Volume {
	v := Volume{Letter: string(root[0]), FileSystem: "unknown"}
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return v
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err == nil {
		v.SizeGB = float64(total) / (1024 * 1024 * 1024)
		v.FreeGB = float64(free) / (1024 * 1024 * 1024)
	}
	var fsName [64]uint16
	if err := windows.GetVolumeInformation(p, nil, 0, nil, nil, nil, &fsName[0], uint32(len(fsName))); err == nil {
		v.FileSystem = windows.UTF16ToString(fsName[:])
	}
	return v
}

func vssState() string {
	m, err := mgr.Connect()
	if err != nil {
		return "scm_unavailable:" + err.Error()
	}
	defer m.Disconnect()
	s, err := m.OpenService("VSS")
	if err != nil {
		return "open_failed:" + err.Error()
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return "query_failed:" + err.Error()
	}
	switch st.State {
	case svc.Stopped:
		return "Stopped (manual start is normal)"
	case svc.Running:
		return "Running"
	default:
		return fmt.Sprintf("state=%d", st.State)
	}
}
