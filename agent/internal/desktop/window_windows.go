//go:build windows

package desktop

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

// NewAppWindow opens a normal resizable application window hosting
// WebView2 (the Edge engine Windows 10/11 already ship). appDataName keeps
// each program's browser profile (cookies, login) under
// %LOCALAPPDATA%\Stowline\<name>, never next to the exe. The window icon is
// resource 1 of the exe (see the per-program rsrc_windows_amd64.syso).
func NewAppWindow(title string, width, height uint, appDataName string) (webview2.WebView, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(base, "Stowline", appDataName),
		WindowOptions: webview2.WindowOptions{
			Title:  title,
			Width:  width,
			Height: height,
			IconId: 1,
			Center: true,
		},
	})
	if w == nil {
		return nil, errors.New("WebView2 runtime unavailable")
	}
	w.SetSize(880, 600, webview2.HintMin)
	return w, nil
}

// Reachable reports whether url answers at all within timeout.
func Reachable(url string, timeout time.Duration) bool {
	c := http.Client{Timeout: timeout}
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// WaitingPage is a small self-contained page shown while a program waits
// for the thing it hosts (the Stowline service, the setup wizard) to come up.
func WaitingPage(title, text string) string {
	return `<!doctype html><html lang="` + T("tr", "en") + `"><head><meta charset="utf-8"><style>
:root{color-scheme:light dark;--p:#f5f7fc;--i:#111c2d;--m:#6b7285;--b:#004fda;--t:#e4eaf6}
@media (prefers-color-scheme:dark){:root{--p:#0d1424;--i:#e8edf8;--m:#8a94ab;--b:#7da2ff;--t:#22304d}}
html,body{height:100%;margin:0}body{background:var(--p);color:var(--i);font:14px "Segoe UI Variable Text","Segoe UI",system-ui,sans-serif;display:grid;place-items:center}
.c{text-align:center;max-width:460px;padding:24px}h1{font:600 22px "Segoe UI Variable Display","Segoe UI",system-ui,sans-serif;margin:18px 0 6px}p{color:var(--m);margin:0}
.s{width:44px;height:44px;border-radius:50%;border:4px solid var(--t);border-top-color:var(--b);margin:0 auto;animation:r 1s linear infinite}@keyframes r{to{transform:rotate(360deg)}}
@media (prefers-reduced-motion:reduce){.s{animation:none}}</style></head><body><div class="c"><div class="s"></div><h1>` + htmlEscape(title) + `</h1><p>` + htmlEscape(text) + `</p></div></body></html>`
}

// ErrorPage is WaitingPage's failure twin.
func ErrorPage(title, text string) string {
	return `<!doctype html><html lang="` + T("tr", "en") + `"><head><meta charset="utf-8"><style>
:root{color-scheme:light dark;--p:#f5f7fc;--i:#111c2d;--m:#6b7285;--r:#ba1a1a;--rs:#ffe4e1}
@media (prefers-color-scheme:dark){:root{--p:#0d1424;--i:#e8edf8;--m:#8a94ab;--r:#ff8a80;--rs:#3a1714}}
html,body{height:100%;margin:0}body{background:var(--p);color:var(--i);font:14px "Segoe UI Variable Text","Segoe UI",system-ui,sans-serif;display:grid;place-items:center}
.c{text-align:center;max-width:520px;padding:24px}h1{font:600 22px "Segoe UI Variable Display","Segoe UI",system-ui,sans-serif;margin:16px 0 6px}p{color:var(--m);margin:0;white-space:pre-wrap;overflow-wrap:anywhere}
.x{width:52px;height:52px;border-radius:50%;background:var(--rs);color:var(--r);display:grid;place-items:center;margin:0 auto;font:700 26px system-ui}</style></head><body><div class="c"><div class="x">!</div><h1>` + htmlEscape(title) + `</h1><p>` + htmlEscape(text) + `</p></div></body></html>`
}

func htmlEscape(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '&':
			out = append(out, []rune("&amp;")...)
		case '<':
			out = append(out, []rune("&lt;")...)
		case '>':
			out = append(out, []rune("&gt;")...)
		case '"':
			out = append(out, []rune("&quot;")...)
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
