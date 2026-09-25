//go:build windows

// Stowline Setup is the one file a technician double-clicks: it asks for
// administrator rights (manifest), unpacks the installer package it
// carries (single-file build) or uses the package folder next to it,
// removes an earlier Stowline install that belongs to another server, starts
// the packaged setup (embedded Python running wizard\bootstrap.py -- the
// same path Setup.cmd uses) hidden, and shows the Turkish setup wizard in
// its own window. Closing the window stops the setup processes with it.
//
// Package layout (embedded payload.zip, or next to this exe):
//
//	deploy.json
//	payload\runtime\python.exe
//	payload\wizard\bootstrap.py
//	payload\wizard\remove_stowline.ps1
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/canblmz1/stowline/agent/internal/desktop"
)

const (
	windowTitle  = "Stowline Setup"
	pilotRoot    = `C:\Stowline`
	serviceName  = "StowlineBackup"
	powershellEx = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
)

var (
	jobMu    sync.Mutex
	setupJob windows.Handle
)

func main() {
	if !desktop.SingleInstance(`Local\Stowline-Kurulum`, windowTitle) {
		return
	}
	w, err := desktop.NewAppWindow(windowTitle, 1180, 820, "Kurulum")
	if err != nil {
		desktop.Alert(windowTitle, desktop.T("Bu bilgisayarda Microsoft Edge WebView2 bileşeni bulunamadı.", "Microsoft Edge WebView2 was not found on this computer."))
		return
	}
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	_ = w.Bind("stowlinePickFolder", func() (string, error) {
		p, err := desktop.PickFolder(hwnd, desktop.T("Yedeklenecek klasörü seçin", "Choose a folder to back up"), desktop.T("Bu klasörü ekle", "Add this folder"))
		if errors.Is(err, desktop.ErrCancelled) {
			return "", nil
		}
		return p, err
	})
	reinstall := make(chan bool, 1)
	_ = w.Bind("stowlineReinstall", func(yes bool) {
		select {
		case reinstall <- yes:
		default:
		}
	})
	logPath := filepath.Join(os.TempDir(), "stowline-setup.log")
	_ = os.WriteFile(logPath, nil, 0o600) // one log per run: cleanup + setup append to it
	show := func(html string) { w.Dispatch(func() { w.SetHtml(html) }) }
	fail := func(title, text string) {
		show(desktop.ErrorPage(title, text+desktop.T("\n\nBu ekranın fotoğrafını IT ekibine iletin.\n\n", "\n\nSend a photo of this screen to your IT team.\n\n")+logTail(logPath, 1200)))
	}

	w.SetHtml(desktop.WaitingPage(desktop.T("Kurulum hazırlanıyor", "Preparing setup"), desktop.T("Kurulum dosyaları açılıyor. Bu bir dakika kadar sürebilir.", "Unpacking the setup files. This can take about a minute.")))
	go func() {
		root, err := packageRoot()
		if err != nil {
			fail(desktop.T("Kurulum dosyaları açılamadı", "Could not unpack the setup files"), err.Error())
			return
		}
		old := detectOldInstall()
		switch {
		case desktop.NeedsCleanup(old, packagedControlURL(root)):
			show(desktop.WaitingPage(desktop.T("Eski Stowline kurulumu kaldırılıyor", "Removing the old Stowline install"), desktop.T("Bu bilgisayarda eski bir Stowline kurulumu bulundu. Kaldırılıp yerine yenisi kurulacak; eski ayarlar silinir. Bu bir-iki dakika sürebilir.", "An old Stowline install was found on this computer. It will be removed and replaced; old settings are deleted. This can take a minute or two.")))
			if err := removeOldInstall(root, logPath); err != nil {
				fail(desktop.T("Eski kurulum kaldırılamadı", "Could not remove the old install"), err.Error())
				return
			}
		case old.PilotRootExists || old.ServiceExists:
			// Already installed against this server. Setup over a running
			// install cannot work (the service locks its exe and its
			// registration already exists), so ask instead of failing.
			show(alreadyInstalledPage())
			if !<-reinstall {
				w.Dispatch(w.Terminate)
				return
			}
			show(desktop.WaitingPage(desktop.T("Stowline kaldırılıyor", "Removing Stowline"), desktop.T("Mevcut kurulum kaldırılıyor; ardından yeniden kurulacak. Bu bir-iki dakika sürebilir.", "Removing the current install; it is then installed again. This can take a minute or two.")))
			if err := removeOldInstall(root, logPath); err != nil {
				fail(desktop.T("Mevcut kurulum kaldırılamadı", "Could not remove the current install"), err.Error())
				return
			}
		}
		show(desktop.WaitingPage(desktop.T("Kurulum hazırlanıyor", "Preparing setup"), desktop.T("Kurulum dosyaları doğrulanıyor ve bilgisayara kopyalanıyor. Bu bir dakika kadar sürebilir.", "Verifying the setup files and copying them to this computer. This can take about a minute.")))
		runWizard(w, root, logPath, fail)
	}()
	w.Run()
	jobMu.Lock()
	if setupJob != 0 {
		windows.CloseHandle(setupJob) // KILL_ON_JOB_CLOSE: stops python + wizard with this window
	}
	jobMu.Unlock()
}

// alreadyInstalledPage is shown when Stowline is already installed and
// enrolled against this package's server on this PC.
func alreadyInstalledPage() string {
	return `<!doctype html><html lang="` + desktop.T("tr", "en") + `"><head><meta charset="utf-8"><style>
:root{color-scheme:light dark;--p:#f5f7fc;--c:#fff;--i:#111c2d;--m:#6b7285;--b:#004fda;--l:#dfe5f2;--g:#0f7b4a;--gs:#e3f4ea}
@media (prefers-color-scheme:dark){:root{--p:#0d1424;--c:#141d31;--i:#e8edf8;--m:#8a94ab;--b:#7da2ff;--l:#26314a;--g:#5fd49a;--gs:#12301f}}
html,body{height:100%;margin:0}body{background:var(--p);color:var(--i);font:14px "Segoe UI Variable Text","Segoe UI",system-ui,sans-serif;display:grid;place-items:center}
.c{text-align:center;max-width:540px;padding:24px}h1{font:600 22px "Segoe UI Variable Display","Segoe UI",system-ui,sans-serif;margin:16px 0 8px}p{color:var(--m);margin:0 0 20px;line-height:1.5}
.o{width:56px;height:56px;border-radius:50%;background:var(--gs);color:var(--g);display:grid;place-items:center;margin:0 auto;font:700 26px system-ui}
button{font:600 14px "Segoe UI",system-ui,sans-serif;border-radius:8px;padding:10px 18px;border:1px solid var(--l);background:var(--c);color:var(--i);cursor:pointer;margin:0 4px}
button.p{background:var(--b);border-color:var(--b);color:#fff}</style></head><body><div class="c"><div class="o">&#10003;</div>
<h1>` + desktop.T("Stowline bu bilgisayarda zaten kurulu", "Stowline is already installed on this computer") + `</h1>
<p>` + desktop.T("Yedekleme çalışıyor; bir şey yapmanız gerekmez. Yedeklenen klasörleri değiştirmek için masaüstündeki <b>Stowline Backups</b>'i ya da yönetim panelini kullanın.<br><br><b>Sıfırdan yeniden kur</b> seçilirse bu bilgisayar yeni bir kayıtla kurulur; eski yedekleri panelde eski kaydın altında kalır.", "Backups are running; there is nothing to do. To change the backed-up folders use <b>Stowline Backups</b> on the desktop or the admin panel.<br><br>With <b>Reinstall from scratch</b> this computer is installed as a new enrollment; its old backups stay under the old record in the panel.") + `</p>
<button onclick="window.stowlineReinstall(false)">` + desktop.T("Kapat", "Close") + `</button><button class="p" onclick="this.disabled=true;window.stowlineReinstall(true)">` + desktop.T("Sıfırdan yeniden kur", "Reinstall from scratch") + `</button>
</div></body></html>`
}

// packageRoot returns the directory holding deploy.json and payload\: the
// embedded package unpacked under ProgramData, or the folder of this exe.
func packageRoot() (string, error) {
	if len(embeddedPayload) > 0 {
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		dest := filepath.Join(base, "Stowline", "Kurulum")
		if err := desktop.ExtractPayload(embeddedPayload, dest); err != nil {
			return "", err
		}
		return dest, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	root := filepath.Dir(exe)
	for _, p := range []string{filepath.Join(root, "payload", "runtime", "python.exe"), filepath.Join(root, "payload", "wizard", "bootstrap.py")} {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf(desktop.T("kurulum paketi eksik: %s -- paketi yeniden indirip tüm klasörü birlikte açın", "setup package incomplete: %s -- download the package again and extract the whole folder"), p)
		}
	}
	return root, nil
}

func packagedControlURL(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, "deploy.json"))
	if err != nil {
		return ""
	}
	var d struct {
		ControlPlaneURL string `json:"control_plane_url"`
	}
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		raw = raw[3:]
	}
	_ = json.Unmarshal(raw, &d)
	return d.ControlPlaneURL
}

func detectOldInstall() desktop.OldInstall {
	var o desktop.OldInstall
	if _, err := os.Stat(pilotRoot); err == nil {
		o.PilotRootExists = true
	}
	if b, err := os.ReadFile(filepath.Join(pilotRoot, "config", "pilot.json")); err == nil {
		o.PilotJSON = b
	}
	if m, err := mgr.Connect(); err == nil {
		if s, err := m.OpenService(serviceName); err == nil {
			o.ServiceExists = true
			s.Close()
		}
		m.Disconnect()
	}
	return o
}

// removeOldInstall runs the package's own remove_stowline.ps1 (the script
// behind Remove.cmd: stops and deletes the StowlineBackup service, removes
// C:\Stowline) and checks that nothing is left.
func removeOldInstall(root, logPath string) error {
	script := filepath.Join(root, "payload", "wizard", "remove_stowline.ps1")
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf(desktop.T("kaldırma betiği pakette yok: %s", "the removal script is missing from the package: %s"), script)
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	proc, err := os.StartProcess(powershellEx, []string{powershellEx, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}, &os.ProcAttr{
		Dir:   root,
		Env:   os.Environ(),
		Files: []*os.File{nil, logf, logf},
		Sys:   &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW},
	})
	if err != nil {
		return err
	}
	st, err := proc.Wait()
	if err != nil {
		return err
	}
	left := detectOldInstall()
	if st.ExitCode() != 0 || left.ServiceExists || left.PilotRootExists {
		return fmt.Errorf(desktop.T("eski kurulum tamamen kaldırılamadı (çıkış kodu %d)", "the old install could not be removed completely (exit code %d)"), st.ExitCode())
	}
	return nil
}

func runWizard(w interface {
	Dispatch(func())
	Navigate(string)
}, root, logPath string, fail func(string, string)) {
	payload := filepath.Join(root, "payload")
	python := filepath.Join(payload, "runtime", "python.exe")
	bootstrap := filepath.Join(payload, "wizard", "bootstrap.py")
	token, port, err := sessionParams()
	if err != nil {
		fail(desktop.T("Kurulum başlatılamadı", "Setup could not start"), err.Error())
		return
	}
	job, proc, err := startSetup(root, payload, python, bootstrap, token, port, logPath)
	if err != nil {
		fail(desktop.T("Kurulum başlatılamadı", "Setup could not start"), err.Error())
		return
	}
	jobMu.Lock()
	setupJob = job
	jobMu.Unlock()
	exited := make(chan struct{})
	go func() {
		_, _ = proc.Wait()
		close(exited)
	}()
	base := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	for {
		select {
		case <-exited:
			fail(desktop.T("Kurulum durdu", "Setup stopped"), desktop.T("Kurulum başlamadan kapandı.", "Setup closed before it started."))
			return
		default:
		}
		if desktop.Reachable(base, time.Second) {
			w.Dispatch(func() { w.Navigate(base + "?t=" + token) })
			return
		}
		time.Sleep(700 * time.Millisecond)
	}
}

func sessionParams() (string, int, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", 0, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return hex.EncodeToString(b), port, nil
}

// startSetup runs payload\runtime\python.exe wizard\bootstrap.py hidden,
// with the same restricted environment Setup.cmd builds, inside a job
// object that dies with this process.
func startSetup(root, payload, python, bootstrap, token string, port int, logPath string) (windows.Handle, *os.Process, error) {
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, nil, err
	}
	defer logf.Close()
	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	env := []string{
		"STOWLINE_SETUP_ROOT=" + payload,
		"STOWLINE_WIZARD_TOKEN=" + token,
		"PYTHONIOENCODING=utf-8",
		"PATH=" + strings.Join([]string{filepath.Join(payload, "runtime"), filepath.Join(payload, "runtime", "Scripts"), filepath.Join(sysRoot, "System32"), sysRoot, filepath.Join(sysRoot, "System32", "Wbem")}, ";"),
	}
	if deploy := filepath.Join(root, "deploy.json"); fileExists(deploy) {
		env = append(env, "STOWLINE_DEPLOY_JSON="+deploy)
	}
	for _, k := range []string{"SystemRoot", "SystemDrive", "windir", "ProgramData", "ProgramFiles", "ProgramFiles(x86)", "PUBLIC", "USERPROFILE", "USERNAME", "COMPUTERNAME", "TEMP", "TMP", "LOCALAPPDATA", "APPDATA", "ALLUSERSPROFILE", "HOMEDRIVE", "HOMEPATH", "PROCESSOR_ARCHITECTURE", "NUMBER_OF_PROCESSORS", "OS", "PATHEXT", "ComSpec"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, nil, err
	}
	proc, err := os.StartProcess(python, []string{python, bootstrap, "--no-browser", "--port", strconv.Itoa(port)}, &os.ProcAttr{
		Dir:   root,
		Env:   env,
		Files: []*os.File{nil, logf, logf},
		Sys:   &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW},
	})
	if err != nil {
		windows.CloseHandle(job)
		return 0, nil, err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(proc.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, h)
		windows.CloseHandle(h)
	}
	if err != nil {
		_ = proc.Kill()
		windows.CloseHandle(job)
		return 0, nil, fmt.Errorf("could not supervise setup: %w", err)
	}
	return job, proc, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func logTail(p string, n int) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return desktop.T("(günlük okunamadı: ", "(could not read the log: ") + p + ")"
	}
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.TrimSpace(string(b))
}
