package scheduler

import (
	"context"
	"sync"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"
)

const DefaultBackoff = time.Hour

// Controller is the local eligibility engine. It does not start restic without
// the Backup hook, which must acquire a WAN lease for REST/Drive backends.
type Controller struct {
	DeviceID         string
	InstallationID   string
	Policy           domain.SchedulePolicy
	PolicyRevisionID string
	Backoff          time.Duration
	Clock            ports.Clock
	Journal          ports.Journal
	Reconcile        func(context.Context) error
	Backup           func(ctx context.Context, slotKey string) error
	StartedAt        time.Time

	mu     sync.RWMutex
	LastOp string
}

func (c *Controller) now() time.Time {
	if c.Clock != nil {
		return c.Clock.Now()
	}
	return time.Now()
}

func (c *Controller) SetPolicy(pol domain.SchedulePolicy, revisionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Policy = pol
	c.PolicyRevisionID = revisionID
}

func (c *Controller) LastReason() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.LastOp
}

func (c *Controller) snapshotPolicy() (domain.SchedulePolicy, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Policy, c.PolicyRevisionID
}

func (c *Controller) Tick(ctx context.Context) (Decision, error) {
	if c.Reconcile != nil {
		if err := c.Reconcile(ctx); err != nil {
			return Decision{Reason: "reconcile_failed"}, err
		}
	}
	pol, rev := c.snapshotPolicy()
	if err := pol.Validate(); err != nil {
		return Decision{Reason: "policy_invalid"}, err
	}
	now := c.now()
	epoch := ScheduleEpoch(pol.Epoch, rev)
	due, today, err := NextSlot(c.DeviceID, c.InstallationID, epoch, pol, now)
	if err != nil {
		return Decision{Reason: "slot_error"}, err
	}
	last, err := c.Journal.LatestSucceededBackupSlot(ctx)
	if err != nil {
		return Decision{Slot: today, Reason: "journal_error"}, err
	}
	loc, _ := time.LoadLocation(pol.Timezone)
	in := Input{
		Now:              now,
		Due:              due,
		TodayKey:         today,
		LastSucceededKey: last,
		CatchUp:          pol.CatchUp,
		Location:         loc,
		EligibilityStart: ClockHHMM(now, loc, pol.EligibilityStartHHMM),
		StartDeadline:    ClockHHMM(now, loc, pol.WindowEndHHMM),
		HardStop:         ClockHHMM(now, loc, pol.HardStopHHMM),
		StartedAt:        c.StartedAt,
		MaxAttempts:      pol.MaxAttemptsOrDefault(),
	}
	if !c.StartedAt.IsZero() {
		civil := now.In(loc).Format("2006-01-02")
		in.BootDelay = BootDelay(c.DeviceID, c.InstallationID, civil, pol.BootDelayMin, pol.BootDelayMax)
	}
	if todayJob, err := c.Journal.JobBySlot(ctx, today); err != nil {
		return Decision{Slot: today, Reason: "journal_error"}, err
	} else if todayJob != nil {
		in.TodayState = todayJob.State
		if att, _ := c.Journal.LatestAttempt(ctx, todayJob.ID); att != nil {
			in.TodayEnded = att.EndedAt
			in.AttemptNo = att.AttemptNo
			in.Backoff = pol.RetryAfter(att.AttemptNo)
		}
	}
	if in.Backoff == 0 {
		in.Backoff = c.Backoff
	}
	d := Decide(in)
	c.mu.Lock()
	c.LastOp = d.Reason
	c.mu.Unlock()
	if !d.Run {
		return d, nil
	}
	if c.Backup == nil {
		return d, nil
	}
	return d, c.Backup(ctx, d.Slot)
}
