//go:build windows

// Package desktop holds the Windows-only pieces the three Stowline desktop
// programs share: the app window (WebView2), the native folder picker,
// opening a folder in Explorer, the user's known folders, and a
// single-instance guard.
package desktop

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")

	shell32                         = windows.NewLazySystemDLL("shell32.dll")
	procSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")

	user32                  = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW         = user32.NewProc("FindWindowW")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procShowWindow          = user32.NewProc("ShowWindow")
	procIsIconic            = user32.NewProc("IsIconic")
	procMessageBoxW         = user32.NewProc("MessageBoxW")
)

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidIShellItem       = windows.GUID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE, Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
)

const (
	clsctxInprocServer   = 0x1
	coinitApartment      = 0x2
	fosPickFolders       = 0x20
	fosForceFileSystem   = 0x40
	fosPathMustExist     = 0x800
	sigdnFileSysPath     = 0x80058000
	hresultCancelled     = 0x800704C7
	vtblShow             = 3
	vtblSetOptions       = 9
	vtblSetFolder        = 12
	vtblGetOptions       = 10
	vtblSetTitle         = 17
	vtblSetOkButtonLabel = 18
	vtblGetResult        = 20
	vtblRelease          = 2
	vtblGetDisplayName   = 5
	swRestore            = 9
)

// ErrCancelled is returned by PickFolder when the user closes the dialog.
var ErrCancelled = errors.New("cancelled")

type comObject struct{ vtbl *[32]uintptr }

func comCall(obj *comObject, index int, args ...uintptr) uintptr {
	all := append([]uintptr{uintptr(unsafe.Pointer(obj))}, args...)
	r, _, _ := syscall.SyscallN(obj.vtbl[index], all...)
	return r
}

// PickFolder shows the Windows "Klasör seç" dialog (the same one Explorer
// uses) owned by hwnd and returns the chosen folder's full path. Must run
// on a thread that pumps window messages -- WebView2 bindings do.
func PickFolder(hwnd uintptr, title, okLabel string) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	procCoInitializeEx.Call(0, coinitApartment) // S_FALSE / RPC_E_CHANGED_MODE both fine: COM is usable

	var dlg *comObject
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if hr != 0 || dlg == nil {
		return "", fmt.Errorf("folder dialog unavailable (0x%08X)", uint32(hr))
	}
	defer comCall(dlg, vtblRelease)

	var opts uint32
	comCall(dlg, vtblGetOptions, uintptr(unsafe.Pointer(&opts)))
	comCall(dlg, vtblSetOptions, uintptr(opts|fosPickFolders|fosForceFileSystem|fosPathMustExist))
	// Open on "Bu bilgisayar" (drives), not the last-used or Documents
	// folder: on a PC whose Documents is redirected into OneDrive the
	// dialog would otherwise open inside the person's OneDrive.
	if thisPC, err := shellItem("::{20D04FE0-3AEA-1069-A2D8-08002B30309D}"); err == nil {
		comCall(dlg, vtblSetFolder, uintptr(unsafe.Pointer(thisPC)))
		comCall(thisPC, vtblRelease)
	}
	if title != "" {
		t, _ := windows.UTF16PtrFromString(title)
		comCall(dlg, vtblSetTitle, uintptr(unsafe.Pointer(t)))
	}
	if okLabel != "" {
		l, _ := windows.UTF16PtrFromString(okLabel)
		comCall(dlg, vtblSetOkButtonLabel, uintptr(unsafe.Pointer(l)))
	}
	hr = comCall(dlg, vtblShow, hwnd)
	if uint32(hr) == hresultCancelled {
		return "", ErrCancelled
	}
	if hr != 0 {
		return "", fmt.Errorf("folder dialog failed (0x%08X)", uint32(hr))
	}
	var item *comObject
	if hr = comCall(dlg, vtblGetResult, uintptr(unsafe.Pointer(&item))); hr != 0 || item == nil {
		return "", fmt.Errorf("no folder chosen (0x%08X)", uint32(hr))
	}
	defer comCall(item, vtblRelease)
	var name *uint16
	if hr = comCall(item, vtblGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&name))); hr != 0 || name == nil {
		return "", fmt.Errorf("chosen item is not a folder on disk (0x%08X)", uint32(hr))
	}
	defer windows.CoTaskMemFree(unsafe.Pointer(name))
	return windows.UTF16PtrToString(name), nil
}

func shellItem(parsingName string) (*comObject, error) {
	n, err := windows.UTF16PtrFromString(parsingName)
	if err != nil {
		return nil, err
	}
	var item *comObject
	hr, _, _ := procSHCreateItemFromParsingName.Call(uintptr(unsafe.Pointer(n)), 0, uintptr(unsafe.Pointer(&iidIShellItem)), uintptr(unsafe.Pointer(&item)))
	if hr != 0 || item == nil {
		return nil, fmt.Errorf("shell item %s (0x%08X)", parsingName, uint32(hr))
	}
	return item, nil
}

// RestoreRootForUser is where Stowline Backups puts delivered restores for
// the signed-in user (see RestoreRoot: never inside OneDrive).
func RestoreRootForUser() (string, error) {
	docs, err := windows.KnownFolderPath(windows.FOLDERID_Documents, 0)
	if err != nil {
		return "", err
	}
	profile, err := windows.KnownFolderPath(windows.FOLDERID_Profile, 0)
	if err != nil {
		return "", err
	}
	return RestoreRoot(docs, profile, []string{os.Getenv("OneDrive"), os.Getenv("OneDriveCommercial"), os.Getenv("OneDriveConsumer")}), nil
}

// OpenFolder shows path in a new Explorer window.
func OpenFolder(path string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// OpenURL opens an http(s) link in the default browser.
func OpenURL(url string) error { return OpenFolder(url) }

// KnownFolder is a folder the user is likely to want backed up.
type KnownFolder struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// KnownFolders returns the signed-in user's Masaüstü, Belgeler and
// Resimler (wherever OneDrive or a GPO has redirected them).
func KnownFolders() []KnownFolder {
	var out []KnownFolder
	for _, k := range []struct {
		name string
		id   *windows.KNOWNFOLDERID
	}{
		{T("Masaüstü", "Desktop"), windows.FOLDERID_Desktop},
		{T("Belgeler", "Documents"), windows.FOLDERID_Documents},
		{T("Resimler", "Pictures"), windows.FOLDERID_Pictures},
	} {
		if p, err := windows.KnownFolderPath(k.id, 0); err == nil && p != "" {
			out = append(out, KnownFolder{Name: k.name, Path: p})
		}
	}
	return out
}

// DocumentsDir is the signed-in user's Documents folder.
func DocumentsDir() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_Documents, 0)
}

// SingleInstance returns false when another copy already holds name; in
// that case it brings the window titled windowTitle to the front.
func SingleInstance(name, windowTitle string) bool {
	n, _ := windows.UTF16PtrFromString(name)
	h, err := windows.CreateMutex(nil, false, n)
	if err == nil || !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		_ = h // held for the life of the process
		return true
	}
	t, _ := windows.UTF16PtrFromString(windowTitle)
	if hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(t))); hwnd != 0 {
		if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
			procShowWindow.Call(hwnd, swRestore)
		}
		procSetForegroundWindow.Call(hwnd)
	}
	return false
}

// Alert shows a plain Windows message box (for failures before any window
// exists, e.g. WebView2 missing).
func Alert(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
}
