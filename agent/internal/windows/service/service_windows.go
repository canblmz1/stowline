//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
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

type commandDiagnosticSinkSetter interface {
	SetCommandDiagnosticSink(func(string))
}

type Service struct {
	Startup     Startup
	Interactive bool
	Runner      Runner
	// Internal seams exercise Execute itself without reconfiguring the host or
	// terminating the test process. Run uses the real implementations below.
	ensureRecovery func() error
	restart        func(chan<- svc.Status) error
	log            elog
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

	var log elog = s.log
	if log == nil {
		log = nopElog{}
		if l, err := eventlog.Open(Name); err == nil {
			defer l.Close()
			log = l
		}
	}
	_ = log.Info(evStart, "Stowline service starting")
	fail := func(err error) (bool, uint32) {
		message := domain.Redact(err.Error())
		_ = log.Error(evStart, message)
		fmt.Fprintln(os.Stderr, message)
		return true, 1
	}
	if !s.Interactive {
		ensure := s.ensureRecovery
		if ensure == nil {
			ensure = EnsureRestartOnFailure
		}
		if err := ensure(); err != nil {
			return fail(fmt.Errorf("automatic update recovery unavailable: %w", err))
		}
	}
	if s.Startup != nil {
		runner, err := s.Startup()
		if err != nil {
			message := "service initialization: " + domain.Redact(err.Error())
			if errors.Is(err, ErrRestartRequired) {
				_ = log.Warning(evStart, message)
			} else {
				_ = log.Error(evStart, message)
			}
			fmt.Fprintln(os.Stderr, message)
			if s.Interactive {
				return true, 1
			}
			restart := s.restart
			if restart == nil {
				restart = restartRolledBackService
			}
			return fail(restart(changes)) // Only returns if the handoff failed.
		}
		s.Runner = runner
	}
	if s.Runner == nil {
		return fail(fmt.Errorf("service startup returned no runner"))
	}
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

// restartRolledBackService is called only inside Execute, after SCM registered
// its status handle and recovery was verified. The Running channel send first
// orders svc.Run's preceding StartPending update before our synchronous update:
// its unbuffered receiver finishes each update before accepting the next. Both
// remaining updates are Running, so no late StartPending can overwrite it.
// SetServiceStatus then confirms the handoff before os.Exit; a channel send
// alone would not guarantee completion. No application work has started.
// Deliberately do not report SERVICE_STOPPED: non-crash recovery may be disabled.
func restartRolledBackService(changes chan<- svc.Status) error {
	return restartRegisteredService(changes, func() error {
		status := windows.SERVICE_STATUS{
			ServiceType:  windows.SERVICE_WIN32_OWN_PROCESS,
			CurrentState: windows.SERVICE_RUNNING,
		}
		return windows.SetServiceStatus(svc.StatusHandle(), &status)
	}, func() { os.Exit(1) })
}

func restartRegisteredService(changes chan<- svc.Status, reportRunning func() error, exit func()) error {
	changes <- svc.Status{State: svc.Running}
	if err := reportRunning(); err != nil {
		return fmt.Errorf("rollback restart status: %w", err)
	}
	exit()
	return fmt.Errorf("SCM restart termination unexpectedly returned")
}

func Run(start Startup, interactive bool) error {
	s := &Service{Startup: start, Interactive: interactive}
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
	if err := setRestartOnFailure(created); err != nil {
		return errors.Join(fmt.Errorf("configure service recovery: %w", err), created.Delete())
	}
	return nil
}

// setRestartOnFailure is the shared recovery-action config for a fresh
// Install() and for EnsureRestartOnFailure below. A service is "failed", by
// SCM's own definition, only when it terminates without reporting
// SERVICE_STOPPED -- exactly what an automatic-agent-upgrade swap
// deliberately does (os.Exit, not a graceful Stop) to hand control back to
// SCM once the new binary is already staged on disk. Without this, that
// exit would just leave the service down until a human noticed.
type recoveryService interface {
	SetRecoveryActions([]mgr.RecoveryAction, uint32) error
	RecoveryActions() ([]mgr.RecoveryAction, error)
	ResetPeriod() (uint32, error)
}

func setRestartOnFailure(s recoveryService) error {
	const reset = uint32((24 * time.Hour) / time.Second)
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 5 * time.Second}}, reset); err != nil {
		return err
	}
	actions, err := s.RecoveryActions()
	if err != nil {
		return fmt.Errorf("read recovery actions: %w", err)
	}
	if len(actions) != 1 || actions[0].Type != mgr.ServiceRestart || actions[0].Delay != 5*time.Second {
		return fmt.Errorf("SCM recovery actions do not match restart policy")
	}
	period, err := s.ResetPeriod()
	if err != nil {
		return fmt.Errorf("read recovery reset period: %w", err)
	}
	if period != reset {
		return fmt.Errorf("SCM recovery reset period does not match restart policy")
	}
	return nil
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
