//go:build windows

package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/debug"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

const (
	// ShutdownBudget is the maximum time Execute waits after Stop/Shutdown
	// before returning to SCM. It is not a graceful child-quit window.
	ShutdownBudget = 60 * time.Second
	// ForcedKillAfter is the process-adapter bound used in service mode
	// before TerminateJobObject. It is forced termination.
	ForcedKillAfter = 2 * time.Second
)

const Name = "StowlineBackup"

type Runner interface {
	RunOnce(ctx context.Context) error
}

type commandDiagnosticSinkSetter interface {
	SetCommandDiagnosticSink(func(string))
}

type Service struct {
	Runner Runner
}

func IsServiceContext() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

func (s *Service) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const cmds = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var log elog = nopElog{}
	if l, err := eventlog.Open(Name); err == nil {
		defer l.Close()
		log = l
	}
	_ = log.Info(evStart, "Stowline service starting")
	if setter, ok := s.Runner.(commandDiagnosticSinkSetter); ok {
		// Lifecycle traces contain no payload or raw error. Rate-limit each
		// distinct marker/id combination so a transient redelivery loop cannot
		// flood Event Log while the service continues polling.
		var traceMu sync.Mutex
		lastTrace := map[string]time.Time{}
		setter.SetCommandDiagnosticSink(func(message string) {
			message = domain.Redact(message)
			now := time.Now()
			traceMu.Lock()
			defer traceMu.Unlock()
			if last, seen := lastTrace[message]; seen && now.Sub(last) < 5*time.Minute {
				return
			}
			lastTrace[message] = now
			_ = log.Info(evCommand, message)
		})
		defer setter.SetCommandDiagnosticSink(nil)
	}

	changes <- svc.Status{State: svc.Running, Accepts: cmds}

	var running atomic.Bool
	var lastErr atomic.Pointer[string]
	done := make(chan struct{}, 1)
	startTick := func() {
		if !running.CompareAndSwap(false, true) {
			return
		}
		go func() {
			defer running.Store(false)
			err := s.Runner.RunOnce(ctx)
			ReportTick(log, ctx, err, &lastErr)
			select {
			case done <- struct{}{}:
			default:
			}
		}()
	}

	// Catch-up / first due check immediately, then once a minute.
	startTick()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	qualTick := time.NewTicker(2 * time.Second)
	defer qualTick.Stop()
	var qualRunning atomic.Bool
	pollQual := func() {
		qr, ok := s.Runner.(QualPoller)
		if !ok {
			return
		}
		if !qualRunning.CompareAndSwap(false, true) {
			return
		}
		go func() {
			defer qualRunning.Store(false)
			err := qr.PollQualification(ctx)
			ReportTick(log, ctx, err, &lastErr)
		}()
	}
	pollQual()
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				_ = log.Info(evStop, "Stowline service stopping; cancelling in-flight work")
				cancel()
				deadline := time.After(ShutdownBudget)
				for running.Load() || qualRunning.Load() {
					select {
					case <-done:
					case <-time.After(100 * time.Millisecond):
					case <-deadline:
						_ = log.Warning(evStop, "in-flight backup did not stop within shutdown budget; returning to SCM; Job Object KillOnJobClose remains in effect")
						return false, 0
					}
				}
				return false, 0
			}
		case <-tick.C:
			startTick()
		case <-qualTick.C:
			pollQual()
		}
	}
}

func Run(runner Runner, interactive bool) error {
	s := &Service{Runner: runner}
	if interactive {
		return debug.Run(Name, s)
	}
	return svc.Run(Name, s)
}

func Install(exe string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	ex, err := m.OpenService(Name)
	if err == nil {
		ex.Close()
		return fmt.Errorf("service %s already installed", Name)
	}
	cfg := mgr.Config{
		DisplayName:      "Stowline Backup Agent",
		Description:      "Local scheduled encrypted backups using pinned Restic. Backs up configured SourceRoots only; does not use LocalSystem USERPROFILE as a user-data source.",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}
	// LocalSystem is required for Restic-managed VSS. Source roots come from
	// C:\Stowline\config\pilot.json, never from the service account profile.
	created, err := m.CreateService(Name, exe, cfg, "service", "run")
	if err != nil {
		return err
	}
	defer created.Close()
	_ = eventlog.InstallAsEventCreate(Name, eventlog.Error|eventlog.Warning|eventlog.Info)
	_ = setRestartOnFailure(created)
	return nil
}

// setRestartOnFailure is the shared recovery-action config for a fresh
// Install() and for EnsureRestartOnFailure below. A service is "failed", by
// SCM's own definition, only when it terminates without reporting
// SERVICE_STOPPED -- exactly what an automatic-agent-upgrade swap
// deliberately does (os.Exit, not a graceful Stop) to hand control back to
// SCM once the new binary is already staged on disk. Without this, that
// exit would just leave the service down until a human noticed.
func setRestartOnFailure(s *mgr.Service) error {
	return s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 5 * time.Second}}, uint32((24 * time.Hour).Seconds()))
}

// EnsureRestartOnFailure applies the same recovery policy to an already
// running, already installed service -- for a machine whose install
// predates this policy being added to Install(), so the very first
// automatic-upgrade swap on that machine is not the one time it needed the
// safety net and didn't have it yet. Safe to call every startup: setting
// the same recovery actions again is a no-op on Windows' side.
func EnsureRestartOnFailure() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return err
	}
	defer s.Close()
	return setRestartOnFailure(s)
}

func Remove() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Delete()
}

func QueryInstalled() (installed bool, binPath string, err error) {
	m, err := mgr.Connect()
	if err != nil {
		return false, "", err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return false, "", nil
	}
	defer s.Close()
	cfg, err := s.Config()
	if err != nil {
		return true, "", err
	}
	return true, cfg.BinaryPathName, nil
}

func Control(cmd string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return err
	}
	defer s.Close()
	switch cmd {
	case "start":
		return s.Start()
	case "stop":
		_, err := s.Control(svc.Stop)
		return err
	default:
		return fmt.Errorf("unknown control %s", cmd)
	}
}
