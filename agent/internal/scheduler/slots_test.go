package scheduler

import (
	"testing"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
)

func utcPol(epoch string, jitterMin int) domain.SchedulePolicy {
	return domain.SchedulePolicy{
		Epoch:            epoch,
		Timezone:         "UTC",
		WindowStartHHMM:  "12:00",
		WindowEndHHMM:    "16:00",
		CatchUp:          true,
		JitterMaxMinutes: jitterMin,
	}
}

func mustSlot(t *testing.T, device, install, epoch string, pol domain.SchedulePolicy, now time.Time) (time.Time, string) {
	t.Helper()
	due, key, err := NextSlot(device, install, epoch, pol, now)
	if err != nil {
		t.Fatal(err)
	}
	return due, key
}

func TestSlotStableAcrossClockRollbackWithinDay(t *testing.T) {
	pol := utcPol("epoch", 15)
	t1 := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	_, k1 := mustSlot(t, "dev", "inst", "epoch", pol, t1)
	_, k2 := mustSlot(t, "dev", "inst", "epoch", pol, t2)
	if k1 != k2 {
		t.Fatalf("%s vs %s", k1, k2)
	}
}

func TestCatchUpOneSlot(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	pol := utcPol("e", 0)
	_, today := mustSlot(t, "d", "i", "e", pol, now)
	key, needed, err := CatchUp("old", "d", "i", "e", pol, now)
	if err != nil || !needed || key != today {
		t.Fatalf("%v %v %s", err, needed, key)
	}
	_, needed, err = CatchUp(today, "d", "i", "e", pol, now)
	if err != nil || needed {
		t.Fatal("no catch-up when already completed today")
	}
}

func TestJitterDeterministic(t *testing.T) {
	a := deterministicJitter("seed", 15*time.Minute)
	b := deterministicJitter("seed", 15*time.Minute)
	if a != b {
		t.Fatal(a, b)
	}
}

func TestIstanbulWindow(t *testing.T) {
	pol := domain.SchedulePolicy{
		Epoch: "pilot", Timezone: "Europe/Istanbul",
		WindowStartHHMM: "12:00", WindowEndHHMM: "16:00", JitterMaxMinutes: 0,
	}
	// 12:00 Europe/Istanbul = 09:00 UTC in September (UTC+3).
	now := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	due, key, err := NextSlot("d", "i", "pilot", pol, now)
	if err != nil {
		t.Fatal(err)
	}
	if due.UTC() != time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC) {
		t.Fatalf("due=%s", due.UTC())
	}
	if key != "d|i|pilot|2026-09-09T09:00:00Z" {
		t.Fatalf("key=%s", key)
	}
	// Before window: still same civil day / same key.
	_, key2, err := NextSlot("d", "i", "pilot", pol, time.Date(2026, 9, 9, 0, 30, 0, 0, time.UTC))
	if err != nil || key2 != key {
		t.Fatalf("midnight boundary key=%s err=%v", key2, err)
	}
}

func TestUTCAndNewYorkDST(t *testing.T) {
	utc := utcPol("e", 0)
	due, _, err := NextSlot("d", "i", "e", utc, time.Date(2026, 9, 9, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if due.UTC() != time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC) {
		t.Fatalf("utc due=%s", due)
	}

	ny := domain.SchedulePolicy{
		Epoch: "e", Timezone: "America/New_York",
		WindowStartHHMM: "12:00", WindowEndHHMM: "16:00", JitterMaxMinutes: 0,
	}
	// 2026-03-08 US spring forward. Civil 12:00 EDT = 16:00 UTC.
	dueSpring, keySpring, err := NextSlot("d", "i", "e", ny, time.Date(2026, 3, 8, 20, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if dueSpring.UTC() != time.Date(2026, 3, 8, 16, 0, 0, 0, time.UTC) {
		t.Fatalf("spring due=%s", dueSpring.UTC())
	}
	// 2026-11-01 US fall back. Civil 12:00 EST = 17:00 UTC.
	dueFall, keyFall, err := NextSlot("d", "i", "e", ny, time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if dueFall.UTC() != time.Date(2026, 11, 1, 17, 0, 0, 0, time.UTC) {
		t.Fatalf("fall due=%s", dueFall.UTC())
	}
	if keySpring == keyFall {
		t.Fatal("distinct civil days must have distinct keys")
	}
}

func TestJitterClampedInsideWindow(t *testing.T) {
	pol := utcPol("e", 240) // 4h jitter on a 4h window → clamped to window-1s
	due, _, err := NextSlot("d", "i", "e", pol, time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, 9, 9, 16, 0, 0, 0, time.UTC)
	start := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	if due.Before(start) || !due.Before(end) {
		t.Fatalf("jitter escaped window: %s", due)
	}
}

func TestPolicyRevisionChangesSlotKey(t *testing.T) {
	pol := utcPol("pilot", 0)
	now := time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC)
	_, a, _ := NextSlot("d", "i", ScheduleEpoch("pilot", "rev-1"), pol, now)
	_, b, _ := NextSlot("d", "i", ScheduleEpoch("pilot", "rev-2"), pol, now)
	if a == b {
		t.Fatal("policy revision must change slot identity")
	}
}

func TestForwardJumpNewCivilDay(t *testing.T) {
	pol := utcPol("e", 0)
	_, k1 := mustSlot(t, "d", "i", "e", pol, time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	_, k2 := mustSlot(t, "d", "i", "e", pol, time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))
	if k1 == k2 {
		t.Fatal("forward jump must not reuse slot")
	}
}

func TestInvalidTimezoneRejected(t *testing.T) {
	pol := utcPol("e", 0)
	pol.Timezone = "Mars/Phobos"
	_, _, err := NextSlot("d", "i", "e", pol, time.Now())
	if err == nil {
		t.Fatal("expected tz error")
	}
}

func TestNextSlotRejectsInvalidWindow(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	badHH := utcPol("e", 0)
	badHH.WindowStartHHMM = "9:00"
	if _, _, err := NextSlot("d", "i", "e", badHH, now); err == nil {
		t.Fatal("invalid HH:MM must fail closed")
	}
	equal := utcPol("e", 0)
	equal.WindowEndHHMM = equal.WindowStartHHMM
	if _, _, err := NextSlot("d", "i", "e", equal, now); err == nil {
		t.Fatal("end <= start must fail closed")
	}
}
