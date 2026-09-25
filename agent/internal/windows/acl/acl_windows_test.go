//go:build windows

package acl

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/canblmz1/stowline/agent/internal/windows/identity"
)

func TestPilotWorkingKeepsOwnerAccess(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker.txt")
	if err := os.WriteFile(marker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(dir, PilotWorking); err != nil {
		t.Skipf("ACL change not permitted in this context: %v", err)
	}
	got, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasSystem || !got.HasAdministrators || !got.HasCurrentUser {
		t.Fatalf("pilot ACL: %+v", got)
	}
	if _, err := os.ReadFile(marker); err != nil {
		t.Fatalf("owner/admin should still read after pilot ACL: %v", err)
	}
}

func TestServiceSecretOmitsInteractiveUser(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "restic-password.machine.dpapi")
	if err := os.WriteFile(marker, []byte("envelope-placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Apply(dir, PilotWorking); err != nil {
		t.Skipf("ACL change not permitted: %v", err)
	}
	t.Cleanup(func() { _ = Apply(dir, PilotWorking) })

	if err := Apply(dir, ServiceSecret); err != nil {
		t.Skipf("service ACL not permitted: %v", err)
	}
	got, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasSystem || !got.HasAdministrators {
		t.Fatalf("service secret ACL missing SYSTEM/Administrators: %+v", got)
	}
	ok, reason := EvaluateServiceSecret(got.ACEs)
	if !ok {
		t.Fatalf("service-secret policy: %s (%+v)", reason, got)
	}
	sys, err := identity.IsLocalSystem()
	if err != nil {
		t.Fatal(err)
	}
	if !sys && got.HasCurrentUser {
		t.Fatalf("ordinary current user must not have an ACE on service-secret ACL: %+v", got)
	}
	// A read succeeding here is only a real leak if the account running
	// this test is neither SYSTEM nor a member of Administrators -- the
	// ACL is supposed to grant Administrators access (got.HasAdministrators
	// above), and CI runners commonly execute as a full Administrators
	// member with no UAC-split token, unlike a typical interactive
	// non-admin desktop user.
	admin, err := identity.IsAdministrator()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(marker); err == nil && !sys && !admin {
		t.Fatal("interactive user must not read service-secret files")
	}
}

func TestServiceStateACL(t *testing.T) {
	dir := t.TempDir()
	if err := Apply(dir, ServiceState); err != nil {
		t.Skipf("ACL change not permitted: %v", err)
	}
	t.Cleanup(func() { _ = Apply(dir, PilotWorking) })
	got, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasSystem || !got.HasAdministrators {
		t.Fatalf("%+v", got)
	}
	ok, reason := EvaluateServiceSecret(got.ACEs)
	if !ok {
		t.Fatalf("service-state policy: %s", reason)
	}
	sys, err := identity.IsLocalSystem()
	if err != nil {
		t.Fatal(err)
	}
	if !sys && got.HasCurrentUser {
		t.Fatalf("service-state must not grant ordinary user: %+v", got)
	}
}

func TestRestrictAliasIsPilotWorking(t *testing.T) {
	dir := t.TempDir()
	if err := RestrictToAdminSystemAndOwner(dir); err != nil {
		t.Skipf("ACL change not permitted: %v", err)
	}
	got, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.HasCurrentUser {
		t.Fatal("RestrictToAdminSystemAndOwner must remain the pilot convenience ACL")
	}
}

func TestServiceSecretPolicyLiveDACLs(t *testing.T) {
	dir := t.TempDir()
	if err := Apply(dir, ServiceSecret); err != nil {
		t.Skipf("ACL change not permitted: %v", err)
	}
	t.Cleanup(func() { _ = Apply(dir, PilotWorking) })

	got, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := EvaluateServiceSecret(got.ACEs); !ok {
		t.Fatalf("SYSTEM+Administrators Apply must PASS: %s %+v", reason, got)
	}

	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	users, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}

	set := func(entries []windows.EXPLICIT_ACCESS) {
		t.Helper()
		a, err := windows.ACLFromEntries(entries, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, a, nil); err != nil {
			t.Skipf("SetNamedSecurityInfo: %v", err)
		}
	}
	mustFail := func(name string) {
		t.Helper()
		got, err := Inspect(dir)
		if err != nil {
			t.Fatal(err)
		}
		if ok, reason := EvaluateServiceSecret(got.ACEs); ok {
			t.Fatalf("%s: expected FAIL, got PASS (%s) aces=%+v", name, reason, got.ACEs)
		}
	}
	mustPass := func(name string) {
		t.Helper()
		got, err := Inspect(dir)
		if err != nil {
			t.Fatal(err)
		}
		if ok, reason := EvaluateServiceSecret(got.ACEs); !ok {
			t.Fatalf("%s: expected PASS, got %s aces=%+v", name, reason, got.ACEs)
		}
	}

	set([]windows.EXPLICIT_ACCESS{
		grantAll(system, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
		grantAll(admin, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
		grantAll(cur, windows.TRUSTEE_IS_USER),
	})
	mustFail("SYSTEM+Administrators+ordinary user")

	set([]windows.EXPLICIT_ACCESS{
		grantAll(admin, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
	})
	mustFail("missing SYSTEM")

	set([]windows.EXPLICIT_ACCESS{
		grantAll(system, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
	})
	mustFail("missing Administrators")

	set([]windows.EXPLICIT_ACCESS{
		grantAll(system, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
		grantAll(admin, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
		{
			AccessPermissions: windows.GENERIC_READ,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(users),
			},
		},
	})
	mustFail("Users read allow")

	set([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.DENY_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(system),
			},
		},
		grantAll(system, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
		grantAll(admin, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
	})
	mustFail("deny SYSTEM before allow")

	set([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.DENY_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(users),
			},
		},
		grantAll(system, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
		grantAll(admin, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
	})
	mustPass("deny Users plus SYSTEM+Administrators")
}
