package acl

import (
	"testing"

	"github.com/canblmz1/stowline/agent/internal/windows/identity"
)

func allow(sid string, mask uint32) ACEView {
	return ACEView{SID: sid, Allow: true, Mask: mask}
}

func deny(sid string, mask uint32) ACEView {
	return ACEView{SID: sid, Deny: true, Mask: mask}
}

func TestEvaluateServiceSecretSystemAndAdministratorsPass(t *testing.T) {
	ok, reason := EvaluateServiceSecret([]ACEView{
		allow(identity.LocalSystemSID, maskGenericAll),
		allow(identity.BuiltinAdministratorsSID, maskGenericAll),
	})
	if !ok {
		t.Fatal(reason)
	}
}

func TestEvaluateServiceSecretSystemCurrentIdentityStillPass(t *testing.T) {
	// LocalSystem is the current token SID under a SYSTEM scheduled task.
	// Presence of S-1-5-18 must not be treated as an ordinary-user ACE.
	aces := []ACEView{
		allow(identity.LocalSystemSID, maskGenericAll),
		allow(identity.BuiltinAdministratorsSID, maskGenericAll),
	}
	ok, reason := EvaluateServiceSecret(aces)
	if !ok {
		t.Fatalf("SYSTEM current identity must PASS: %s", reason)
	}
}

func TestEvaluateServiceSecretOrdinaryUserFail(t *testing.T) {
	ok, reason := EvaluateServiceSecret([]ACEView{
		allow(identity.LocalSystemSID, maskGenericAll),
		allow(identity.BuiltinAdministratorsSID, maskGenericAll),
		allow("S-1-5-21-111-222-333-1001", maskGenericAll),
	})
	if ok {
		t.Fatal("ordinary user allow must FAIL")
	}
	if reason == "" {
		t.Fatal("expected reason")
	}
}

func TestEvaluateServiceSecretMissingSystemFail(t *testing.T) {
	ok, _ := EvaluateServiceSecret([]ACEView{
		allow(identity.BuiltinAdministratorsSID, maskGenericAll),
	})
	if ok {
		t.Fatal("missing SYSTEM must FAIL")
	}
}

func TestEvaluateServiceSecretMissingAdministratorsFail(t *testing.T) {
	ok, _ := EvaluateServiceSecret([]ACEView{
		allow(identity.LocalSystemSID, maskGenericAll),
	})
	if ok {
		t.Fatal("missing Administrators must FAIL")
	}
}

func TestEvaluateServiceSecretBroadUsersFail(t *testing.T) {
	base := []ACEView{
		allow(identity.LocalSystemSID, maskGenericAll),
		allow(identity.BuiltinAdministratorsSID, maskGenericAll),
	}
	for _, sid := range []string{identity.BuiltinUsersSID, identity.AuthenticatedUsersSID, identity.WorldSID} {
		aces := append(append([]ACEView{}, base...), ACEView{
			SID:       sid,
			Allow:     true,
			Mask:      maskFileGenericRead,
			Inherited: true,
		})
		ok, reason := EvaluateServiceSecret(aces)
		if ok {
			t.Fatalf("inherited %s read allow must FAIL: %s", sid, reason)
		}
	}
}

func TestEvaluateServiceSecretDenyRequiredPrincipalFail(t *testing.T) {
	ok, _ := EvaluateServiceSecret([]ACEView{
		deny(identity.LocalSystemSID, maskFileReadData),
		allow(identity.LocalSystemSID, maskGenericAll),
		allow(identity.BuiltinAdministratorsSID, maskGenericAll),
	})
	if ok {
		t.Fatal("deny SYSTEM read must FAIL regardless of allow order")
	}
}

func TestEvaluateServiceSecretDenyUsersStillPass(t *testing.T) {
	ok, reason := EvaluateServiceSecret([]ACEView{
		deny(identity.BuiltinUsersSID, maskGenericAll),
		allow(identity.LocalSystemSID, maskGenericAll),
		allow(identity.BuiltinAdministratorsSID, maskGenericAll),
	})
	if !ok {
		t.Fatalf("deny Users should not fail ServiceSecret: %s", reason)
	}
}

func TestEvaluateServiceSecretUnparsedFail(t *testing.T) {
	ok, _ := EvaluateServiceSecret([]ACEView{
		allow(identity.LocalSystemSID, maskGenericAll),
		allow(identity.BuiltinAdministratorsSID, maskGenericAll),
		{Unparsed: true},
	})
	if ok {
		t.Fatal("unparsed ACE must fail closed")
	}
}

func TestGrantsSecretRead(t *testing.T) {
	if !GrantsSecretRead(maskFileReadData) || !GrantsSecretRead(maskGenericAll) || !GrantsSecretRead(maskGenericRead) {
		t.Fatal("read/generic bits must count")
	}
	if GrantsSecretRead(0) || GrantsSecretRead(0x20000) { // READ_CONTROL alone
		t.Fatal("non-read bits must not count as secret read")
	}
}
