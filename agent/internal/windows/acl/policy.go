package acl

import (
	"strings"

	"github.com/canblmz1/stowline/agent/internal/windows/identity"
)

// ACEView is a redacted DACL entry. It never includes secret file contents.
type ACEView struct {
	SID         string
	Allow       bool
	Deny        bool
	Mask        uint32
	Inherited   bool
	InheritOnly bool
	Unparsed    bool
}

// Inspection is a redacted DACL view. It never includes secret file contents.
type Inspection struct {
	SIDs              []string
	HasSystem         bool
	HasAdministrators bool
	HasCurrentUser    bool
	ACEs              []ACEView
}

// NTFS / generic bits that imply the trustee can read secret file bytes.
const (
	maskFileReadData    = 0x00000001
	maskGenericAll      = 0x10000000
	maskGenericRead     = 0x80000000
	maskMaximumAllowed  = 0x02000000
	maskFileAllAccess   = 0x001F01FF
	maskFileGenericRead = 0x00120089
)

func GrantsSecretRead(mask uint32) bool {
	if mask&maskFileReadData != 0 {
		return true
	}
	if mask&maskGenericRead != 0 || mask&maskGenericAll != 0 || mask&maskMaximumAllowed != 0 {
		return true
	}
	if mask&maskFileAllAccess == maskFileAllAccess {
		return true
	}
	if mask&maskFileGenericRead == maskFileGenericRead {
		return true
	}
	return false
}

// EvaluateServiceSecret reports whether aces match the ServiceSecret policy:
// allow SYSTEM (S-1-5-18) and BUILTIN\Administrators, and no secret-read allow
// for any other principal (including the current interactive user, Users,
// Authenticated Users, Everyone). Current process identity is not used: a
// LocalSystem token SID appearing as SYSTEM is required, not a failure.
func EvaluateServiceSecret(aces []ACEView) (ok bool, reason string) {
	var sysAllow, adminAllow bool
	for _, ace := range aces {
		if ace.Unparsed {
			return false, "unparsed ACE type"
		}
		sid := strings.TrimSpace(ace.SID)
		if sid == "" {
			continue
		}
		read := GrantsSecretRead(ace.Mask)
		if ace.Deny && read && identity.IsServiceSecretAllowedSID(sid) {
			return false, "deny ACE blocks SYSTEM or Administrators secret read"
		}
		if !ace.Allow || !read {
			continue
		}
		switch {
		case identity.IsLocalSystemSID(sid):
			sysAllow = true
		case identity.IsBuiltinAdministratorsSID(sid):
			adminAllow = true
		default:
			label := sid
			if identity.IsBroadPrincipalSID(sid) {
				label = "broad principal " + sid
			}
			return false, "unexpected allow ACE for " + label
		}
	}
	if !sysAllow {
		return false, "missing SYSTEM (S-1-5-18) allow"
	}
	if !adminAllow {
		return false, "missing Administrators allow"
	}
	return true, "SYSTEM+Administrators only"
}
