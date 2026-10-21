//go:build windows

package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type startupRunner struct{ ran chan struct{} }

func (r startupRunner) RunOnce(context.Context) error { close(r.ran); return nil }

func TestExecuteRollbackHandoffPrecedesAllApplicationWork(t *testing.T) {
	var order []string
	changes := make(chan svc.Status, 8)
	exit := errors.New("test process exit")
	s := &Service{
		log: nopElog{},
		ensureRecovery: func() error {
			if status := <-changes; status.State != svc.StartPending {
				t.Fatalf("handler did not announce startup: %v", status)
			}
			order = append(order, "connected/recovery")
			return nil
		},
		Startup: func() (Runner, error) {
			order = append(order, "guard")
			return nil, ErrRestartRequired
		},
		restart: func(chan<- svc.Status) error { order = append(order, "restart"); panic(exit) },
		Runner:  startupRunner{ran: make(chan struct{})},
	}
	func() {
		defer func() {
			if got := recover(); got != exit {
				t.Fatalf("handoff did not exit: %v", got)
			}
		}()
		s.Execute(nil, make(chan svc.ChangeRequest), changes)
	}()
	if !reflect.DeepEqual(order, []string{"connected/recovery", "guard", "restart"}) {
		t.Fatal(order)
	}
	select {
	case <-s.Runner.(startupRunner).ran:
		t.Fatal("work ran during rollback")
	default:
	}
}

func TestExecuteRecoveryFailurePreventsGuardAndPreparation(t *testing.T) {
	s := &Service{log: nopElog{}, ensureRecovery: func() error { return errors.New("access denied") },
		Startup: func() (Runner, error) { t.Fatal("startup ran without recovery"); return nil, nil }}
	if specific, code := s.Execute(nil, nil, make(chan svc.Status, 8)); !specific || code == 0 {
		t.Fatal("recovery failure was hidden")
	}
}

func TestExecuteInitializationFailureUsesCrashRecovery(t *testing.T) {
	exited := errors.New("process terminated")
	s := &Service{log: nopElog{}, ensureRecovery: func() error { return nil },
		Startup: func() (Runner, error) { return nil, errors.New("config/engine failure") },
		restart: func(chan<- svc.Status) error { panic(exited) }}
	defer func() {
		if recover() != exited {
			t.Fatal("initialization returned SERVICE_STOPPED instead of crash recovery")
		}
	}()
	s.Execute(nil, nil, make(chan svc.Status, 8))
}

func TestRestartHandoffWaitsForOrderedRunningAndSynchronousStatus(t *testing.T) {
	changes := make(chan svc.Status)
	reportEntered, releaseReport := make(chan struct{}), make(chan struct{})
	exitCalled, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- restartRegisteredService(changes, func() error {
			close(reportEntered)
			<-releaseReport
			return nil
		}, func() { close(exitCalled) })
	}()
	select {
	case <-reportEntered:
		t.Fatal("synchronous status raced ahead of ordered channel status")
	default:
	}
	if status := <-changes; status.State != svc.Running {
		t.Fatal(status)
	}
	<-reportEntered
	select {
	case <-exitCalled:
		t.Fatal("exit before synchronous SetServiceStatus completed")
	default:
	}
	close(releaseReport)
	if err := <-done; err == nil {
		t.Fatal("returning exit function not reported")
	}
	<-exitCalled
}

func TestExecuteInteractiveStartNeverReconfiguresInstalledService(t *testing.T) {
	ran := make(chan struct{})
	s := &Service{Interactive: true, log: nopElog{},
		ensureRecovery: func() error { t.Error("interactive start reconfigured SCM"); return nil },
		Startup:        func() (Runner, error) { return startupRunner{ran}, nil }}
	requests := make(chan svc.ChangeRequest)
	changes := make(chan svc.Status, 8)
	done := make(chan uint32, 1)
	go func() { _, code := s.Execute(nil, requests, changes); done <- code }()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("normal service work never started")
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal(code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("service did not stop")
	}
}

type recoveryFixture struct {
	setErr, readErr error
	actions         []mgr.RecoveryAction
	period          uint32
}

func (f recoveryFixture) SetRecoveryActions([]mgr.RecoveryAction, uint32) error { return f.setErr }
func (f recoveryFixture) RecoveryActions() ([]mgr.RecoveryAction, error)        { return f.actions, f.readErr }
func (f recoveryFixture) ResetPeriod() (uint32, error)                          { return f.period, nil }

func TestRecoveryPolicyMustBeAppliedAndReadBack(t *testing.T) {
	good := recoveryFixture{actions: []mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 5 * time.Second}}, period: 86400}
	if err := setRestartOnFailure(good); err != nil {
		t.Fatal(err)
	}
	for name, fixture := range map[string]recoveryFixture{
		"set denied":    {setErr: errors.New("denied")},
		"read denied":   {readErr: errors.New("denied")},
		"empty actions": {},
		"wrong action":  {actions: []mgr.RecoveryAction{{Type: mgr.NoAction}}, period: 86400},
		"wrong delay":   {actions: []mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: time.Hour}}, period: 86400},
		"wrong reset":   {actions: good.actions, period: 0},
	} {
		t.Run(name, func(t *testing.T) {
			if setRestartOnFailure(fixture) == nil {
				t.Fatal("unsafe recovery policy accepted")
			}
		})
	}
}
