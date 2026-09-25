package scheduler

import (
	"testing"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func TestDecideDoesNotBackupEveryMinute(t *testing.T) {
	pol := utcPol("e", 0)
	due, today := mustSlot(t, "d", "i", "e", pol, time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	in := Input{
		Now:              due.Add(3 * time.Minute),
		Due:              due,
		TodayKey:         today,
		LastSucceededKey: today,
		TodayState:       domain.JobSucceeded,
		CatchUp:          true,
		Backoff:          time.Hour,
	}
	got := Decide(in)
	if got.Run {
		t.Fatalf("succeeded slot must not re-run: %+v", got)
	}
	if got.Reason != ReasonSkipComplete {
		t.Fatalf("reason=%s", got.Reason)
	}
}

func TestDecideCatchUpOneSlotNotHistory(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	pol := utcPol("e", 0)
	due, today := mustSlot(t, "d", "i", "e", pol, now)
	old := "d|i|e|2026-09-01T12:00:00Z"
	got := Decide(Input{
		Now:              now,
		Due:              due,
		TodayKey:         today,
		LastSucceededKey: old,
		CatchUp:          true,
		Backoff:          time.Hour,
		Location:         time.UTC,
	})
	if !got.Run || got.Reason != ReasonRunCatchUp || got.Slot != today {
		t.Fatalf("%+v", got)
	}
}

func TestDecideWaitsUntilDueOnFirstRun(t *testing.T) {
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	pol := utcPol("e", 0)
	due, today := mustSlot(t, "d", "i", "e", pol, now)
	got := Decide(Input{
		Now:      now,
		Due:      due,
		TodayKey: today,
		CatchUp:  true,
		Backoff:  time.Hour,
	})
	if got.Run || got.Reason != ReasonSkipUntilDue {
		t.Fatalf("%+v due=%s", got, due)
	}
}

func TestDecideBackoffAfterFailure(t *testing.T) {
	now := time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)
	pol := utcPol("e", 0)
	due, today := mustSlot(t, "d", "i", "e", pol, now)
	got := Decide(Input{
		Now:        now,
		Due:        due,
		TodayKey:   today,
		TodayState: domain.JobFailed,
		TodayEnded: now.Add(-10 * time.Minute),
		Backoff:    time.Hour,
		CatchUp:    true,
	})
	if got.Run || got.Reason != ReasonSkipBackoff {
		t.Fatalf("%+v", got)
	}
}

func TestDecideRetryAfterBackoff(t *testing.T) {
	now := time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)
	pol := utcPol("e", 0)
	due, today := mustSlot(t, "d", "i", "e", pol, now)
	got := Decide(Input{
		Now:        now,
		Due:        due,
		TodayKey:   today,
		TodayState: domain.JobFailed,
		TodayEnded: now.Add(-2 * time.Hour),
		Backoff:    time.Hour,
		CatchUp:    true,
	})
	if !got.Run {
		t.Fatalf("%+v", got)
	}
}

func TestDecideRestartDoesNotDuplicate(t *testing.T) {
	pol := utcPol("e", 0)
	now := time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)
	due, today := mustSlot(t, "d", "i", "e", pol, now)
	got := Decide(Input{
		Now:              now,
		Due:              due,
		TodayKey:         today,
		LastSucceededKey: today,
		TodayState:       domain.JobSucceeded,
	})
	if got.Run {
		t.Fatal("restart after success must not duplicate")
	}
}

func TestDecideBootAfterMissedScheduleWaitsForEligibility(t *testing.T) {
	pol := utcPol("e", 0)
	now := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC) // before 08:30
	due, today := mustSlot(t, "d", "i", "e", pol, now)
	got := Decide(Input{
		Now:              now,
		Due:              due,
		TodayKey:         today,
		LastSucceededKey: "d|i|e|2026-09-01T12:00:00Z",
		CatchUp:          true,
		Location:         time.UTC,
		EligibilityStart: time.Date(2026, 9, 9, 8, 30, 0, 0, time.UTC),
	})
	if got.Run || got.Reason != ReasonSkipUntilEligible {
		t.Fatalf("08:00 must not herd catch-up: %+v", got)
	}
}

func TestDecideCatchUpAfterEligibilityIsOneCurrentJob(t *testing.T) {
	now := time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC)
	pol := utcPol("e", 0)
	due, today := mustSlot(t, "d", "i", "e", pol, now)
	got := Decide(Input{
		Now:              now,
		Due:              due,
		TodayKey:         today,
		LastSucceededKey: "d|i|e|2026-09-06T12:00:00Z",
		CatchUp:          true,
		Location:         time.UTC,
		EligibilityStart: time.Date(2026, 9, 9, 8, 30, 0, 0, time.UTC),
		StartDeadline:    time.Date(2026, 9, 9, 16, 30, 0, 0, time.UTC),
		HardStop:         time.Date(2026, 9, 9, 17, 45, 0, 0, time.UTC),
	})
	if !got.Run || got.Reason != ReasonRunCatchUp || got.Slot != today {
		t.Fatalf("three missed days must collapse to one current slot: %+v", got)
	}
}

func TestDecideStartDeadlineBlocksNewJobs(t *testing.T) {
	now := time.Date(2026, 9, 9, 16, 31, 0, 0, time.UTC)
	got := Decide(Input{
		Now:           now,
		Due:           time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC),
		TodayKey:      "today",
		StartDeadline: time.Date(2026, 9, 9, 16, 30, 0, 0, time.UTC),
		HardStop:      time.Date(2026, 9, 9, 17, 45, 0, 0, time.UTC),
	})
	if got.Run || got.Reason != ReasonSkipStartDeadline {
		t.Fatalf("%+v", got)
	}
}

func TestDecideRetryBudgetStopsAfterThreeAttempts(t *testing.T) {
	now := time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)
	got := Decide(Input{
		Now:         now,
		Due:         now.Add(-time.Hour),
		TodayKey:    "today",
		TodayState:  domain.JobFailed,
		TodayEnded:  now.Add(-2 * time.Hour),
		AttemptNo:   3,
		MaxAttempts: 3,
	})
	if got.Run || got.Reason != ReasonSkipRetryBudget {
		t.Fatalf("%+v", got)
	}
}

func TestDecideBootDelay(t *testing.T) {
	now := time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC)
	got := Decide(Input{
		Now:              now,
		Due:              now.Add(3 * time.Hour),
		TodayKey:         "today",
		LastSucceededKey: "d|i|e|2026-09-01T12:00:00Z",
		CatchUp:          true,
		Location:         time.UTC,
		EligibilityStart: time.Date(2026, 9, 9, 8, 30, 0, 0, time.UTC),
		StartedAt:        now.Add(-2 * time.Minute),
		BootDelay:        10 * time.Minute,
	})
	if got.Run || got.Reason != ReasonSkipBootDelay {
		t.Fatalf("%+v", got)
	}
}

func TestDecidePolicyRevisionMiddayIsNewSlot(t *testing.T) {
	now := time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)
	pol := utcPol("pilot", 0)
	_, today := mustSlot(t, "d", "i", ScheduleEpoch("pilot", "rev-2"), pol, now)
	got := Decide(Input{
		Now:              now,
		Due:              now.Add(-time.Minute),
		TodayKey:         today,
		LastSucceededKey: "d|i|pilot/rev-1|2026-09-09T12:00:00Z",
		CatchUp:          true,
		Location:         time.UTC,
	})
	if !got.Run {
		t.Fatalf("new revision should schedule, not inherit old slot: %+v", got)
	}
	if got.Reason == ReasonRunCatchUp && got.Slot == "d|i|pilot/rev-1|2026-09-09T12:00:00Z" {
		t.Fatal("must not reuse previous revision slot")
	}
}
