package identity

import "strings"

// Well-known SIDs used for process-identity and service-secret ACL policy.
// These are classification constants, not a substitute for reading the process token.
const (
	LocalSystemSID           = "S-1-5-18"
	BuiltinAdministratorsSID = "S-1-5-32-544"
	WorldSID                 = "S-1-1-0"
	AuthenticatedUsersSID    = "S-1-5-11"
	BuiltinUsersSID          = "S-1-5-32-545"
	InteractiveSID           = "S-1-5-4"
	BuiltinGuestsSID         = "S-1-5-32-546"
	CreatorOwnerSID          = "S-1-3-0"
	LocalSID                 = "S-1-2-0"
	NetworkSID               = "S-1-5-2"
)

func EqualSID(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func IsLocalSystemSID(sid string) bool {
	return EqualSID(sid, LocalSystemSID)
}

func IsBuiltinAdministratorsSID(sid string) bool {
	return EqualSID(sid, BuiltinAdministratorsSID)
}

// IsServiceSecretAllowedSID is the ServiceSecret allowlist: SYSTEM and Administrators only.
func IsServiceSecretAllowedSID(sid string) bool {
	return IsLocalSystemSID(sid) || IsBuiltinAdministratorsSID(sid)
}

// IsBroadPrincipalSID is a well-known group that must not receive secret-read on ServiceSecret.
func IsBroadPrincipalSID(sid string) bool {
	switch {
	case EqualSID(sid, WorldSID),
		EqualSID(sid, AuthenticatedUsersSID),
		EqualSID(sid, BuiltinUsersSID),
		EqualSID(sid, InteractiveSID),
		EqualSID(sid, BuiltinGuestsSID),
		EqualSID(sid, CreatorOwnerSID),
		EqualSID(sid, LocalSID),
		EqualSID(sid, NetworkSID):
		return true
	default:
		return false
	}
}
