//go:build !windows

package identity

import "fmt"

func ProcessUserSID() (string, error) {
	return "", fmt.Errorf("process token SID is Windows-only")
}

func IsLocalSystem() (bool, error) { return false, nil }

func IsAdministrator() (bool, error) { return false, nil }

func Describe() (string, error) {
	return Format("", false, false), nil
}

func Format(sid string, localSystem, elevated bool) string {
	if localSystem {
		return "LocalSystem (S-1-5-18)"
	}
	if elevated {
		return "Interactive administrator (" + sid + ")"
	}
	if sid == "" {
		return "non-windows"
	}
	return "Interactive user (" + sid + ")"
}
