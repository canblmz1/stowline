//go:build windows

package paths

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
)

type KnownFolder struct {
	Name       string
	Path       string
	SID        string
	Redirected bool
	Reparse    bool
	Cloud      bool
	Source     string
}

func InteractiveUserProfiles() ([]KnownFolder, error) {
	var out []KnownFolder
	systemProfile := strings.ToLower(os.Getenv("SystemRoot") + `\System32\config\systemprofile`)
	entries, err := os.ReadDir(`C:\Users`)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "Public" || name == "Default" || name == "Default User" || name == "All Users" {
			continue
		}
		root := filepath.Join(`C:\Users`, name)
		if strings.EqualFold(root, os.Getenv("USERPROFILE")) && strings.Contains(strings.ToLower(root), `systemprofile`) {
			continue
		}
		if strings.ToLower(root) == systemProfile {
			continue
		}
		out = append(out, inspectFolder("profile", root, name))
		out = append(out, inspectFolder("Desktop", filepath.Join(root, "Desktop"), name))
		out = append(out, inspectFolder("Documents", filepath.Join(root, "Documents"), name))
		out = append(out, inspectFolder("Downloads", filepath.Join(root, "Downloads"), name))
	}
	if p := os.Getenv("USERPROFILE"); p != "" && !strings.Contains(strings.ToLower(p), "systemprofile") {
		desktop, _ := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0)
		docs, _ := windows.KnownFolderPath(windows.FOLDERID_Documents, 0)
		out = append(out, inspectFolder("KnownFolder:Desktop", desktop, currentUserSIDHint()))
		out = append(out, inspectFolder("KnownFolder:Documents", docs, currentUserSIDHint()))
	}
	return out, nil
}

func inspectFolder(kind, path, sid string) KnownFolder {
	kf := KnownFolder{Name: kind, Path: path, SID: sid, Source: "filesystem"}
	if path == "" {
		return kf
	}
	kf.Reparse = IsReparse(path)
	kf.Cloud = IsCloudPlaceholder(path)
	low := strings.ToLower(path)
	kf.Redirected = strings.Contains(low, `\onedrive`) || kf.Reparse
	return kf
}

func currentUserSIDHint() string {
	return "current-interactive-user"
}

func LocalSystemProfilePath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, `System32\config\systemprofile`)
}

func IsLocalSystemProfile(p string) bool {
	return strings.EqualFold(filepath.Clean(p), filepath.Clean(LocalSystemProfilePath()))
}
