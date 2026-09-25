//go:build windows

package identity

import "golang.org/x/sys/windows"

// Elevated reports whether the current process token has the elevation flag.
func Elevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}
