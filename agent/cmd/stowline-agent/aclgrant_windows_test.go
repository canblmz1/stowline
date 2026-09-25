//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestGrantUsersReadReachesFilesAlreadyInsideTheDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "C", "Belgeler", "rapor.docx")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := grantUsersRead(dir); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(file, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sd.String(), ";;;BU)") {
		t.Fatalf("file DACL lacks the Users group: %s", sd.String())
	}
}
