//go:build windows

// Stowline Backups is the program the person at the PC opens: it hosts the
// agent's local page (status, folders, restore, history, help) in its own
// window and adds what a web page cannot do -- the Windows folder picker,
// opening folders in Explorer, and copying restored files into the user's
// own profile (Belgeler, or the profile folder when Belgeler is in
// OneDrive) with the user's own permissions.
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	webview2 "github.com/jchv/go-webview2"

	"github.com/canblmz1/stowline/agent/internal/desktop"
)

const (
	windowTitle = "Stowline Backups"
	agentURL    = "http://127.0.0.1:18080/"
	// restoreArea is the agent's staging root (config.File.StagingRoot's
	// fixed pilot default); deliveries may only copy out of it.
	restoreArea = `C:\Stowline\Restore`
)

func main() {
	if !desktop.SingleInstance(`Local\Stowline-Yedeklerim`, windowTitle) {
		return
	}
	w, err := desktop.NewAppWindow(windowTitle, 1120, 780, "Yedeklerim")
	if err != nil {
		desktop.Alert(windowTitle, desktop.T("Bu bilgisayarda Microsoft Edge WebView2 bileşeni bulunamadı. Lütfen IT ekibine haber verin.", "Microsoft Edge WebView2 was not found on this computer. Please tell your IT team."))
		return
	}
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	bindNative(w, hwnd)

	if desktop.Reachable(agentURL+"api/status", 2*time.Second) {
		w.Navigate(agentURL)
	} else {
		w.SetHtml(desktop.WaitingPage(desktop.T("Stowline yedekleme hizmeti bekleniyor", "Waiting for the Stowline backup service"), desktop.T("Bilgisayar yeni açıldıysa bu birkaç saniye sürebilir. Bu pencere hazır olunca kendiliğinden açılacak.", "If the computer was just switched on this can take a few seconds. This window opens by itself when ready.")))
		go func() {
			for !desktop.Reachable(agentURL+"api/status", 2*time.Second) {
				time.Sleep(2 * time.Second)
			}
			w.Dispatch(func() { w.Navigate(agentURL) })
		}()
	}
	w.Run()
}

// deliveries tracks background copies into Documents, polled by the page.
type delivery struct {
	Done        bool   `json:"done"`
	Destination string `json:"destination,omitempty"`
	Error       string `json:"error,omitempty"`
	BytesDone   int64  `json:"bytes_done"`
	TotalBytes  int64  `json:"total_bytes"`
}

var (
	deliveriesMu sync.Mutex
	deliveries   = map[string]*delivery{}
)

// deliverShim gives the page one promise-returning call; the copy itself
// runs off the UI thread and is polled, so a large restore never freezes
// the window.
const deliverShim = `window.stowlineDeliverRestore = async function (jobJSON) {
  const token = await window.stowlineDeliverStart(jobJSON);
  for (;;) {
    await new Promise((r) => setTimeout(r, 400));
    const s = await window.stowlineDeliverPoll(token);
    if (s.done) return s.error ? { error: s.error } : { destination: s.destination };
  }
};`

func bindNative(w webview2.WebView, hwnd uintptr) {
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	must(w.Bind("stowlinePickFolder", func() (string, error) {
		p, err := desktop.PickFolder(hwnd, desktop.T("Yedeklenecek klasörü seçin", "Choose a folder to back up"), desktop.T("Bu klasörü yedekle", "Back up this folder"))
		if errors.Is(err, desktop.ErrCancelled) {
			return "", nil
		}
		return p, err
	}))
	must(w.Bind("stowlineOpenFolder", func(path string) error {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return errors.New(desktop.T("klasör bulunamadı", "folder not found"))
		}
		return desktop.OpenFolder(path)
	}))
	must(w.Bind("stowlineKnownFolders", func() []desktop.KnownFolder {
		return desktop.KnownFolders()
	}))
	must(w.Bind("stowlineDeliverStart", func(jobJSON string) (string, error) {
		var ref struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(jobJSON), &ref); err != nil || ref.ID == "" {
			return "", errors.New(desktop.T("geçersiz geri yükleme", "invalid restore"))
		}
		// Re-read the job from the agent instead of trusting what the page sent.
		job, err := fetchJob(ref.ID)
		if err != nil {
			return "", err
		}
		if job.State != "done" || job.Staging == "" {
			return "", errors.New(desktop.T("geri yükleme henüz bitmedi", "the restore has not finished yet"))
		}
		destRoot, err := desktop.RestoreRootForUser()
		if err != nil {
			return "", errors.New(desktop.T("Geri Yüklenenler klasörü belirlenemedi", "could not determine the Restored Files folder"))
		}
		d := &delivery{}
		deliveriesMu.Lock()
		deliveries[ref.ID] = d
		deliveriesMu.Unlock()
		go func() {
			dest, err := desktop.Deliver(job.Staging, restoreArea, job.Selections, destRoot, time.Now(), func(p desktop.DeliverProgress) {
				deliveriesMu.Lock()
				d.BytesDone, d.TotalBytes = p.BytesDone, p.TotalBytes
				deliveriesMu.Unlock()
			})
			deliveriesMu.Lock()
			d.Done, d.Destination = true, dest
			if err != nil {
				d.Error = err.Error()
			}
			deliveriesMu.Unlock()
		}()
		return ref.ID, nil
	}))
	must(w.Bind("stowlineDeliverPoll", func(token string) (delivery, error) {
		deliveriesMu.Lock()
		defer deliveriesMu.Unlock()
		d, ok := deliveries[token]
		if !ok {
			return delivery{}, errors.New("bilinmeyen kopyalama")
		}
		return *d, nil
	}))
	w.Init(deliverShim)
}

type restoreJob struct {
	ID         string   `json:"id"`
	State      string   `json:"state"`
	Staging    string   `json:"staging"`
	Selections []string `json:"selections"`
}

func fetchJob(id string) (restoreJob, error) {
	c := http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(agentURL + "api/restores/" + id)
	if err != nil {
		return restoreJob{}, errors.New(desktop.T("Stowline hizmetine ulaşılamadı", "could not reach the Stowline service"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return restoreJob{}, errors.New(desktop.T("geri yükleme bulunamadı", "restore not found"))
	}
	var job restoreJob
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return restoreJob{}, err
	}
	return job, nil
}
