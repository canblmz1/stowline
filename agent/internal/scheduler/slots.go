package scheduler

import (
	"hash/fnv"
	"time"

	"github.com/canblmz1/stowline/agent/internal/domain"
	"github.com/canblmz1/stowline/agent/internal/ports"

	_ "time/tzdata"
)

// ScheduleEpoch binds the schedule epoch to a policy revision so a midday
// revision cannot reuse an old civil-day slot key.
func ScheduleEpoch(epoch, revisionID string) string {
	if revisionID == "" {
		return epoch
	}
	return epoch + "/" + revisionID
}

// NextSlot returns the due instant and stable UTC slot key for the civil day
// of `now` in the policy timezone. The key is the window-start instant in UTC.
// Jitter is deterministic and clamped inside [WindowStart, WindowEnd).
func NextSlot(deviceID, installationID, epoch string, pol domain.SchedulePolicy, now time.Time) (due time.Time, key string, err error) {
	if err := pol.Validate(); err != nil {
		return time.Time{}, "", err
	}
	loc, err := time.LoadLocation(pol.Timezone)
	if err != nil {
		return time.Time{}, "", err
	}
	sh, sm, err := domain.ParseHHMM(pol.WindowStartHHMM)
	if err != nil {
		return time.Time{}, "", err
	}
	eh, em, err := domain.ParseHHMM(pol.WindowEndHHMM)
	if err != nil {
		return time.Time{}, "", err
	}
	local := now.In(loc)
	windowStart := time.Date(local.Year(), local.Month(), local.Day(), sh, sm, 0, 0, loc)
	windowEnd := time.Date(local.Year(), local.Month(), local.Day(), eh, em, 0, 0, loc)
	span := windowEnd.Sub(windowStart)
	jitterMax := time.Duration(pol.JitterMaxMinutes) * time.Minute
	if jitterMax >= span && span > time.Second {
		jitterMax = span - time.Second
	}
	seed := deviceID + installationID + epoch + windowStart.UTC().Format(time.RFC3339)
	jitter := deterministicJitter(seed, jitterMax)
	due = windowStart.Add(jitter)
	if !due.Before(windowEnd) {
		due = windowEnd.Add(-time.Second)
	}
	key = domain.SlotKey(deviceID, installationID, epoch, windowStart.UTC())
	return due, key, nil
}

func deterministicJitter(seed string, max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	n := h.Sum64() % uint64(max)
	return time.Duration(n)
}

// CatchUp returns today's slot when a catch-up is still outstanding.
func CatchUp(lastCompletedKey string, deviceID, installationID, epoch string, pol domain.SchedulePolicy, now time.Time) (key string, needed bool, err error) {
	_, today, err := NextSlot(deviceID, installationID, epoch, pol, now)
	if err != nil {
		return "", false, err
	}
	if lastCompletedKey == today {
		return "", false, nil
	}
	return today, true, nil
}

func Duration(clock ports.Clock, start time.Time) time.Duration {
	if clock == nil {
		return time.Since(start)
	}
	return clock.Since(start)
}
