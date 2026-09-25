//go:build windows

package paths

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

const (
	fileAttributeReparsePoint       = 0x00000400
	fileAttributeRecallOnDataAccess = 0x00400000
	fileAttributeRecallOnOpen       = 0x00040000
	fileFlagBackupSemantics         = 0x02000000
	fileFlagOpenReparsePoint        = 0x00200000
)

func confinedOS(rootAbs, destAbs string) error {
	if err := validateRootOS(rootAbs); err != nil {
		return err
	}
	if IsForbiddenTarget(destAbs) {
		return domain.ErrRestorePathRejected
	}
	destParent := destAbs
	for !pathExists(destParent) {
		next := filepath.Dir(destParent)
		if next == destParent {
			break
		}
		destParent = next
	}
	destFinal, err := finalPathResolved(destParent)
	if err != nil {
		return err
	}
	if IsForbiddenTarget(destFinal) {
		return domain.ErrRestorePathRejected
	}
	rootFinal, err := finalPathResolved(rootAbs)
	if err != nil {
		return err
	}
	rf := NormalizeCompare(rootFinal)
	df := NormalizeCompare(destFinal)
	if df != rf && !strings.HasPrefix(df, rf+`\`) {
		return domain.ErrStagingEscape
	}
	if err := rejectReparseEscape(rootAbs, destAbs); err != nil {
		return err
	}
	return nil
}

func validateRootOS(root string) error {
	if IsForbiddenTarget(root) {
		return domain.ErrRestorePathRejected
	}
	if !pathExists(root) {
		return nil
	}
	if IsReparse(root) {
		return &Issue{Reason: "reparse-point", Path: root}
	}
	final, err := finalPathResolved(root)
	if err != nil {
		return err
	}
	if IsForbiddenTarget(final) {
		return domain.ErrRestorePathRejected
	}
	if IsReparse(final) {
		return &Issue{Reason: "reparse-point", Path: final}
	}
	return nil
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func finalPathResolved(p string) (string, error) {
	if !pathExists(p) {
		return filepath.Clean(p), nil
	}
	u16, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(
		u16,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		fileFlagBackupSemantics,
		0,
	)
	if err != nil {
		return "", &Issue{Reason: "final-path", Path: p}
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 4096)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil || n == 0 {
		return "", &Issue{Reason: "final-path", Path: p}
	}
	s := windows.UTF16ToString(buf[:n])
	s = strings.TrimPrefix(s, `\\?\`)
	s = strings.TrimPrefix(s, `UNC\`)
	s = strings.TrimRight(s, `\`)
	return s, nil
}

func rejectReparseEscape(root, dest string) error {
	cur := dest
	for {
		st, err := os.Lstat(cur)
		if err == nil {
			attr := uint32(0)
			if data, ok := st.Sys().(*syscall.Win32FileAttributeData); ok {
				attr = data.FileAttributes
			} else {
				p, _ := windows.UTF16PtrFromString(cur)
				attr, _ = windows.GetFileAttributes(p)
			}
			if attr&fileAttributeReparsePoint != 0 {
				return &Issue{Reason: "reparse-point", Path: cur}
			}
			if attr&(fileAttributeRecallOnDataAccess|fileAttributeRecallOnOpen) != 0 {
				return &Issue{Reason: "cloud-placeholder", Path: cur}
			}
		}
		next := filepath.Dir(cur)
		if strings.EqualFold(filepath.Clean(next), filepath.Clean(root)) || next == cur {
			break
		}
		cur = next
	}
	return nil
}

func FileAttributes(path string) (uint32, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	attr, err := windows.GetFileAttributes(p)
	if err != nil {
		return 0, err
	}
	return attr, nil
}

func IsReparse(path string) bool {
	attr, err := FileAttributes(path)
	if err != nil {
		return false
	}
	return attr&fileAttributeReparsePoint != 0
}

func IsCloudPlaceholder(path string) bool {
	attr, err := FileAttributes(path)
	if err != nil {
		return false
	}
	return attr&(fileAttributeRecallOnDataAccess|fileAttributeRecallOnOpen) != 0
}
