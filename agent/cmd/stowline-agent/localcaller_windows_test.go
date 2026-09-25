//go:build windows

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	sd, err := windows.GetNamedSecurityInfo(file, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sd.String(), ";;;"+sid+")") {
		t.Fatalf("file DACL lacks the requester: %s", sd.String())
	}
	if grantReadTo(dir, "") == nil {
		t.Fatal("a grant without an account must fail, not fall back to everyone")
	}
}
