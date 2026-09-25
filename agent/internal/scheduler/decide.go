package scheduler

import (
	"strings"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

// Input is the pure scheduling decision. No I/O.
type Input struct {
	Now              time.Time
	Due              time.Time
	TodayKey         string
	LastSucceededKey string
	TodayState       domain.JobState
	TodayEnded       time.Time
	Backoff          time.Duration
	CatchUp          bool
	Location         *time.Location
	EligibilityStart time.Time
	StartDeadline    time.Time
	HardStop         time.Time
	StartedAt        time.Time
	BootDelay        time.Duration
	AttemptNo        int
	MaxAttempts      int
}

// Decision says whether this tick may REQUEST WAN admission for today's slot.
// Run=true does not mean start restic.
type Decision struct {
	Run    bool
	Reason string
	Slot   string
}

const (
	ReasonRunScheduled      = "run_scheduled"
	ReasonRunCatchUp        = "run_catchup"
	ReasonSkipComplete      = "skip_complete"
	ReasonSkipUntilDue      = "skip_until_due"
	ReasonSkipBackoff       = "skip_backoff"
	ReasonSkipActive        = "skip_active"
	ReasonSkipUntilEligible = "skip_until_eligible"
	ReasonSkipStartDeadline = "skip_start_deadline"
	ReasonSkipHardStop      = "skip_hard_stop"
	ReasonSkipRetryBudget   = "skip_retry_budget"
	ReasonSkipBootDelay     = "skip_boot_delay"
	ReasonSkipSiteWait      = "skip_site_wait"
	ReasonSkipControlPlane  = "skip_control_plane"
	ReasonSkipNoAdmission   = "skip_no_admission"
	ReasonSkipPaused        = "skip_paused"
	ReasonSkipProvider      = "skip_provider_unavailable"
	ReasonSkipUnmeasured    = "skip_site_unmeasured"
)

// Decide implements: one logical slot per civil day, one catch-up, bounded retries.
func Decide(in Input) Decision {
	d := Decision{Slot: in.TodayKey}
	if in.TodayState == domain.JobSucceeded {
		d.Reason = ReasonSkipComplete
		return d
	}
	if in.TodayState == domain.JobActive || in.TodayState == domain.JobReconciling {
		d.Reason = ReasonSkipActive
		return d
	}
	maxAttempts := in.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if in.AttemptNo >= maxAttempts && (in.TodayState == domain.JobFailed || in.TodayState == domain.JobCancelled || in.TodayState == domain.JobPartial) {
		d.Reason = ReasonSkipRetryBudget
		return d
	}

	failedToday := in.TodayState == domain.JobFailed || in.TodayState == domain.JobCancelled || in.TodayState == domain.JobPartial
	if failedToday && in.Backoff > 0 && !in.TodayEnded.IsZero() && in.Now.Sub(in.TodayEnded) < in.Backoff {
		d.Reason = ReasonSkipBackoff
		return d
	}

	if !in.HardStop.IsZero() && !in.Now.Before(in.HardStop) {
		d.Reason = ReasonSkipHardStop
		return d
	}
	if !in.StartDeadline.IsZero() && !in.Now.Before(in.StartDeadline) {
		d.Reason = ReasonSkipStartDeadline
		return d
	}
	if !in.EligibilityStart.IsZero() && in.Now.Before(in.EligibilityStart) {
		d.Reason = ReasonSkipUntilEligible
		return d
	}
	if !in.StartedAt.IsZero() && in.BootDelay > 0 && in.Now.Before(in.StartedAt.Add(in.BootDelay)) {
		d.Reason = ReasonSkipBootDelay
		return d
	}

	todayParts := strings.Split(in.TodayKey, "|")
	lastParts := strings.Split(in.LastSucceededKey, "|")
	missedPrevious := false
	if len(todayParts) == 4 && len(lastParts) == 4 && strings.Join(todayParts[:3], "|") == strings.Join(lastParts[:3], "|") {
		today, te := time.Parse(time.RFC3339, todayParts[3])
		last, le := time.Parse(time.RFC3339, lastParts[3])
		if te == nil && le == nil {
			loc := in.Location
			if loc == nil {
				loc = time.UTC
			}
			todayCivil := civilDay(today, loc)
			lastCivil := civilDay(last, loc)
			missedPrevious = lastCivil.Before(todayCivil.AddDate(0, 0, -1))
		}
	}
	neverRan := in.LastSucceededKey == "" && in.TodayState == ""

	if missedPrevious && in.CatchUp {
		d.Run = true
		d.Reason = ReasonRunCatchUp
		return d
	}
	if in.Now.Before(in.Due) && neverRan {
		d.Reason = ReasonSkipUntilDue
		return d
	}
	if !in.Now.Before(in.Due) || failedToday {
		d.Run = true
		d.Reason = ReasonRunScheduled
		return d
	}
	d.Reason = ReasonSkipUntilDue
	return d
}

func civilDay(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
}

func ClockHHMM(now time.Time, loc *time.Location, hhmm string) time.Time {
	if loc == nil || hhmm == "" {
		return time.Time{}
	}
	h, m, err := domain.ParseHHMM(hhmm)
	if err != nil {
		return time.Time{}
	}
	l := now.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), h, m, 0, 0, loc)
}

func BootDelay(deviceID, installationID, civilDay string, minD, maxD time.Duration) time.Duration {
	if minD <= 0 {
		minD = 5 * time.Minute
	}
	if maxD <= minD {
		maxD = 20 * time.Minute
	}
	span := maxD - minD
	return minD + deterministicJitter(deviceID+"|"+installationID+"|"+civilDay+"|boot", span)
}
