//go:build windows

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func currentSID(t *testing.T) string {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return u.User.Sid.String()
}

func TestTheCallerIsTheAccountThatOpenedTheConnection(t *testing.T) {
	var got localCaller
	var gotErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, gotErr = resolveLocalCaller(r)
	}))
	defer srv.Close()
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if gotErr != nil {
		t.Fatalf("resolve: %v", gotErr)
	}
	if got.SID != currentSID(t) {
		t.Fatalf("caller SID = %q, want %q", got.SID, currentSID(t))
	}
	if home, _ := os.UserHomeDir(); home != "" && got.Profile != snapshotPathKey(home) {
		t.Fatalf("profile = %q, want %q", got.Profile, snapshotPathKey(home))
	}
	for _, o := range got.OtherProfiles {
		if o == got.Profile {
			t.Fatal("own profile listed as someone else's")
		}
	}
}

// daclAllows compares SIDs, not SDDL text: SDDL writes some accounts as
// aliases (the built-in Administrator is "LA"), not as S-1-5-... With
// explicit, only an ACE set on path itself counts, not an inherited one.
func daclAllows(t *testing.T, path, sid string, explicit bool) bool {
	t.Helper()
	want, err := windows.StringToSid(sid)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, i, &ace) != nil {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(want) {
			continue
		}
		if explicit && ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			continue
		}
		return true
	}
	return false
}

func TestRestoredFilesAreReadableByTheRequesterNotEveryUser(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "C", "Belgeler", "rapor.docx")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sid := currentSID(t)
	if err := grantReadTo(dir, sid); err != nil {
		t.Fatal(err)
	}
	if !daclAllows(t, dir, sid, true) {
		t.Fatalf("the restore folder has no ACE of its own for the requester %s", sid)
	}
	if !daclAllows(t, file, sid, false) {
		t.Fatalf("files already inside do not get the requester's access %s", sid)
	}
	if grantReadTo(dir, "") == nil {
		t.Fatal("a grant without an account must fail, not fall back to everyone")
	}
}
