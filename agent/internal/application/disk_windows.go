//go:build windows

package application

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func volumeFreeBytes(path string) (uint64, error) {
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
