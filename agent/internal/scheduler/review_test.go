package scheduler

import (
	"testing"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func TestReviewPartialRetriesAfterBackoff(t *testing.T) {
	now := time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)
	got := Decide(Input{Now: now, Due: now.Add(-time.Hour), TodayKey: "today", TodayState: domain.JobPartial, TodayEnded: now.Add(-2 * time.Hour), Backoff: time.Hour})
	if !got.Run {
		t.Fatal("partial incorrectly completes the daily slot")
	}
}

func TestReviewYesterdaySuccessDoesNotTriggerEarlyCatchup(t *testing.T) {
	now := time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC)
	pol := utcPol("e", 0)
	due, today := mustSlot(t, "d", "i", "e", pol, now)
	_, yesterday := mustSlot(t, "d", "i", "e", pol, now.AddDate(0, 0, -1))
	if got := Decide(Input{Now: now, Due: due, TodayKey: today, LastSucceededKey: yesterday, CatchUp: true, Location: time.UTC}); got.Run {
		t.Fatal("yesterday's successful backup treated as missed day")
	}
}
