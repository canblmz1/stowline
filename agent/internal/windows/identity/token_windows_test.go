//go:build windows

package identity

import "testing"

func TestIsLocalSystemMatchesProcessTokenSID(t *testing.T) {
	sid, err := ProcessUserSID()
	if err != nil {
		t.Fatal(err)
	}
	ok, err := IsLocalSystem()
	if err != nil {
		t.Fatal(err)
	}
	if IsLocalSystemSID(sid) != ok {
		t.Fatalf("IsLocalSystem=%v but process SID=%s", ok, sid)
	}
	if Elevated() && ok && !IsLocalSystemSID(sid) {
		t.Fatal("elevation must not be treated as LocalSystem")
	}
	detail, err := Describe()
	if err != nil {
		t.Fatal(err)
	}
	if ok && detail != "LocalSystem (S-1-5-18)" {
		t.Fatalf("Describe=%q", detail)
	}
	if !ok && detail == "LocalSystem (S-1-5-18)" {
		t.Fatal("Describe claimed LocalSystem without S-1-5-18")
	}
}
