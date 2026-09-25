package main

import (
	"context"
	"sync"
	"time"

	ctrlclient "github.com/canblmz1/stowline/agent/internal/control"
	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/scheduler"
)

type livePolicy struct {
	mu sync.Mutex
	p  domain.LocalPolicy
}

type liveBackupProgress struct {
	mu      sync.Mutex
	value   domain.BackupProgress
	present bool
}

func (p *liveBackupProgress) update(value domain.BackupProgress) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.value, p.present = value, true
	p.mu.Unlock()
}

func (p *liveBackupProgress) snapshot() *domain.BackupProgress {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.present {
		return nil
	}
	value := p.value
	return &value
}

// resetForNewRun marks a shared, service-lifetime instance as tracking a
// fresh backup, discarding whatever value a previous run left behind --
// used where liveBackupProgress is reused across many backups (the local
// status endpoint's source of truth) rather than allocated fresh per run.
func (p *liveBackupProgress) resetForNewRun() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.value, p.present = domain.BackupProgress{}, true
	p.mu.Unlock()
}

// clear marks a shared instance as not currently tracking any backup, so
// the local status endpoint reports idle rather than the last finished
// run's stale numbers.
func (p *liveBackupProgress) clear() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.value, p.present = domain.BackupProgress{}, false
	p.mu.Unlock()
}

func (l *livePolicy) get() domain.LocalPolicy {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.p
}

func (l *livePolicy) set(p domain.LocalPolicy) {
	l.mu.Lock()
	l.p = p
	l.mu.Unlock()
}

func wanClass(lastSucceeded string) domain.WANClass {
	if lastSucceeded == "" {
		return domain.WANClassInitialSeed
	}
	return domain.WANClassNormalBackup
}

func deadlineFor(now time.Time, pol domain.SchedulePolicy, class domain.WANClass) time.Time {
	loc, err := time.LoadLocation(pol.Timezone)
	if err != nil {
		loc = time.Local
	}
	runtime := pol.RuntimeForClass(class)
	dead := now.Add(runtime)
	if hard := scheduler.ClockHHMM(now, loc, pol.HardStopHHMM); !hard.IsZero() && hard.Before(dead) {
		dead = hard
	}
	return dead
}

func refuseWANStart(exec *ctrlclient.Executor, pol domain.LocalPolicy, kind domain.BackendKind) string {
	return ctrlclient.WANStartBlockReason(exec, pol, kind)
}

func holdLease(ctx context.Context, client *ctrlclient.Client, lease *ctrlclient.AdmissionResult, progress *liveBackupProgress) (stop func()) {
	if client == nil || lease == nil || !lease.Granted() {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_, _ = client.RenewLease(ctx, lease.LeaseID, progress.snapshot())
			}
		}
	}()
	return func() {
		once.Do(func() {
			close(done)
			_ = client.ReleaseLease(context.Background(), lease.LeaseID, "complete")
		})
	}
}
