//go:build windows

package identity

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// ProcessUserSID returns the current process token user SID (not the thread impersonation token).
func ProcessUserSID() (string, error) {
	tok := windows.GetCurrentProcessToken()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("GetTokenUser: %w", err)
	}
	if tu == nil || tu.User.Sid == nil {
		return "", fmt.Errorf("process token has no user SID")
	}
	return tu.User.Sid.String(), nil
}

// IsLocalSystem reports whether the current process token user is NT AUTHORITY\SYSTEM (S-1-5-18).
// Elevation, SCM presence, and account-name strings are not used.
func IsLocalSystem() (bool, error) {
	sid, err := ProcessUserSID()
	if err != nil {
		return false, err
	}
	wellKnown, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false, fmt.Errorf("WinLocalSystemSid: %w", err)
	}
	return strings.EqualFold(sid, wellKnown.String()) && IsLocalSystemSID(sid), nil
}

// IsAdministrator reports whether the current process token is a member of
// the builtin Administrators group -- distinct from Elevated(): a service/
// automation account (e.g. a CI runner) can be a full Administrators member
// with no UAC-split token at all, so IsElevated() alone does not tell you
// whether an ACL granting access to Administrators would let this process
// read a file.
func IsAdministrator() (bool, error) {
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, fmt.Errorf("WinBuiltinAdministratorsSid: %w", err)
	}
	member, err := windows.GetCurrentProcessToken().IsMember(admin)
	if err != nil {
		return false, fmt.Errorf("IsMember: %w", err)
	}
	return member, nil
}

// Describe returns a redacted identity label for preflight. It never includes secrets.
func Describe() (string, error) {
	sid, err := ProcessUserSID()
	if err != nil {
		return "", err
	}
	sys, err := IsLocalSystem()
	if err != nil {
		return "", err
	}
	return Format(sid, sys, Elevated()), nil
}

func Format(sid string, localSystem, elevated bool) string {
	if localSystem {
		return "LocalSystem (S-1-5-18)"
	}
	if elevated {
		return "Interactive administrator (" + sid + ")"
	}
	if sid == "" {
		return "unknown"
	}
	return "Interactive user (" + sid + ")"
}
