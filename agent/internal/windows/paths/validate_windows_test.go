//go:build windows

package paths

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestTrailingSeparatorAndCase(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "Job")
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRestoreDestination(root+string(filepath.Separator), dest+string(filepath.Separator)); err != nil {
		t.Fatal(err)
	}
	if !IsForbiddenTarget(`c:\windows\`) || !IsForbiddenTarget(`C:\WINDOWS\System32`) {
		t.Fatal("forbidden compare must be case-insensitive and tolerate trailing separators")
	}
}

func TestUnicodeStagingPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "İstanbul-üğşöç")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "job-1")
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRestoreDestination(root, dest); err != nil {
		t.Fatal(err)
	}
}

func TestSpacesInStagingPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "restore staging")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "job with spaces")
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRestoreDestination(root, dest); err != nil {
		t.Fatal(err)
	}
}

func TestNormalRootAccepted(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "job")
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ValidateStagingRoot(root); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRestoreDestination(root, dest); err != nil {
		t.Fatal(err)
	}
}

func TestForbiddenResolvedTarget(t *testing.T) {
	if err := ValidateStagingRoot(`C:\Windows`); err == nil {
		t.Fatal("windows dir must be rejected")
	}
}

func TestJunctionedStagingRootRejected(t *testing.T) {
	outside := t.TempDir()
	base := t.TempDir()
	link := filepath.Join(base, "jroot")
	if err := makeDirReparse(link, outside); err != nil {
		t.Skipf("cannot create directory reparse: %v", err)
	}
	if err := ValidateStagingRoot(link); err == nil {
		t.Fatal("junctioned staging root must be rejected")
	}
}

func TestSubdirJunctionRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	sub := filepath.Join(root, "escape")
	if err := makeDirReparse(sub, outside); err != nil {
		t.Skipf("cannot create directory reparse: %v", err)
	}
	dest := filepath.Join(sub, "job")
	if err := ValidateRestoreDestination(root, dest); err == nil {
		t.Fatal("subdir junction must be rejected")
	}
}

func makeDirReparse(link, target string) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if err := createMountPoint(link, abs); err == nil {
		return nil
	} else {
		_ = os.Remove(link)
	}
	from, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return err
	}
	const (
		directory = 0x1
		unpriv    = 0x2
	)
	return windows.CreateSymbolicLink(from, to, directory|unpriv)
}

func createMountPoint(link, target string) error {
	if err := os.Mkdir(link, 0700); err != nil {
		return err
	}
	subst, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		return err
	}
	print, err := windows.UTF16FromString(target)
	if err != nil {
		return err
	}
	path := append(append([]uint16{}, subst...), print...)
	dataLen := 8 + len(path)*2
	buf := make([]byte, 8+dataLen)
	put32 := func(off int, v uint32) {
		buf[off] = byte(v)
		buf[off+1] = byte(v >> 8)
		buf[off+2] = byte(v >> 16)
		buf[off+3] = byte(v >> 24)
	}
	put16 := func(off int, v uint16) { buf[off] = byte(v); buf[off+1] = byte(v >> 8) }
	put32(0, 0xA0000003) // IO_REPARSE_TAG_MOUNT_POINT
	put16(4, uint16(dataLen))
	put16(8, 0)
	put16(10, uint16((len(subst)-1)*2))
	put16(12, uint16(len(subst)*2))
	put16(14, uint16((len(print)-1)*2))
	off := 16
	for _, u := range path {
		put16(off, u)
		off += 2
	}
	name, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, fileFlagBackupSemantics|fileFlagOpenReparsePoint, 0)
	if err != nil {
		_ = os.Remove(link)
		return err
	}
	defer windows.CloseHandle(h)
	var ret uint32
	if err := windows.DeviceIoControl(h, 0x000900A4, &buf[0], uint32(len(buf)), nil, 0, &ret, nil); err != nil {
		_ = os.Remove(link)
		return err
	}
	return nil
}
