//go:build windows

// Stowline Admin is the admin's program: the Stowline admin panel in its own
// window, with its own icon, remembering the login between runs. The panel
// itself (auth, Şifre Kasası reveal, audit) is unchanged -- this is only
// the window around it.
//
// Which panel it opens: --panel <url>, else "panel_url" in
// stowline-admin.json next to the exe, else the production panel.
package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/canblmz1/stowline/agent/internal/desktop"
)

const (
	windowTitle     = "Stowline Admin"
	defaultPanelURL = "https://backup.example.com"
)

func panelURL() string {
	args := os.Args[1:]
	for i, a := range args {
		if a == "--panel" && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--panel="); ok {
			return v
		}
	}
	if exe, err := os.Executable(); err == nil {
		if raw, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "stowline-admin.json")); err == nil {
			var cfg struct {
				PanelURL string `json:"panel_url"`
			}
			if json.Unmarshal(raw, &cfg) == nil && cfg.PanelURL != "" {
				return cfg.PanelURL
			}
		}
	}
	return defaultPanelURL
}

func main() {
	target := panelURL()
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		desktop.Alert(windowTitle, desktop.T("Panel adresi geçersiz: ", "Invalid panel address: ")+target+desktop.T("\nYalnızca https:// (ya da bu bilgisayardaki test paneli) açılabilir.", "\nOnly https:// (or a test panel on this computer) can be opened."))
		return
	}
	if !desktop.SingleInstance(`Local\Stowline-Yonetim`, windowTitle) {
		return
	}
	w, err := desktop.NewAppWindow(windowTitle, 1360, 880, "Management")
	if err != nil {
		desktop.Alert(windowTitle, desktop.T("Bu bilgisayarda Microsoft Edge WebView2 bileşeni bulunamadı.", "Microsoft Edge WebView2 was not found on this computer."))
		return
	}
	defer w.Destroy()
	if desktop.Reachable(target, 8*time.Second) {
		w.Navigate(target)
	} else {
		w.SetHtml(desktop.WaitingPage(desktop.T("Yönetim paneline bağlanılıyor", "Connecting to the admin panel"), desktop.T("İnternet bağlantısı bekleniyor: ", "Waiting for the internet connection: ")+u.Host))
		go func() {
			for !desktop.Reachable(target, 8*time.Second) {
				time.Sleep(3 * time.Second)
			}
			w.Dispatch(func() { w.Navigate(target) })
		}()
	}
	w.Run()
}
