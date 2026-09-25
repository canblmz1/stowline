package identity

import "testing"

func TestIsLocalSystemSID(t *testing.T) {
	if !IsLocalSystemSID("S-1-5-18") || !IsLocalSystemSID("s-1-5-18") {
		t.Fatal("LocalSystem SID must match")
	}
	for _, sid := range []string{"", "S-1-5-19", "S-1-5-18-0", "S-1-5-32-544", "SYSTEM", "NT AUTHORITY\\SYSTEM", "LocalSystem"} {
		if IsLocalSystemSID(sid) {
			t.Fatalf("must not classify %q as LocalSystem", sid)
		}
	}
}

func TestIsServiceSecretAllowedSID(t *testing.T) {
	if !IsServiceSecretAllowedSID(LocalSystemSID) || !IsServiceSecretAllowedSID(BuiltinAdministratorsSID) {
		t.Fatal("SYSTEM and Administrators must be allowed")
	}
	if IsServiceSecretAllowedSID("S-1-5-21-1-2-3-1001") || IsServiceSecretAllowedSID(BuiltinUsersSID) {
		t.Fatal("ordinary/user groups must not be allowlisted")
	}
}

func TestIsBroadPrincipalSID(t *testing.T) {
	for _, sid := range []string{WorldSID, AuthenticatedUsersSID, BuiltinUsersSID, InteractiveSID} {
		if !IsBroadPrincipalSID(sid) {
			t.Fatalf("%s must be treated as a broad principal", sid)
		}
	}
	if IsBroadPrincipalSID(LocalSystemSID) || IsBroadPrincipalSID(BuiltinAdministratorsSID) {
		t.Fatal("SYSTEM/Administrators are not broad user groups")
	}
}

func TestUsernameIsNotLocalSystem(t *testing.T) {
	if IsLocalSystemSID("SYSTEM") || IsLocalSystemSID("LocalSystem") || IsLocalSystemSID("NT AUTHORITY\\SYSTEM") {
		t.Fatal("must not infer LocalSystem from a name string")
	}
}
